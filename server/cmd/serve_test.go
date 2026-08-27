package cmd

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/t0mer/renfild/internal/store"
)

// retainedUtterance writes the pair of files the pipeline would have written
// into the day directory retention would have chosen, and records the command
// recording against an utterance of the given age.
func retainedUtterance(t *testing.T, db *store.Store, root string, age time.Duration) (command, wake string) {
	t.Helper()
	day := filepath.Join(root, time.Now().Add(-age).UTC().Format("2006-01-02"))
	return retainedUtteranceIn(t, db, day, age)
}

// retainedUtteranceIn is retainedUtterance with the directory chosen by the
// caller, for the case where two recordings of different ages have to share
// one day.
func retainedUtteranceIn(t *testing.T, db *store.Store, day string, age time.Duration) (command, wake string) {
	t.Helper()
	ts := time.Now().Add(-age)

	if err := os.MkdirAll(day, 0o750); err != nil {
		t.Fatalf("creating day directory: %v", err)
	}
	base := filepath.Join(day, ts.UTC().Format("150405.000"))
	command, wake = base+"-command.wav", base+"-wake.wav"
	for _, path := range []string{command, wake} {
		if err := os.WriteFile(path, []byte("RIFF"), 0o640); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}

	if _, err := db.InsertUtterance(context.Background(), store.Utterance{
		TS:         ts,
		Speaker:    "tomer",
		Transcript: "what is the time",
		AudioPath:  command,
	}); err != nil {
		t.Fatalf("InsertUtterance() error: %v", err)
	}
	return command, wake
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "renfild.db"))
	if err != nil {
		t.Fatalf("store.Open() error: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestPrunePassRemovesBothRecordingsAndTheEmptyDay(t *testing.T) {
	db := openStore(t)
	root := t.TempDir()
	command, wake := retainedUtterance(t, db, root, 48*time.Hour)

	if got := prunePass(context.Background(), db, 24*time.Hour, quietLogger()); got != 1 {
		t.Fatalf("prunePass() = %d, want 1", got)
	}

	for _, path := range []string{command, wake} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s survived the prune: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Dir(command)); !os.IsNotExist(err) {
		t.Fatalf("the emptied day directory survived: %v", err)
	}

	// The history entry stays; only its pointer to the audio is cleared.
	utterances, total, err := db.ListUtterances(context.Background(), store.HistoryFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListUtterances() error: %v", err)
	}
	if total != 1 {
		t.Fatalf("history holds %d utterances, want 1", total)
	}
	if utterances[0].AudioPath != "" {
		t.Fatalf("audio_path = %q, want it cleared", utterances[0].AudioPath)
	}
}

func TestPrunePassKeepsRecordingsInsideTheWindow(t *testing.T) {
	db := openStore(t)
	root := t.TempDir()
	command, wake := retainedUtterance(t, db, root, time.Hour)

	if got := prunePass(context.Background(), db, 24*time.Hour, quietLogger()); got != 0 {
		t.Fatalf("prunePass() = %d, want 0", got)
	}
	for _, path := range []string{command, wake} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s was removed early: %v", path, err)
		}
	}
}

func TestPrunePassKeepsADayThatStillHoldsRecordings(t *testing.T) {
	db := openStore(t)
	day := filepath.Join(t.TempDir(), "2026-08-27")

	expired, _ := retainedUtteranceIn(t, db, day, 48*time.Hour)
	fresh, freshWake := retainedUtteranceIn(t, db, day, time.Minute)

	if got := prunePass(context.Background(), db, 24*time.Hour, quietLogger()); got != 1 {
		t.Fatalf("prunePass() = %d, want 1", got)
	}
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("the expired recording survived: %v", err)
	}
	for _, path := range []string{fresh, freshWake, day} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s was removed with the expired recording: %v", path, err)
		}
	}
}

func TestPruneAudioRunsAPassBeforeTheFirstTick(t *testing.T) {
	db := openStore(t)
	root := t.TempDir()
	command, _ := retainedUtterance(t, db, root, 48*time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		pruneAudio(ctx, db, 24*time.Hour, quietLogger())
		close(done)
	}()

	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(command); os.IsNotExist(err) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the startup pass never ran")
		case <-time.After(20 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pruneAudio did not stop with its context")
	}
}
