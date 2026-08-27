package clients

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakePiperDaemon writes a script that speaks piper's --json-input protocol:
// one JSON line in, a WAV on disk and its path echoed back. It records every
// start in a "starts" file next to the voice so a test can tell a reused
// process from a fresh one.
func fakePiperDaemon(t *testing.T, body string) (binary, voice, starts string) {
	t.Helper()
	dir := t.TempDir()
	binary = filepath.Join(dir, "piper")
	if err := os.WriteFile(binary, []byte(body), 0o700); err != nil {
		t.Fatalf("writing fake piper: %v", err)
	}
	voice = filepath.Join(dir, "voice.onnx")
	if err := os.WriteFile(voice, []byte("model"), 0o600); err != nil {
		t.Fatalf("writing fake voice: %v", err)
	}
	return binary, voice, filepath.Join(dir, "starts")
}

// wellBehavedDaemon answers every line for as long as stdin stays open.
const wellBehavedDaemon = `#!/bin/sh
echo start >> "$(dirname "$2")/starts"
while IFS= read -r line; do
  out=$(printf '%s' "$line" | sed 's/.*"output_file":"\([^"]*\)".*/\1/')
  printf 'RIFFWAVE' > "$out"
  echo "$out"
done
`

func startCount(t *testing.T, path string) int {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return len(strings.Fields(string(body)))
}

func TestPiperPersistentReusesOneProcess(t *testing.T) {
	binary, voice, starts := fakePiperDaemon(t, wellBehavedDaemon)

	piper := NewPiper(PiperOptions{Binary: binary, Voice: voice, Timeout: 5 * time.Second, Persistent: true})
	defer piper.Close()

	for i := range 3 {
		audio, err := piper.Synthesize(context.Background(), "hello")
		if err != nil {
			t.Fatalf("Synthesize() %d error: %v", i, err)
		}
		if string(audio) != "RIFFWAVE" {
			t.Fatalf("audio %d = %q", i, audio)
		}
	}

	if got := startCount(t, starts); got != 1 {
		t.Fatalf("piper started %d times, want 1", got)
	}
}

func TestPiperPersistentCleansUpItsOutput(t *testing.T) {
	binary, voice, _ := fakePiperDaemon(t, wellBehavedDaemon)

	piper := NewPiper(PiperOptions{Binary: binary, Voice: voice, Timeout: 5 * time.Second, Persistent: true})
	if _, err := piper.Synthesize(context.Background(), "hello"); err != nil {
		t.Fatalf("Synthesize() error: %v", err)
	}

	dir := piper.proc.dir
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading work directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("work directory still holds %d files, want the reply removed", len(entries))
	}

	piper.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("work directory survived Close(): %v", err)
	}
}

func TestPiperPersistentRestartsAfterTheProcessDies(t *testing.T) {
	// Answers one line, then exits — the shape of an OOM kill between replies.
	binary, voice, starts := fakePiperDaemon(t, `#!/bin/sh
echo start >> "$(dirname "$2")/starts"
IFS= read -r line
out=$(printf '%s' "$line" | sed 's/.*"output_file":"\([^"]*\)".*/\1/')
printf 'RIFFWAVE' > "$out"
echo "$out"
`)

	piper := NewPiper(PiperOptions{Binary: binary, Voice: voice, Timeout: 5 * time.Second, Persistent: true})
	defer piper.Close()

	for i := range 2 {
		audio, err := piper.Synthesize(context.Background(), "hello")
		if err != nil {
			t.Fatalf("Synthesize() %d error: %v", i, err)
		}
		if string(audio) != "RIFFWAVE" {
			t.Fatalf("audio %d = %q", i, audio)
		}
		// Give the exit a moment to land before the next request looks.
		time.Sleep(50 * time.Millisecond)
	}

	if got := startCount(t, starts); got != 2 {
		t.Fatalf("piper started %d times, want 2", got)
	}
}

func TestPiperPersistentTimesOutAndRecovers(t *testing.T) {
	// Swallows the first request without answering, then behaves.
	binary, voice, starts := fakePiperDaemon(t, `#!/bin/sh
echo start >> "$(dirname "$2")/starts"
if [ -f "$(dirname "$2")/seen" ]; then
  while IFS= read -r line; do
    out=$(printf '%s' "$line" | sed 's/.*"output_file":"\([^"]*\)".*/\1/')
    printf 'RIFFWAVE' > "$out"
    echo "$out"
  done
else
  touch "$(dirname "$2")/seen"
  while IFS= read -r line; do :; done
fi
`)

	piper := NewPiper(PiperOptions{Binary: binary, Voice: voice, Timeout: 300 * time.Millisecond, Persistent: true})
	defer piper.Close()

	_, err := piper.Synthesize(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want a timeout", err)
	}
	if piper.proc != nil {
		t.Fatal("a timed-out process must not be kept for the next reply")
	}

	audio, err := piper.Synthesize(context.Background(), "hello again")
	if err != nil {
		t.Fatalf("Synthesize() after a timeout: %v", err)
	}
	if string(audio) != "RIFFWAVE" {
		t.Fatalf("audio = %q", audio)
	}
	if got := startCount(t, starts); got != 2 {
		t.Fatalf("piper started %d times, want 2", got)
	}
}

