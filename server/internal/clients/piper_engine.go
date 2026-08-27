package clients

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Piper engines. The C++ release binary from rhasspy/piper is frozen at
// 2023.11.14 and cannot load voices whose phoneme map postdates it — a Hebrew
// voice makes it abort with "Phonemes must be one codepoint". piper1-gpl is the
// maintained successor: same models, a Python entry point instead of a static
// binary, and it speaks Hebrew.
const (
	EnginePiperCPP    = "cpp"
	EnginePiperPython = "python"
)

// piperEngine adapts the two command-line dialects to one protocol: a process
// that takes an utterance per line and announces each WAV as it lands.
type piperEngine interface {
	// oneShotArgs asks for a single WAV on stdout.
	oneShotArgs(o PiperOptions) []string
	// daemonArgs starts line-at-a-time mode, writing WAVs into dir.
	daemonArgs(o PiperOptions, dir string) []string
	// request renders one utterance. An empty path means the engine names the
	// file itself and the acknowledgement will say where it went.
	request(dir string, seq uint64, text string) (line, path string, err error)
	// parseAck reports the finished WAV named by a line, if it names one.
	parseAck(line string) (path string, ok bool)
	// acksOnStderr says which stream the acknowledgements arrive on.
	acksOnStderr() bool
}

// engineFor resolves the configured engine name, defaulting to the C++ build.
func engineFor(name string) (piperEngine, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", EnginePiperCPP:
		return cppEngine{}, nil
	case EnginePiperPython:
		return pythonEngine{}, nil
	default:
		return nil, fmt.Errorf("piper.engine: unknown value %q (want %q or %q)",
			name, EnginePiperCPP, EnginePiperPython)
	}
}

// cppEngine drives the rhasspy/piper release binary.
type cppEngine struct{}

func (cppEngine) base(o PiperOptions) []string {
	args := []string{"--model", o.Voice}
	if o.SpeakerID > 0 {
		args = append(args, "--speaker", strconv.Itoa(o.SpeakerID))
	}
	return append(args, o.ExtraArgs...)
}

func (e cppEngine) oneShotArgs(o PiperOptions) []string {
	// "--output_file -" makes Piper write a complete WAV to stdout.
	return append(e.base(o), "--output_file", "-")
}

func (e cppEngine) daemonArgs(o PiperOptions, dir string) []string {
	return append(e.base(o), "--json-input", "--output_dir", dir)
}

// piperRequest is one line of the C++ build's --json-input protocol.
type piperRequest struct {
	Text       string `json:"text"`
	OutputFile string `json:"output_file"`
}

func (cppEngine) request(dir string, seq uint64, text string) (string, string, error) {
	path := filepath.Join(dir, fmt.Sprintf("reply-%d.wav", seq))
	line, err := json.Marshal(piperRequest{Text: text, OutputFile: path})
	if err != nil {
		return "", "", fmt.Errorf("encoding piper request: %w", err)
	}
	return string(line), path, nil
}

// parseAck: this build echoes the output path, and nothing else, on stdout.
func (cppEngine) parseAck(line string) (string, bool) {
	path := strings.TrimSpace(line)
	return path, path != ""
}

func (cppEngine) acksOnStderr() bool { return false }

// pythonEngine drives OHF-Voice/piper1-gpl.
type pythonEngine struct{}

func (pythonEngine) base(o PiperOptions) []string {
	args := []string{"-m", o.Voice}
	if o.SpeakerID > 0 {
		args = append(args, "-s", strconv.Itoa(o.SpeakerID))
	}
	return append(args, o.ExtraArgs...)
}

func (e pythonEngine) oneShotArgs(o PiperOptions) []string {
	return append(e.base(o), "-f", "-")
}

func (e pythonEngine) daemonArgs(o PiperOptions, dir string) []string {
	// This build has no line protocol; it takes plain text lines and names the
	// files itself, then says which it wrote.
	return append(e.base(o), "-d", dir)
}

func (pythonEngine) request(_ string, _ uint64, text string) (string, string, error) {
	return text, "", nil
}

// pythonAckPrefix is how piper1-gpl announces a finished file on stderr.
const pythonAckPrefix = "Wrote "

func (pythonEngine) parseAck(line string) (string, bool) {
	index := strings.Index(line, pythonAckPrefix)
	if index < 0 {
		return "", false
	}
	path := strings.TrimSpace(line[index+len(pythonAckPrefix):])
	return path, path != ""
}

func (pythonEngine) acksOnStderr() bool { return true }
