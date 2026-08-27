package clients

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// piperProcess is a live piper in line-at-a-time mode.
type piperProcess struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	// acks carries one entry per finished utterance. It is buffered so a
	// reply that arrives after its request gave up does not wedge the reader.
	acks   chan string
	stderr *tailBuffer
	dir    string
	// done closes when the process exits, so a request can tell a dead piper
	// from a slow one.
	done chan struct{}
}

// synthesizePersistent hands one utterance to the running piper and reads back
// the WAV it wrote. The mutex serialises requests because the protocol is a
// single ordered stream: two in flight would interleave their acknowledgements.
func (p *Piper) synthesizePersistent(ctx context.Context, text string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	proc, err := p.ensureProcess()
	if err != nil {
		return nil, err
	}

	p.seq++
	line, want, err := p.engine.request(proc.dir, p.seq, text)
	if err != nil {
		return nil, err
	}
	if want != "" {
		defer os.Remove(want)
	}

	if _, err := io.WriteString(proc.stdin, line+"\n"); err != nil {
		p.stopLocked()
		return nil, fmt.Errorf("writing to piper: %w (%s)", err, proc.stderr.snippet())
	}

	var path string
	select {
	case <-ctx.Done():
		// A half-finished utterance leaves the stream out of step with us, so
		// the process cannot be reused.
		p.stopLocked()
		return nil, fmt.Errorf("piper timed out after %s", p.timeout())
	case <-proc.done:
		p.stopLocked()
		return nil, fmt.Errorf("piper exited (%s)", proc.stderr.snippet())
	case path = <-proc.acks:
	}

	if want != "" && path != want {
		// Piper answered about a different utterance: the stream is
		// desynchronised and the only safe move is a fresh process.
		p.stopLocked()
		return nil, fmt.Errorf("piper acknowledged %q, expected %q", path, want)
	}
	if want == "" {
		// The engine named the file, so this is the one to clean up.
		defer os.Remove(path)
	}

	audio, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading piper output: %w (%s)", err, proc.stderr.snippet())
	}
	if len(audio) == 0 {
		return nil, fmt.Errorf("piper produced no audio (%s)", proc.stderr.snippet())
	}
	return audio, nil
}

// Warm starts the persistent process ahead of the first reply. Piper loads the
// voice before it reads anything, so paying that at boot keeps the first answer
// of the day as quick as the rest. It is a no-op in one-shot mode.
func (p *Piper) Warm() error {
	if !p.opts.Persistent {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.Available(); err != nil {
		return err
	}
	_, err := p.ensureProcess()
	return err
}

// ensureProcess returns a running piper, starting or replacing one as needed.
// The caller holds the mutex.
func (p *Piper) ensureProcess() (*piperProcess, error) {
	if p.proc != nil {
		select {
		case <-p.proc.done:
			// Died between requests, most likely killed by the OOM reaper.
			p.stopLocked()
		default:
			return p.proc, nil
		}
	}

	dir, err := os.MkdirTemp("", "renfild-tts-")
	if err != nil {
		return nil, fmt.Errorf("creating piper work directory: %w", err)
	}

	cmd := exec.Command(p.opts.Binary, p.engine.daemonArgs(p.opts, dir)...)
	cmd.Dir = dir
	// Without a delay, Wait blocks until every writer to the output pipes is
	// gone, so a stray grandchild could hold shutdown open indefinitely.
	cmd.WaitDelay = 2 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("piper stdin: %w", err)
	}

	// Piper logs a line per utterance; keeping only the tail means a long-lived
	// process cannot grow a buffer without bound, and an error still has
	// context attached.
	tail := &tailBuffer{limit: 4096}
	var acked io.Reader
	if p.engine.acksOnStderr() {
		// piper1-gpl announces each finished file among its log lines, so the
		// reader has to sort acknowledgements from noise.
		acked, err = cmd.StderrPipe()
	} else {
		acked, err = cmd.StdoutPipe()
		cmd.Stderr = tail
	}
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("piper output: %w", err)
	}

	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("starting piper: %w", err)
	}

	proc := &piperProcess{
		cmd:    cmd,
		stdin:  stdin,
		acks:   make(chan string, 1),
		stderr: tail,
		dir:    dir,
		done:   make(chan struct{}),
	}
	go readAcks(acked, p.engine, tail, proc.acks)
	go func() {
		cmd.Wait()
		close(proc.done)
	}()

	p.proc = proc
	return proc, nil
}

// readAcks routes completion notices to the request in flight and everything
// else to the tail buffer.
func readAcks(r io.Reader, engine piperEngine, tail *tailBuffer, acks chan<- string) {
	scanner := bufio.NewScanner(r)
	// A path or a log line, but a voice that mangles its input could produce a
	// long one; give the scanner room before it gives up.
	scanner.Buffer(make([]byte, 0, 8*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if path, ok := engine.parseAck(line); ok {
			select {
			case acks <- path:
			default:
				// Nobody is waiting: the request gave up and this process is
				// on its way out.
			}
			continue
		}
		tail.Write([]byte(line + "\n"))
	}
}

// Close shuts the persistent process down. It is safe to call on a one-shot
// client and safe to call twice.
func (p *Piper) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
	return nil
}

// stopLocked terminates the current process and clears it. The caller holds
// the mutex.
func (p *Piper) stopLocked() {
	proc := p.proc
	if proc == nil {
		return
	}
	p.proc = nil

	// Closing stdin is how piper is meant to be told to stop; the kill is for
	// the case where it is wedged mid-synthesis.
	proc.stdin.Close()
	select {
	case <-proc.done:
	case <-time.After(2 * time.Second):
		proc.cmd.Process.Kill()
		<-proc.done
	}
	os.RemoveAll(proc.dir)
}

// tailBuffer keeps the last limit bytes written to it, so a failing process can
// report what it said on the way out without retaining a whole run of logs.
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

// snippet returns the tail trimmed to something fit for an error message.
func (t *tailBuffer) snippet() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return snippet(t.buf)
}
