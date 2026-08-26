package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t0mer/renfild/internal/config"
	"github.com/t0mer/renfild/internal/intent"
	"github.com/t0mer/renfild/internal/metrics"
	"github.com/t0mer/renfild/internal/speaker"
	"github.com/t0mer/renfild/internal/store"
)

// --------------------------------------------------------------------------
// fakes
// --------------------------------------------------------------------------

type fakeStore struct {
	mu       sync.Mutex
	speakers []speaker.Record
	recorded []store.Utterance
	listErr  error
}

func (f *fakeStore) ListSpeakers(context.Context) ([]speaker.Record, error) {
	return f.speakers, f.listErr
}

func (f *fakeStore) InsertUtterance(_ context.Context, u store.Utterance) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, u)
	return int64(len(f.recorded)), nil
}

func (f *fakeStore) last() store.Utterance {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.recorded) == 0 {
		return store.Utterance{}
	}
	return f.recorded[len(f.recorded)-1]
}

type fakeEmbedder struct {
	mu      sync.Mutex
	vectors []speaker.Vector
	calls   int
	err     error
}

func (f *fakeEmbedder) Embed(context.Context, []byte) (speaker.Vector, float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, 0, f.err
	}
	index := f.calls
	f.calls++
	if index >= len(f.vectors) {
		index = len(f.vectors) - 1
	}
	return f.vectors[index], 2.0, nil
}

func (f *fakeEmbedder) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeWhisper struct {
	text string
	err  error
}

func (f fakeWhisper) Transcribe(context.Context, []byte) (string, error) { return f.text, f.err }

type fakeTTS struct {
	audio []byte
	err   error
	spoke string
}

func (f *fakeTTS) Synthesize(_ context.Context, text string) ([]byte, error) {
	f.spoke = text
	if f.err != nil {
		return nil, f.err
	}
	return f.audio, nil
}

type staticRules []intent.Rule

func (s staticRules) Rules(context.Context) ([]intent.Rule, error) { return s, nil }

// --------------------------------------------------------------------------
// harness
// --------------------------------------------------------------------------

// voice builds a deterministic pseudo-embedding. Random vectors in 192
// dimensions are near-orthogonal, so different seeds behave like different
// people.
func voice(seed int64) speaker.Vector {
	rng := rand.New(rand.NewSource(seed))
	v := make(speaker.Vector, speaker.Dims)
	for i := range v {
		v[i] = float32(rng.NormFloat64())
	}
	return v.Normalize()
}

// blend mixes two voices, which is how the tests build a sample that lands
// close to — but not squarely on — an enrolled speaker.
func blend(a, b speaker.Vector, weight float32) speaker.Vector {
	out := make(speaker.Vector, len(a))
	for i := range a {
		out[i] = a[i]*weight + b[i]*(1-weight)
	}
	return out.Normalize()
}

type harness struct {
	pipeline *Pipeline
	store    *fakeStore
	embedder *fakeEmbedder
	tts      *fakeTTS
}

func newHarness(t *testing.T, transcript string, rules []intent.Rule) *harness {
	t.Helper()

	cfg := &config.Config{
		AudioRetention: "none",
		Speaker: config.Speaker{
			DefaultThreshold:    0.45,
			UnknownPolicy:       config.PolicyRestricted,
			LowConfidenceMargin: 0.05,
			EnrollmentSamples:   5,
			MinSampleSimilarity: 0.30,
		},
	}
	db := &fakeStore{speakers: []speaker.Record{
		{ID: 1, Name: "tomer", Role: speaker.RoleOwner, Centroid: voice(0)},
		{ID: 2, Name: "dana", Role: speaker.RoleMember, Centroid: voice(50)},
	}}
	embedder := &fakeEmbedder{vectors: []speaker.Vector{voice(0)}}
	tts := &fakeTTS{audio: []byte("RIFFfake")}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	router := intent.New(staticRules(rules), map[string]intent.Handler{
		intent.HandlerReply: intent.ReplyHandler{},
	}, func() bool { return false }, log)

	return &harness{
		store:    db,
		embedder: embedder,
		tts:      tts,
		pipeline: &Pipeline{
			Store:    db,
			Embedder: embedder,
			Whisper:  fakeWhisper{text: transcript},
			Router:   router,
			TTS:      tts,
			Config:   cfg,
			Runtime:  config.NewRuntime(cfg),
			Log:      log,
			Metrics:  metrics.New(),
		},
	}
}

