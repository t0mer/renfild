package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/t0mer/renfild/internal/config"
	"github.com/t0mer/renfild/internal/intent"
	"github.com/t0mer/renfild/internal/metrics"
	"github.com/t0mer/renfild/internal/pipeline"
	"github.com/t0mer/renfild/internal/speaker"
	"github.com/t0mer/renfild/internal/store"
)

type stubEmbedder struct {
	vector speaker.Vector
	err    error
}

func (s stubEmbedder) Embed(context.Context, []byte) (speaker.Vector, float64, error) {
	return s.vector, 2.0, s.err
}

type stubWhisper struct{ text string }

func (s stubWhisper) Transcribe(context.Context, []byte) (string, error) { return s.text, nil }

type stubTTS struct{ audio []byte }

func (s stubTTS) Synthesize(context.Context, string) ([]byte, error) { return s.audio, nil }

// testVector builds a deterministic pseudo-embedding; distinct seeds are
// near-orthogonal in 192 dimensions, which is what makes them useful as
// stand-ins for different voices.
func testVector(seed int64) speaker.Vector {
	rng := rand.New(rand.NewSource(seed))
	v := make(speaker.Vector, speaker.Dims)
	for i := range v {
		v[i] = float32(rng.NormFloat64())
	}
	return v.Normalize()
}

func newServer(t *testing.T, transcript string) (*Server, http.Handler) {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatalf("OpenMemory() error: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := &config.Config{
		Listen: ":8080", DB: ":memory:", AudioRetention: "none", AudioDir: t.TempDir(),
		Whisper: config.Whisper{API: config.WhisperAPIOpenAI},
		Speaker: config.Speaker{
			DefaultThreshold: 0.45, UnknownPolicy: config.PolicyRestricted,
			LowConfidenceMargin: 0.05, EnrollmentSamples: 5, MinSampleSimilarity: 0.30,
		},
	}
	runtime := config.NewRuntime(cfg)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	embedder := stubEmbedder{vector: testVector(0)}
	tts := stubTTS{audio: []byte("RIFFfake")}

	router := intent.New(db, map[string]intent.Handler{
		intent.HandlerReply: intent.ReplyHandler{},
	}, func() bool { return false }, log)

	events := NewEventHub()
	processor := &pipeline.Pipeline{
		Store: db, Embedder: embedder, Whisper: stubWhisper{text: transcript},
		Router: router, TTS: tts, Config: cfg, Runtime: runtime, Log: log,
		Metrics: metrics.New(), Observer: events.Publish,
	}
	server := &Server{
		Store: db, Pipeline: processor, Config: cfg, Runtime: runtime,
		Metrics: processor.Metrics, Log: log, Events: events, Embedder: embedder, TTS: tts,
	}
	return server, server.Routes()
}

func utteranceRequest(t *testing.T) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, part := range []string{"wake", "command"} {
		file, err := writer.CreateFormFile(part, part+".wav")
		if err != nil {
			t.Fatalf("building multipart body: %v", err)
		}
		file.Write([]byte("RIFFaudio"))
	}
	writer.WriteField("satellite_id", "living-room")
	writer.Close()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/utterance", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func TestHealthz(t *testing.T) {
	_, handler := newServer(t, "hello")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body map[string]string
	json.Unmarshal(recorder.Body.Bytes(), &body)
	if body["status"] != "ok" {
		t.Fatalf("body = %v", body)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	_, handler := newServer(t, "hello")
	// Labelled counters only appear once they have been incremented.
	handler.ServeHTTP(httptest.NewRecorder(), utteranceRequest(t))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "renfild_utterances_total") {
		t.Fatal("metrics output is missing the utterance counter")
	}
}

func TestUtteranceReturnsAudioAndHeaders(t *testing.T) {
	_, handler := newServer(t, "hello there")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, utteranceRequest(t))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "audio/wav" {
		t.Fatalf("content type = %q", recorder.Header().Get("Content-Type"))
	}
	transcript, _ := url.PathUnescape(recorder.Header().Get("X-Transcript"))
	if transcript != "hello there" {
		t.Fatalf("X-Transcript = %q", transcript)
	}
	if recorder.Header().Get("X-Speaker") == "" {
		t.Fatal("X-Speaker header is missing")
	}
	if recorder.Body.Len() == 0 {
		t.Fatal("no audio returned")
	}
}