func TestPiperPersistentReportsStderrWhenItCannotStart(t *testing.T) {
	binary, voice, _ := fakePiperDaemon(t, "#!/bin/sh\necho 'voice not found' >&2\nexit 1\n")

	piper := NewPiper(PiperOptions{Binary: binary, Voice: voice, Timeout: time.Second, Persistent: true})
	defer piper.Close()

	_, err := piper.Synthesize(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "voice not found") {
		t.Fatalf("error = %v, want piper's stderr", err)
	}
}

func TestPiperCloseIsSafeWithoutADaemon(t *testing.T) {
	binary, voice, _ := fakePiperDaemon(t, wellBehavedDaemon)

	piper := NewPiper(PiperOptions{Binary: binary, Voice: voice, Timeout: time.Second})
	if err := piper.Close(); err != nil {
		t.Fatalf("Close() on a one-shot client: %v", err)
	}
	if err := piper.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}
}

func TestTailBufferKeepsOnlyTheTail(t *testing.T) {
	tail := &tailBuffer{limit: 8}
	tail.Write([]byte("0123456789"))
	tail.Write([]byte("abc"))
	if got := tail.snippet(); got != "56789abc" {
		t.Fatalf("snippet = %q", got)
	}
}

// pythonDaemon imitates piper1-gpl: plain text lines in, a file it names
// itself, and a log line on stderr saying where it went.
const pythonDaemon = `#!/bin/sh
echo start >> "$(dirname "$2")/starts"
n=0
dir=.
while [ $# -gt 0 ]; do
  if [ "$1" = "-d" ]; then dir="$2"; fi
  shift
done
while IFS= read -r line; do
  n=$((n + 1))
  printf 'RIFFWAVE' > "$dir/out-$n.wav"
  echo "INFO:__main__:Wrote $dir/out-$n.wav" >&2
done
`

func TestPiperPythonEnginePersistent(t *testing.T) {
	binary, voice, starts := fakePiperDaemon(t, pythonDaemon)

	piper := NewPiper(PiperOptions{
		Engine: EnginePiperPython, Binary: binary, Voice: voice,
		Timeout: 5 * time.Second, Persistent: true,
	})
	defer piper.Close()

	for i := range 3 {
		audio, err := piper.Synthesize(context.Background(), "שלום")
		if err != nil {
			t.Fatalf("Synthesize() %d error: %v", i, err)
		}
		if string(audio) != "RIFFWAVE" {
			t.Fatalf("audio %d = %q", i, audio)
		}
	}
	if got := startCount(t, starts); got != 1 {
		t.Fatalf("piper started %d times, want 1", got)
	}

	// The engine names its own files, so the client has to clean up what the
	// acknowledgement pointed at.
	entries, err := os.ReadDir(piper.proc.dir)
	if err != nil {
		t.Fatalf("reading work directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("work directory still holds %d files", len(entries))
	}
}

func TestPiperPythonEngineOneShotArguments(t *testing.T) {
	binary, voice, _ := fakePiperDaemon(t, "#!/bin/sh\ncat > /dev/null\necho \"$@\"\n")

	piper := NewPiper(PiperOptions{
		Engine: EnginePiperPython, Binary: binary, Voice: voice, SpeakerID: 2,
		Timeout: 5 * time.Second, ExtraArgs: []string{"--length-scale", "1.1"},
	})
	out, err := piper.Synthesize(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Synthesize() error: %v", err)
	}
	for _, want := range []string{"-m " + voice, "-s 2", "--length-scale 1.1", "-f -"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("arguments %q are missing %q", out, want)
		}
	}
}

func TestPiperRejectsAnUnknownEngine(t *testing.T) {
	binary, voice, _ := fakePiperDaemon(t, wellBehavedDaemon)

	piper := NewPiper(PiperOptions{Engine: "espeak", Binary: binary, Voice: voice})
	if err := piper.Available(); err == nil || !strings.Contains(err.Error(), "espeak") {
		t.Fatalf("Available() = %v, want a complaint about the engine", err)
	}
	if _, err := piper.Synthesize(context.Background(), "hello"); err == nil {
		t.Fatal("expected Synthesize() to refuse an unknown engine")
	}
}

func TestPiperFlattensMultiLineReplies(t *testing.T) {
	binary, voice, _ := fakePiperDaemon(t, wellBehavedDaemon)

	piper := NewPiper(PiperOptions{Binary: binary, Voice: voice, Timeout: 5 * time.Second, Persistent: true})
	defer piper.Close()

	// Two lines would otherwise become two utterances and two acknowledgements.
	audio, err := piper.Synthesize(context.Background(), "first line\n\nsecond line")
	if err != nil {
		t.Fatalf("Synthesize() error: %v", err)
	}
	if string(audio) != "RIFFWAVE" {
		t.Fatalf("audio = %q", audio)
	}
}

func TestPythonEngineParsesOnlyItsAckLines(t *testing.T) {
	engine := pythonEngine{}
	if _, ok := engine.parseAck("INFO:__main__:Loading voice"); ok {
		t.Fatal("a log line was taken for an acknowledgement")
	}
	path, ok := engine.parseAck("INFO:__main__:Wrote /tmp/x/out-1.wav")
	if !ok || path != "/tmp/x/out-1.wav" {
		t.Fatalf("parseAck() = %q, %v", path, ok)
	}
}
