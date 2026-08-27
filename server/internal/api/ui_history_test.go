package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/t0mer/renfild/internal/store"
)

// storedUtterance records one utterance whose audio_path is whatever the caller
// says, so the tests can put a hostile path in the database on purpose.
func storedUtterance(t *testing.T, server *Server, audioPath string) int64 {
	t.Helper()
	id, err := server.Store.InsertUtterance(context.Background(), store.Utterance{
		TS:         time.Now(),
		Speaker:    "tomer",
		Transcript: "what time is it",
		AudioPath:  audioPath,
	})
	if err != nil {
		t.Fatalf("InsertUtterance() error: %v", err)
	}
	return id
}

func TestHistoryAudioServesARetainedRecording(t *testing.T) {
	server, handler := newServer(t, "hello")

	path := filepath.Join(server.Config.AudioDir, "2026-08-27", "120000.000-command.wav")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("creating audio directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("RIFFfake"), 0o640); err != nil {
		t.Fatalf("writing audio: %v", err)
	}
	id := storedUtterance(t, server, path)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, audioURL(id), nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if got := recorder.Header().Get("Content-Type"); got != "audio/wav" {
		t.Fatalf("Content-Type = %q", got)
	}
	if recorder.Body.String() != "RIFFfake" {
		t.Fatalf("body = %q", recorder.Body)
	}
}

func TestHistoryAudioRefusesAPathOutsideTheAudioDirectory(t *testing.T) {
	server, handler := newServer(t, "hello")

	// Somewhere real, so a pass would actually leak the file rather than 404.
	outside := filepath.Join(t.TempDir(), "secrets.wav")
	if err := os.WriteFile(outside, []byte("RIFFsecret"), 0o640); err != nil {
		t.Fatalf("writing the decoy: %v", err)
	}

	for _, stored := range []string{
		outside,
		filepath.Join(server.Config.AudioDir, "..", filepath.Base(outside)),
		filepath.Join(server.Config.AudioDir, "..", "..", "etc", "shadow"),
		// The directory itself is not a file inside the directory.
		server.Config.AudioDir,
	} {
		id := storedUtterance(t, server, stored)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, audioURL(id), nil))

		if recorder.Code == http.StatusOK {
			t.Fatalf("%q was served with status 200", stored)
		}
		if strings.Contains(recorder.Body.String(), "RIFFsecret") {
			t.Fatalf("%q leaked the file it pointed at", stored)
		}
	}
}

func TestHistoryAudioWhenNothingWasRetained(t *testing.T) {
	server, handler := newServer(t, "hello")
	id := storedUtterance(t, server, "")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, audioURL(id), nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestHistoryAudioWhenTheFileHasBeenPruned(t *testing.T) {
	server, handler := newServer(t, "hello")
	// A path inside the directory that the pruner has already removed.
	id := storedUtterance(t, server, filepath.Join(server.Config.AudioDir, "gone-command.wav"))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, audioURL(id), nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestHistoryAudioRejectsABadID(t *testing.T) {
	_, handler := newServer(t, "hello")

	for _, path := range []string{"/api/ui/history/nope/audio", "/api/ui/history/999/audio"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code == http.StatusOK {
			t.Fatalf("%s returned 200", path)
		}
	}
}

func audioURL(id int64) string {
	return "/api/ui/history/" + strconv.FormatInt(id, 10) + "/audio"
}
