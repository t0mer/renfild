package clients

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
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
	Binary    string
	Voice     string
	SpeakerID int
	Timeout   time.Duration
	// ExtraArgs is passed verbatim to the binary, for voice-specific tuning
	// such as --length_scale.
	ExtraArgs []string
	// Persistent keeps one piper process alive between replies instead of
	// spawning it per utterance. Loading the voice costs about a second on a
	// Pi 4 and a typical answer is only two seconds of audio, so paying that
	// once at startup rather than on every reply nearly halves time-to-speech.
	Persistent bool
}

// Piper drives the Piper binary. In one-shot mode it feeds text on stdin and
// reads a WAV back from stdout. In persistent mode it keeps a process alive in
// --json-input mode and exchanges one line per utterance; see piper_daemon.go.
type Piper struct {
	opts PiperOptions

	mu   sync.Mutex
	proc *piperProcess
	seq  uint64
}

// NewPiper builds a Piper TTS client.
func NewPiper(opts PiperOptions) *Piper {
	return &Piper{opts: opts}
}

// Available reports whether the binary and the voice model are both present.
func (p *Piper) Available() error {
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

// voiceArgs are the arguments shared by both modes.
func (p *Piper) voiceArgs() []string {
	args := []string{"--model", p.opts.Voice}
	if p.opts.SpeakerID > 0 {
		args = append(args, "--speaker", strconv.Itoa(p.opts.SpeakerID))
	}
	return append(args, p.opts.ExtraArgs...)
}

// Synthesize speaks text and returns a WAV blob.
func (p *Piper) Synthesize(ctx context.Context, text string) ([]byte, error) {
	text = strings.TrimSpace(text)
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

// synthesizeOneShot spawns piper, hands it the text and collects the WAV it
// writes to stdout.
func (p *Piper) synthesizeOneShot(ctx context.Context, text string) ([]byte, error) {
	// "--output_file -" makes Piper write a complete WAV to stdout.
	args := append(p.voiceArgs(), "--output_file", "-")

	cmd := exec.CommandContext(ctx, p.opts.Binary, args...)
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
