package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// failingTTS stands in for a Piper that is missing or wedged.
type failingTTS struct{ err error }

func (f failingTTS) Synthesize(_ context.Context, _ string) ([]byte, error) { return nil, f.err }

// TestUIEndpointsAnswerNotFoundForMissingRows pins the contract the web UI
// relies on: acting on something that has been deleted is a 404, not a 500 and
// not a silent success.
func TestUIEndpointsAnswerNotFoundForMissingRows(t *testing.T) {
	_, handler := newServer(t, "hello")

	cases := []struct {
		method, path string
		body         string
	}{
		{http.MethodGet, "/api/ui/speakers/404", ""},
		{http.MethodDelete, "/api/ui/speakers/404", ""},
		{http.MethodPut, "/api/ui/speakers/404", `{"name":"ghost","role":"member"}`},
		{http.MethodGet, "/api/ui/speakers/404/enrollments", ""},
		{http.MethodDelete, "/api/ui/speakers/404/enrollments/404", ""},
		{http.MethodGet, "/api/ui/intents/404", ""},
		{http.MethodDelete, "/api/ui/intents/404", ""},
		{http.MethodPut, "/api/ui/intents/404", `{"name":"ghost","enabled":true,"match_type":"contains","patterns":["x"],"min_role":"member","handler":"reply","handler_config":{"template":"hi"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %s)", recorder.Code, recorder.Body)
			}
		})
	}
}

func TestUIEndpointsRejectAnUnparseableID(t *testing.T) {
	_, handler := newServer(t, "hello")

	for _, path := range []string{
		"/api/ui/speakers/abc",
		"/api/ui/intents/abc",
		"/api/ui/speakers/1/enrollments/abc",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, path, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", path, recorder.Code)
		}
	}
}

func TestTestTTSFallsBackToADefaultPhrase(t *testing.T) {
	_, handler := newServer(t, "hello")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/ui/test/tts", strings.NewReader(`{"text":"  "}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if got := recorder.Header().Get("Content-Type"); got != "audio/wav" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestTestTTSReportsAFailingEngine(t *testing.T) {
	server, handler := newServer(t, "hello")
	server.TTS = failingTTS{err: errors.New("piper voice is missing")}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/ui/test/tts", strings.NewReader(`{"text":"hello"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "piper voice is missing") {
		t.Fatalf("body = %s, want the engine's reason", recorder.Body)
	}
}

func TestTestTTSWithoutAnEngine(t *testing.T) {
	server, handler := newServer(t, "hello")
	server.TTS = nil

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/ui/test/tts", strings.NewReader(`{"text":"hello"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

func TestTestTTSRejectsAMalformedBody(t *testing.T) {
	_, handler := newServer(t, "hello")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/ui/test/tts", strings.NewReader("not json"))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}