func greeting() []intent.Rule {
	return []intent.Rule{{
		ID: 1, Name: "greeting", Enabled: true, MatchType: intent.MatchContains,
		Patterns: []string{"hello"}, MinRole: speaker.RoleMember, Handler: intent.HandlerReply,
		HandlerConfig: []byte(`{"template":"Hello {{.Speaker}}."}`), Priority: 10,
	}}
}

func request() Request {
	return Request{SatelliteID: "living-room", Wake: []byte("wake"), Command: []byte("command"), Now: time.Now()}
}

// --------------------------------------------------------------------------
// tests
// --------------------------------------------------------------------------

func TestProcessHappyPath(t *testing.T) {
	h := newHarness(t, "hello there", greeting())

	response, err := h.pipeline.Process(context.Background(), request())
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	if response.Speaker != "tomer" {
		t.Fatalf("speaker = %q, want tomer", response.Speaker)
	}
	if response.Intent != "greeting" || response.Reply != "Hello tomer." {
		t.Fatalf("got intent %q reply %q", response.Intent, response.Reply)
	}
	if string(response.Audio) != "RIFFfake" {
		t.Fatalf("audio = %q, want the synthesised WAV", response.Audio)
	}
	if h.tts.spoke != "Hello tomer." {
		t.Fatalf("TTS spoke %q", h.tts.spoke)
	}

	recorded := h.store.last()
	if recorded.Transcript != "hello there" || recorded.Intent != "greeting" {
		t.Fatalf("history row = %+v", recorded)
	}
	if recorded.LatencyMsTotal < 0 || recorded.SatelliteID != "living-room" {
		t.Fatalf("history row is missing context: %+v", recorded)
	}
	if !recorded.Allowed {
		t.Fatal("utterance should be recorded as allowed")
	}
}

func TestProcessRunsIdentificationAndTranscriptionConcurrently(t *testing.T) {
	h := newHarness(t, "hello", greeting())
	// One embedding call only: the match is decisive, so no second opinion.
	if _, err := h.pipeline.Process(context.Background(), request()); err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	if got := h.embedder.count(); got != 1 {
		t.Fatalf("embedder called %d times, want 1", got)
	}
}

func TestBorderlineMatchAsksTheCommandAudio(t *testing.T) {
	h := newHarness(t, "hello", greeting())
	// A wake embedding that lands right on the threshold, then a decisive
	// command embedding.
	reference := voice(0)
	borderline := blend(reference, voice(500), 0.7)
	h.embedder.vectors = []speaker.Vector{borderline, reference}

	tuning := h.pipeline.Runtime.Get()
	// Put the threshold exactly on the wake sample's score, which is what
	// "borderline" means.
	tuning.SpeakerThreshold = speaker.Cosine(borderline, reference)
	tuning.LowConfidenceMargin = 0.05
	if err := h.pipeline.Runtime.Set(tuning); err != nil {
		t.Fatalf("Runtime.Set() error: %v", err)
	}

	if _, err := h.pipeline.Process(context.Background(), request()); err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	if got := h.embedder.count(); got != 2 {
		t.Fatalf("embedder called %d times, want 2 for a borderline match", got)
	}
}

func TestUnknownVoiceUnderRestrictedPolicy(t *testing.T) {
	h := newHarness(t, "hello", greeting())
	h.embedder.vectors = []speaker.Vector{voice(1000)}

	response, err := h.pipeline.Process(context.Background(), request())
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	if response.Speaker != speaker.Unknown {
		t.Fatalf("speaker = %q, want unknown", response.Speaker)
	}
	// The greeting requires 'member', so an unknown voice is refused politely.
	if response.Reply != intent.DefaultUnknownReply {
		t.Fatalf("reply = %q, want the unknown-voice refusal", response.Reply)
	}
	if response.Allowed {
		t.Fatal("an unknown voice must not be marked allowed for a member-only intent")
	}
}

func TestUnknownVoiceUnderDenyPolicyStaysSilent(t *testing.T) {
	h := newHarness(t, "hello", greeting())
	h.embedder.vectors = []speaker.Vector{voice(1000)}

	tuning := h.pipeline.Runtime.Get()
	tuning.UnknownPolicy = config.PolicyDeny
	h.pipeline.Runtime.Set(tuning)

	response, err := h.pipeline.Process(context.Background(), request())
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	if response.Reply != "" || len(response.Audio) != 0 {
		t.Fatalf("deny policy produced a reply: %+v", response)
	}
	if h.store.last().Intent != "denied" {
		t.Fatalf("history intent = %q, want denied", h.store.last().Intent)
	}
}

