package intent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/t0mer/renfild/internal/speaker"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type staticRules []Rule

func (s staticRules) Rules(context.Context) ([]Rule, error) { return s, nil }

type fakeLLM struct {
	answer string
	err    error
	system string
	user   string
}

func (f *fakeLLM) Chat(_ context.Context, system, user string) (string, error) {
	f.system, f.user = system, user
	return f.answer, f.err
}

func testRouter(rules []Rule, llm LLM, fallback bool) *Router {
	handlers := map[string]Handler{
		HandlerReply:   ReplyHandler{},
		HandlerWebhook: WebhookHandler{Client: http.DefaultClient},
		HandlerLLM:     LLMHandler{Client: llm, SystemPrompt: "test", MaxWords: 10},
	}
	return New(staticRules(rules), handlers, func() bool { return fallback }, quietLogger())
}

func replyRule(name, pattern, template string, minRole speaker.Role, priority int) Rule {
	return Rule{
		Name:          name,
		Enabled:       true,
		MatchType:     MatchContains,
		Patterns:      []string{pattern},
		MinRole:       minRole,
		Handler:       HandlerReply,
		HandlerConfig: json.RawMessage(`{"template":"` + template + `"}`),
		Priority:      priority,
	}
}

func ownerRequest(transcript string) Request {
	return Request{
		Speaker: "tomer", Role: speaker.RoleOwner, Known: true,
		Transcript: transcript, SatelliteID: "living-room", Now: time.Now(),
	}
}

func TestRouteRunsTheFirstMatchingRuleByPriority(t *testing.T) {
	rules := []Rule{
		replyRule("specific", "turn on the kitchen light", "kitchen", speaker.RoleMember, 10),
		replyRule("general", "turn on", "general", speaker.RoleMember, 50),
	}
	// Deliberately supplied out of order: priority, not slice order, decides.
	result, err := testRouter([]Rule{rules[1], rules[0]}, nil, false).
		Route(context.Background(), ownerRequest("turn on the kitchen light"))
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}
	if result.Intent != "specific" || result.Reply != "kitchen" {
		t.Fatalf("got intent %q reply %q, want specific/kitchen", result.Intent, result.Reply)
	}
}

func TestRouteBreaksPriorityTiesByID(t *testing.T) {
	first := replyRule("first", "hello", "one", speaker.RoleMember, 10)
	first.ID = 1
	second := replyRule("second", "hello", "two", speaker.RoleMember, 10)
	second.ID = 2

	result, _ := testRouter([]Rule{second, first}, nil, false).
		Route(context.Background(), ownerRequest("hello"))
	if result.Intent != "first" {
		t.Fatalf("intent = %q, want first", result.Intent)
	}
}

func TestRouteRendersTemplateData(t *testing.T) {
	rule := replyRule("greeting", "hello", "Hello {{.Speaker}}, you said {{.Transcript}}", speaker.RoleAny, 10)
	result, err := testRouter([]Rule{rule}, nil, false).
		Route(context.Background(), ownerRequest("hello there"))
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}
	if result.Reply != "Hello tomer, you said hello there" {
		t.Fatalf("reply = %q", result.Reply)
	}
}

func TestRouteEnforcesRoleFloor(t *testing.T) {
	rules := []Rule{replyRule("unlock", "unlock the door", "unlocked", speaker.RoleOwner, 10)}
	router := testRouter(rules, nil, false)

	tests := []struct {
		name        string
		request     Request
		wantAllowed bool
		wantReply   string
	}{
		{
			name:        "owner is allowed",
			request:     ownerRequest("unlock the door"),
			wantAllowed: true,
			wantReply:   "unlocked",
		},
		{
			name: "member is refused by name",
			request: Request{
				Speaker: "dana", Role: speaker.RoleMember, Known: true,
				Transcript: "unlock the door", Now: time.Now(),
			},
			wantAllowed: false,
			wantReply:   "Sorry dana, you are not allowed to do that.",
		},
		{
			name: "unknown voice gets the voice refusal",
			request: Request{
				Speaker: speaker.Unknown, Role: speaker.UnknownRole,
				Transcript: "unlock the door", Now: time.Now(),
			},
			wantAllowed: false,
			wantReply:   DefaultUnknownReply,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := router.Route(context.Background(), tc.request)
			if err != nil {
				t.Fatalf("Route() error: %v", err)
			}
			if result.Allowed != tc.wantAllowed {
				t.Fatalf("allowed = %v, want %v", result.Allowed, tc.wantAllowed)
			}
			if result.Reply != tc.wantReply {
				t.Fatalf("reply = %q, want %q", result.Reply, tc.wantReply)
			}
		})
	}
}

