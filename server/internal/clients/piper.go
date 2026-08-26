package clients

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// TTS turns text into a WAV blob. Piper implements it today; an HTTP engine
// would implement it tomorrow without the pipeline noticing.
type TTS interface {
	Synthesize(ctx context.Context, text string) ([]byte, error)
}

// Piper invokes the Piper binary once per reply: text on stdin, WAV on stdout.
// Synthesis on arm64 is quick enough that keeping a daemon alive is not worth
// the complexity.
type Piper struct {
	Binary    string
	Voice     string
	SpeakerID int
	Timeout   time.Duration
	ExtraArgs []string
}

// NewPiper builds a Piper TTS client.
func NewPiper(binary, voice string, speakerID int, timeout time.Duration, extraArgs []string) *Piper {
	return &Piper{
		Binary:    binary,
		Voice:     voice,
		SpeakerID: speakerID,
		Timeout:   timeout,
		ExtraArgs: extraArgs,
	}
}

// Available reports whether the binary and the voice model are both present.
func (p *Piper) Available() error {
	if _, err := os.Stat(p.Binary); err != nil {
		return fmt.Errorf("piper binary %s: %w", p.Binary, err)
	}
	if _, err := os.Stat(p.Voice); err != nil {
		return fmt.Errorf("piper voice %s: %w", p.Voice, err)
	}
	return nil
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

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// "--output_file -" makes Piper write a complete WAV to stdout.
	args := []string{"--model", p.Voice, "--output_file", "-"}
	if p.SpeakerID > 0 {
		args = append(args, "--speaker", strconv.Itoa(p.SpeakerID))
	}
	args = append(args, p.ExtraArgs...)

	cmd := exec.CommandContext(ctx, p.Binary, args...)
	cmd.Stdin = strings.NewReader(text + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("piper timed out after %s", timeout)
		}
		return nil, fmt.Errorf("running piper: %w (%s)", err, snippet(stderr.Bytes()))
	}
	if stdout.Len() == 0 {
		return nil, fmt.Errorf("piper produced no audio (%s)", snippet(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}