func TestUtteranceEncodesHebrewHeaders(t *testing.T) {
	_, handler := newServer(t, "מה השעה")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, utteranceRequest(t))

	raw := recorder.Header().Get("X-Transcript")
	// The raw header must stay ASCII, and must decode back to the Hebrew text.
	for _, r := range raw {
		if r > 127 {
			t.Fatalf("header contains a non-ASCII rune: %q", raw)
		}
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil || decoded != "מה השעה" {
		t.Fatalf("decoded %q (%v), want the Hebrew transcript", decoded, err)
	}
}

func TestUtteranceReturns204WhenThereIsNothingToSay(t *testing.T) {
	_, handler := newServer(t, "")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, utteranceRequest(t))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
}

func TestUtteranceRejectsANonMultipartBody(t *testing.T) {
	_, handler := newServer(t, "hello")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/utterance", strings.NewReader("nope"))
	request.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestSpeakerCRUDOverHTTP(t *testing.T) {
	_, handler := newServer(t, "hello")

	created := httptest.NewRecorder()
	handler.ServeHTTP(created, jsonRequest(http.MethodPost, "/api/ui/speakers",
		`{"name":"tomer","role":"owner","threshold":null}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", created.Code, created.Body.String())
	}
	var record speaker.Record
	json.Unmarshal(created.Body.Bytes(), &record)
	if record.ID == 0 || record.Name != "tomer" {
		t.Fatalf("created %+v", record)
	}

	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/ui/speakers", nil))
	var speakers []speaker.Record
	json.Unmarshal(listed.Body.Bytes(), &speakers)
	if len(speakers) != 1 {
		t.Fatalf("listed %d speakers", len(speakers))
	}

	deleted := httptest.NewRecorder()
	handler.ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete, "/api/ui/speakers/1", nil))
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", deleted.Code)
	}

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/ui/speakers/1", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", missing.Code)
	}
}

func TestCreateSpeakerRejectsTheAnyRole(t *testing.T) {
	_, handler := newServer(t, "hello")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, jsonRequest(http.MethodPost, "/api/ui/speakers", `{"name":"ghost","role":"any"}`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestEnrollmentAcceptsAndRejectsSamples(t *testing.T) {
	server, handler := newServer(t, "hello")

	created := httptest.NewRecorder()
	handler.ServeHTTP(created, jsonRequest(http.MethodPost, "/api/ui/speakers", `{"name":"tomer","role":"owner"}`))

	// First sample: always accepted.
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, audioRequest(t, "/api/ui/speakers/1/enrollments", "wake-1"))
	if first.Code != http.StatusCreated {
		t.Fatalf("first enrollment status = %d, body %s", first.Code, first.Body.String())
	}

	// A wildly different embedding is rejected, with a reason.
	server.Embedder = stubEmbedder{vector: testVector(900)}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, audioRequest(t, "/api/ui/speakers/1/enrollments", "wake-2"))

	var result enrollmentResult
	json.Unmarshal(second.Body.Bytes(), &result)
	if result.Accepted {
		t.Fatalf("a mismatched sample was accepted: %+v", result)
	}
	if result.Reason == "" {
		t.Fatal("rejection needs a reason")
	}
}

func TestIdentifyReportsScoresForEverySpeaker(t *testing.T) {
	server, handler := newServer(t, "hello")
	handler.ServeHTTP(httptest.NewRecorder(),
		jsonRequest(http.MethodPost, "/api/ui/speakers", `{"name":"tomer","role":"owner"}`))
	handler.ServeHTTP(httptest.NewRecorder(), audioRequest(t, "/api/ui/speakers/1/enrollments", "wake"))

	server.Embedder = stubEmbedder{vector: testVector(0)}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, audioRequest(t, "/api/ui/speakers/identify", ""))

	var payload struct {
		Identity speaker.Identity   `json:"identity"`
		Scores   map[string]float64 `json:"scores"`
	}
	json.Unmarshal(recorder.Body.Bytes(), &payload)
	if !payload.Identity.Known || payload.Identity.Name != "tomer" {
		t.Fatalf("identity = %+v", payload.Identity)
	}
	if _, ok := payload.Scores["tomer"]; !ok {
		t.Fatalf("scores = %v", payload.Scores)
	}
}

func TestIntentCRUDAndDryRunOverHTTP(t *testing.T) {
	_, handler := newServer(t, "hello")

	created := httptest.NewRecorder()
	handler.ServeHTTP(created, jsonRequest(http.MethodPost, "/api/ui/intents", `{
		"name":"lights","enabled":true,"match_type":"contains","patterns":["lights"],
		"min_role":"member","handler":"reply","handler_config":{"template":"Lights, {{.Speaker}}."},
		"priority":5}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", created.Code, created.Body.String())
	}

	tested := httptest.NewRecorder()
	handler.ServeHTTP(tested, jsonRequest(http.MethodPost, "/api/ui/test/intent",
		`{"transcript":"turn on the lights","speaker":"tomer","role":"owner"}`))
	var result testIntentResponse
	json.Unmarshal(tested.Body.Bytes(), &result)
	if result.Result.Intent != "lights" || result.Result.Reply != "Lights, tomer." {
		t.Fatalf("dry run = %+v", result)
	}
}

func TestCreateIntentRejectsABadRegex(t *testing.T) {
	_, handler := newServer(t, "hello")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, jsonRequest(http.MethodPost, "/api/ui/intents", `{
		"name":"broken","enabled":true,"match_type":"regex","patterns":["([a-z"],
		"min_role":"member","handler":"reply","handler_config":{}}`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestSettingsRoundTripOverHTTP(t *testing.T) {
	_, handler := newServer(t, "hello")

	updated := httptest.NewRecorder()
	handler.ServeHTTP(updated, jsonRequest(http.MethodPut, "/api/ui/settings", `{
		"speaker_threshold":0.6,"unknown_policy":"deny","low_confidence_margin":0.03,
		"llm_fallback":true,"min_sample_similarity":0.35,"enrollment_samples":6}`))
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, body %s", updated.Code, updated.Body.String())
	}

	fetched := httptest.NewRecorder()
	handler.ServeHTTP(fetched, httptest.NewRequest(http.MethodGet, "/api/ui/settings", nil))
	var payload settingsResponse
	json.Unmarshal(fetched.Body.Bytes(), &payload)
	if payload.Runtime.SpeakerThreshold != 0.6 || payload.Runtime.UnknownPolicy != "deny" {
		t.Fatalf("settings = %+v", payload.Runtime)
	}
}

func TestSettingsRejectAnInvalidPolicy(t *testing.T) {
	_, handler := newServer(t, "hello")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, jsonRequest(http.MethodPut, "/api/ui/settings", `{
		"speaker_threshold":0.6,"unknown_policy":"maybe","low_confidence_margin":0.03,
		"llm_fallback":true,"min_sample_similarity":0.35,"enrollment_samples":6}`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestHistoryRecordsUtterances(t *testing.T) {
	_, handler := newServer(t, "hello there")
	handler.ServeHTTP(httptest.NewRecorder(), utteranceRequest(t))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/ui/history?limit=10", nil))

	var page historyResponse
	json.Unmarshal(recorder.Body.Bytes(), &page)
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("history = %+v", page)
	}
	if page.Items[0].Transcript != "hello there" {
		t.Fatalf("transcript = %q", page.Items[0].Transcript)
	}
}

func TestHistoryAudioIsAbsentWhenRetentionIsOff(t *testing.T) {
	_, handler := newServer(t, "hello")
	handler.ServeHTTP(httptest.NewRecorder(), utteranceRequest(t))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/ui/history/1/audio", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when nothing is stored", recorder.Code)
	}
}

func TestSPAFallbackServesTheApp(t *testing.T) {
	_, handler := newServer(t, "hello")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/speakers", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("content type = %q", recorder.Header().Get("Content-Type"))
	}
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

func jsonRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func audioRequest(t *testing.T, path, label string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("audio", "sample.wav")
	if err != nil {
		t.Fatalf("building upload: %v", err)
	}
	file.Write([]byte("RIFFaudio"))
	if label != "" {
		writer.WriteField("label", label)
	}
	writer.Close()

	request := httptest.NewRequest(http.MethodPost, path, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}