func TestUnknownVoiceMayStillTriggerAnyRoleIntents(t *testing.T) {
	rules := []Rule{replyRule("time", "what time is it", "It is late.", speaker.RoleAny, 10)}
	result, err := testRouter(rules, nil, false).Route(context.Background(), Request{
		Speaker: speaker.Unknown, Role: speaker.UnknownRole,
		Transcript: "what time is it", Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}
	if !result.Allowed || result.Reply != "It is late." {
		t.Fatalf("got %+v, want the intent to run", result)
	}
}

func TestRouteHebrewRule(t *testing.T) {
	rules := []Rule{replyRule("time-he", "מה השעה", "אני לא יודע", speaker.RoleAny, 10)}
	result, err := testRouter(rules, nil, false).Route(context.Background(), Request{
		Speaker: "tomer", Role: speaker.RoleOwner, Known: true,
		Transcript: "מה השעה עכשיו?", Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}
	if result.Intent != "time-he" || result.Reply != "אני לא יודע" {
		t.Fatalf("got %+v, want the Hebrew rule to fire", result)
	}
}

func TestNoMatchFallsBackToTheLLM(t *testing.T) {
	llm := &fakeLLM{answer: "The sky is blue because of scattering."}
	result, err := testRouter(nil, llm, true).Route(context.Background(), ownerRequest("why is the sky blue"))
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}
	if result.Handler != HandlerLLM || result.Matched {
		t.Fatalf("got %+v, want an unmatched LLM answer", result)
	}
	if !strings.Contains(llm.system, "tomer") {
		t.Fatalf("system prompt lacks speaker context: %q", llm.system)
	}
	if llm.user != "why is the sky blue" {
		t.Fatalf("user prompt = %q", llm.user)
	}
}

func TestLLMFallbackIsCappedInLength(t *testing.T) {
	llm := &fakeLLM{answer: strings.Repeat("word ", 40)}
	result, _ := testRouter(nil, llm, true).Route(context.Background(), ownerRequest("tell me everything"))
	if got := len(strings.Fields(result.Reply)); got > 10 {
		t.Fatalf("reply has %d words, want at most 10", got)
	}
}

func TestLLMFallbackIsRefusedForUnknownVoices(t *testing.T) {
	llm := &fakeLLM{answer: "should never be spoken"}
	result, err := testRouter(nil, llm, true).Route(context.Background(), Request{
		Speaker: speaker.Unknown, Role: speaker.UnknownRole,
		Transcript: "what is my bank balance", Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}
	if result.Allowed || result.Reply != DefaultUnknownReply {
		t.Fatalf("got %+v, want the unknown-voice refusal", result)
	}
	if llm.user != "" {
		t.Fatal("the model was called for an unidentified voice")
	}
}

func TestNoMatchWithoutFallback(t *testing.T) {
	result, err := testRouter(nil, nil, false).Route(context.Background(), ownerRequest("anything at all"))
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}
	if result.Reply != DefaultNoMatchReply {
		t.Fatalf("reply = %q, want %q", result.Reply, DefaultNoMatchReply)
	}
}

func TestHandlerErrorIsReportedWithoutLosingTheIntentName(t *testing.T) {
	llm := &fakeLLM{err: errors.New("ollama is down")}
	rule := Rule{
		Name: "ask", Enabled: true, MatchType: MatchContains, Patterns: []string{"ask"},
		MinRole: speaker.RoleMember, Handler: HandlerLLM, Priority: 10,
	}
	result, err := testRouter([]Rule{rule}, llm, false).Route(context.Background(), ownerRequest("ask something"))
	if err == nil {
		t.Fatal("expected the handler error to surface")
	}
	if result.Intent != "ask" {
		t.Fatalf("intent = %q, want ask", result.Intent)
	}
}

func TestWebhookHandlerCallsTheEndpointAndCanSpeakItsResponse(t *testing.T) {
	var gotBody, gotMethod, gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody, gotMethod, gotHeader = string(body), r.Method, r.Header.Get("X-Token")
		w.Write([]byte("the garage is closed"))
	}))
	defer server.Close()

	rule := Rule{
		Name: "garage", Enabled: true, MatchType: MatchContains, Patterns: []string{"garage"},
		MinRole: speaker.RoleOwner, Handler: HandlerWebhook, Priority: 10,
		HandlerConfig: json.RawMessage(`{
			"url": "` + server.URL + `",
			"method": "POST",
			"headers": {"X-Token": "secret"},
			"body": "{\"who\":\"{{.Speaker}}\"}",
			"speak_response": true
		}`),
	}

	result, err := testRouter([]Rule{rule}, nil, false).Route(context.Background(), ownerRequest("open the garage"))
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}
	if gotMethod != "POST" || gotHeader != "secret" {
		t.Fatalf("request was %s with token %q", gotMethod, gotHeader)
	}
	if gotBody != `{"who":"tomer"}` {
		t.Fatalf("body = %q", gotBody)
	}
	if result.Reply != "the garage is closed" {
		t.Fatalf("reply = %q", result.Reply)
	}
}

func TestWebhookHandlerReportsAFailingEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	rule := Rule{
		Name: "broken", Enabled: true, MatchType: MatchContains, Patterns: []string{"broken"},
		MinRole: speaker.RoleMember, Handler: HandlerWebhook, Priority: 10,
		HandlerConfig: json.RawMessage(`{"url":"` + server.URL + `"}`),
	}
	if _, err := testRouter([]Rule{rule}, nil, false).Route(context.Background(), ownerRequest("broken")); err == nil {
		t.Fatal("expected an error from a 500 response")
	}
}

func TestRuleSourceFailureIsAnError(t *testing.T) {
	router := New(RuleSourceFunc(func(context.Context) ([]Rule, error) {
		return nil, errors.New("database is gone")
	}), nil, func() bool { return false }, quietLogger())

	if _, err := router.Route(context.Background(), ownerRequest("hello")); err == nil {
		t.Fatal("expected the rule-source error to surface")
	}
}
