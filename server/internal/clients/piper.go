package clients

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// TTS turns text into a WAV blob. Piper implements it today; an HTTP engine
// would implement it tomorrow without the pipeline noticing.
type TTS interface {
	Synthesize(ctx context.Context, text string) ([]byte, error)
}

// PiperOptions configures the Piper text-to-speech client.
type PiperOptions struct {
	// Engine is "cpp" (the rhasspy/piper release binary, the default) or
	// "python" (piper1-gpl, the one that can speak Hebrew).
	Engine    string
	Binary    string
	Voice     string
	SpeakerID int
	Timeout   time.Duration
	// ExtraArgs is passed verbatim to the binary, for voice-specific tuning
	// such as --length_scale.
	ExtraArgs []string
	// Persistent keeps one piper process alive between replies instead of
	// spawning it per utterance. Loading the voice costs a second or more on a
	// Pi 4 and a typical answer is only two seconds of audio, so paying that
	// once at startup rather than on every reply nearly halves time-to-speech.
	Persistent bool
}

// Piper drives a Piper build. In one-shot mode it feeds text on stdin and reads
// a WAV back from stdout. In persistent mode it keeps a process alive and
// exchanges one line per utterance; see piper_daemon.go.
type Piper struct {
	opts   PiperOptions
	engine piperEngine
	// engineErr defers an unknown-engine name to the first call, so building a
	// client never fails and the operator sees the problem in one place.
	engineErr error

	mu   sync.Mutex
	proc *piperProcess
	seq  uint64
}

// NewPiper builds a Piper TTS client.
func NewPiper(opts PiperOptions) *Piper {
	engine, err := engineFor(opts.Engine)
	return &Piper{opts: opts, engine: engine, engineErr: err}
}

// Available reports whether the engine is known and the binary and the voice
// model are both present.
func (p *Piper) Available() error {
	if p.engineErr != nil {
		return p.engineErr
	}
	if _, err := os.Stat(p.opts.Binary); err != nil {
		return fmt.Errorf("piper binary %s: %w", p.opts.Binary, err)
	}
	if _, err := os.Stat(p.opts.Voice); err != nil {
		return fmt.Errorf("piper voice %s: %w", p.opts.Voice, err)
	}
	return nil
}

// timeout is the per-utterance budget, defaulted when unset.
func (p *Piper) timeout() time.Duration {
	if p.opts.Timeout <= 0 {
		return 10 * time.Second
	}
	return p.opts.Timeout
}

// Synthesize speaks text and returns a WAV blob.
func (p *Piper) Synthesize(ctx context.Context, text string) ([]byte, error) {
	text = flatten(text)
	if text == "" {
		return nil, fmt.Errorf("nothing to synthesize")
	}
	if err := p.Available(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()

	if p.opts.Persistent {
		return p.synthesizePersistent(ctx, text)
	}
	return p.synthesizeOneShot(ctx, text)
}

// flatten collapses a reply onto one line. Every piper build splits stdin on
// newlines, so a two-line reply would otherwise become two utterances and, in
// persistent mode, two acknowledgements for one request.
func flatten(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// synthesizeOneShot spawns piper, hands it the text and collects the WAV it
// writes to stdout.
func (p *Piper) synthesizeOneShot(ctx context.Context, text string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, p.opts.Binary, p.engine.oneShotArgs(p.opts)...)
	cmd.Stdin = strings.NewReader(text + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("piper timed out after %s", p.timeout())
		}
		return nil, fmt.Errorf("running piper: %w (%s)", err, snippet(stderr.Bytes()))
	}
	if stdout.Len() == 0 {
		return nil, fmt.Errorf("piper produced no audio (%s)", snippet(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}
