package clients

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// piperRequest is one line of piper's --json-input protocol. Piper answers
// each line by writing the WAV and echoing the path back on stdout, which
// gives us a clean per-utterance completion marker.
type piperRequest struct {
	Text       string `json:"text"`
	OutputFile string `json:"output_file"`
}

// piperProcess is a live piper instance in --json-input mode.
type piperProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *tailBuffer
	dir    string
	// done closes when the process exits, so a request can tell a dead piper
	// from a slow one.
	done chan struct{}
}

// synthesizePersistent hands one line to the running piper and reads back the
// WAV it wrote. The mutex serialises requests because the protocol is a single
// ordered stream: two utterances in flight would interleave their acks.
func (p *Piper) synthesizePersistent(ctx context.Context, text string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	proc, err := p.ensureProcess()
	if err != nil {
		return nil, err
	}

	p.seq++
	output := filepath.Join(proc.dir, fmt.Sprintf("reply-%d.wav", p.seq))
	defer os.Remove(output)

	line, err := json.Marshal(piperRequest{Text: text, OutputFile: output})
	if err != nil {
		return nil, fmt.Errorf("encoding piper request: %w", err)
	}
	if _, err := proc.stdin.Write(append(line, '\n')); err != nil {
		p.stopLocked()
		return nil, fmt.Errorf("writing to piper: %w (%s)", err, proc.stderr.snippet())
	}

	ack := make(chan struct {
		line string
		err  error
	}, 1)
	go func() {
		got, err := proc.stdout.ReadString('\n')
		ack <- struct {
			line string
			err  error
		}{got, err}
	}()

	select {
	case <-ctx.Done():
		// A half-finished utterance leaves the pipe out of step with us, so
		// the process cannot be reused.
		p.stopLocked()
		return nil, fmt.Errorf("piper timed out after %s", p.timeout())
	case <-proc.done:
		p.stopLocked()
		return nil, fmt.Errorf("piper exited (%s)", proc.stderr.snippet())
	case got := <-ack:
		if got.err != nil {
			p.stopLocked()
			return nil, fmt.Errorf("reading from piper: %w (%s)", got.err, proc.stderr.snippet())
		}
		if strings.TrimSpace(got.line) != output {
			// Piper answered about a different utterance: the stream is
			// desynchronised and the only safe move is a fresh process.
			p.stopLocked()
			return nil, fmt.Errorf("piper acknowledged %q, expected %q", strings.TrimSpace(got.line), output)
		}
	}

	audio, err := os.ReadFile(output)
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

	args := append(p.voiceArgs(), "--json-input", "--output_dir", dir)
	cmd := exec.Command(p.opts.Binary, args...)
	cmd.Dir = dir
	// Without a delay, Wait blocks until every writer to the stderr pipe is
	// gone, so a stray grandchild could hold shutdown open indefinitely.
	cmd.WaitDelay = 2 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("piper stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("piper stdout: %w", err)
	}
	// Piper logs a line per utterance; keeping only the tail means a long-lived
	// process cannot grow a buffer without bound, and an error still has
	// context attached.
	stderrTail := &tailBuffer{limit: 4096}
	cmd.Stderr = stderrTail

	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("starting piper: %w", err)
	}

	proc := &piperProcess{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
		stderr: stderrTail,
		dir:    dir,
		done:   make(chan struct{}),
	}
	go func() {
		cmd.Wait()
		close(proc.done)
	}()

	p.proc = proc
	return proc, nil
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