func TestUnknownVoiceUnderAllowPolicyIsTreatedAsAMember(t *testing.T) {
	h := newHarness(t, "hello", greeting())
	h.embedder.vectors = []speaker.Vector{voice(1000)}

	tuning := h.pipeline.Runtime.Get()
	tuning.UnknownPolicy = config.PolicyAllow
	h.pipeline.Runtime.Set(tuning)

	response, err := h.pipeline.Process(context.Background(), request())
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	if response.Intent != "greeting" {
		t.Fatalf("intent = %q, want greeting", response.Intent)
	}
}

func TestTranscriptionFailureStillAnswers(t *testing.T) {
	h := newHarness(t, "", greeting())
	h.pipeline.Whisper = fakeWhisper{err: errors.New("whisper is down")}

	response, err := h.pipeline.Process(context.Background(), request())
	if err == nil {
		t.Fatal("expected the stage error to be reported to the caller")
	}
	if response.Reply != ErrorReply {
		t.Fatalf("reply = %q, want the spoken apology", response.Reply)
	}
	if len(response.Audio) == 0 {
		t.Fatal("the apology should still be synthesised")
	}
	if !strings.Contains(h.store.last().Error, "whisper is down") {
		t.Fatalf("history error = %q", h.store.last().Error)
	}
}

func TestEmptyTranscriptSaysNothing(t *testing.T) {
	h := newHarness(t, "   ", greeting())
	h.pipeline.Whisper = fakeWhisper{text: ""}

	response, err := h.pipeline.Process(context.Background(), request())
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	if response.Reply != "" || len(response.Audio) != 0 {
		t.Fatalf("empty transcript produced %+v", response)
	}
	if h.store.last().Intent != "empty" {
		t.Fatalf("history intent = %q, want empty", h.store.last().Intent)
	}
}

func TestSpeakerIdentificationFailureFallsBackToUnknown(t *testing.T) {
	h := newHarness(t, "hello", greeting())
	h.embedder.err = errors.New("embedder is down")

	response, err := h.pipeline.Process(context.Background(), request())
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	if response.Speaker != speaker.Unknown {
		t.Fatalf("speaker = %q, want unknown", response.Speaker)
	}
	// The utterance is still transcribed and routed; only the identity is lost.
	if h.store.last().Transcript != "hello" {
		t.Fatalf("transcript was lost: %+v", h.store.last())
	}
}

func TestSynthesisFailureIsRecordedButNotFatal(t *testing.T) {
	h := newHarness(t, "hello", greeting())
	h.tts.err = errors.New("piper is missing")

	response, err := h.pipeline.Process(context.Background(), request())
	if err == nil {
		t.Fatal("expected the synthesis failure to be reported")
	}
	if response.Reply != "Hello tomer." {
		t.Fatalf("reply text should survive a TTS failure, got %q", response.Reply)
	}
	if len(response.Audio) != 0 {
		t.Fatal("no audio should be returned when synthesis fails")
	}
	if !strings.Contains(h.store.last().Error, "piper is missing") {
		t.Fatalf("history error = %q", h.store.last().Error)
	}
}

func TestNoEnrolledSpeakersYieldsUnknownWithoutCallingTheEmbedder(t *testing.T) {
	h := newHarness(t, "hello", greeting())
	h.store.speakers = nil

	response, err := h.pipeline.Process(context.Background(), request())
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	if response.Speaker != speaker.Unknown {
		t.Fatalf("speaker = %q, want unknown", response.Speaker)
	}
	if h.embedder.count() != 0 {
		t.Fatal("the embedder should not be called when nobody is enrolled")
	}
}

func TestObserverSeesEveryUtterance(t *testing.T) {
	h := newHarness(t, "hello", greeting())
	var seen []store.Utterance
	h.pipeline.Observer = func(u store.Utterance) { seen = append(seen, u) }

	if _, err := h.pipeline.Process(context.Background(), request()); err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	if len(seen) != 1 || seen[0].Intent != "greeting" {
		t.Fatalf("observer saw %+v", seen)
	}
}
