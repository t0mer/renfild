package intent

import (
	"encoding/json"
	"testing"

	"github.com/t0mer/renfild/internal/speaker"
)

func rule(matchType string, patterns ...string) Rule {
	return Rule{
		Name:      "test",
		Enabled:   true,
		MatchType: matchType,
		Patterns:  patterns,
		MinRole:   speaker.RoleMember,
		Handler:   HandlerReply,
	}
}

func TestRuleMatches(t *testing.T) {
	tests := []struct {
		name       string
		rule       Rule
		transcript string
		want       bool
	}{
		{"exact hit", rule(MatchExact, "good morning"), "good morning", true},
		{"exact needs the whole phrase", rule(MatchExact, "good morning"), "good morning renfild", false},
		{"exact normalizes its patterns", rule(MatchExact, "Good Morning!"), "good morning", true},
		{"contains hit", rule(MatchContains, "lights"), "turn on the lights please", true},
		{"contains miss", rule(MatchContains, "lights"), "what is the time", false},
		{"contains any of several", rule(MatchContains, "radio", "music"), "put some music on", true},
		{"regex hit", rule(MatchRegex, `^turn (on|off) the .*`), "turn off the kitchen lamp", true},
		{"regex miss", rule(MatchRegex, `^turn (on|off) `), "the lights turn on", false},
		{"regex is case-insensitive", rule(MatchRegex, `^HELLO`), "hello there", true},
		{"hebrew contains", rule(MatchContains, "מה השעה"), "מה השעה עכשיו", true},
		{"hebrew exact", rule(MatchExact, "שלום"), "שלום", true},
		{"empty pattern never matches", rule(MatchContains, ""), "anything", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.rule
			if got := r.Matches(Normalize(tc.transcript)); got != tc.want {
				t.Fatalf("Matches(%q) = %v, want %v", tc.transcript, got, tc.want)
			}
		})
	}
}

func TestDisabledRuleNeverMatches(t *testing.T) {
	r := rule(MatchContains, "lights")
	r.Enabled = false
	if r.Matches("turn on the lights") {
		t.Fatal("a disabled rule matched")
	}
}

func TestRuleValidate(t *testing.T) {
	valid := rule(MatchContains, "lights")
	valid.HandlerConfig = json.RawMessage(`{"template":"ok"}`)

	tests := []struct {
		name    string
		mutate  func(*Rule)
		wantErr bool
	}{
		{"valid", func(*Rule) {}, false},
		{"no name", func(r *Rule) { r.Name = "  " }, true},
		{"bad match type", func(r *Rule) { r.MatchType = "fuzzy" }, true},
		{"bad handler", func(r *Rule) { r.Handler = "mqtt" }, true},
		{"bad role", func(r *Rule) { r.MinRole = "wizard" }, true},
		{"no patterns", func(r *Rule) { r.Patterns = nil }, true},
		{"invalid json config", func(r *Rule) { r.HandlerConfig = json.RawMessage(`{`) }, true},
		{"invalid regex", func(r *Rule) { r.MatchType = MatchRegex; r.Patterns = []string{"([a-z"} }, true},
		{"valid regex", func(r *Rule) { r.MatchType = MatchRegex; r.Patterns = []string{"^hi$"} }, false},

		// handler_config has to survive the handler that will read it, not
		// merely be JSON: a quoted string is valid JSON and useless here.
		{"config is a json string", func(r *Rule) {
			r.HandlerConfig = json.RawMessage(`"{\"template\":\"ok\"}"`)
		}, true},
		{"config is an array", func(r *Rule) { r.HandlerConfig = json.RawMessage(`["template"]`) }, true},
		{"reply without a template", func(r *Rule) { r.HandlerConfig = json.RawMessage(`{}`) }, true},
		{"reply with no config at all", func(r *Rule) { r.HandlerConfig = nil }, true},
		{"reply with a broken template", func(r *Rule) {
			r.HandlerConfig = json.RawMessage(`{"template":"hello {{.Speaker"}`)
		}, true},
		{"webhook without a url", func(r *Rule) {
			r.Handler = HandlerWebhook
			r.HandlerConfig = json.RawMessage(`{"method":"POST"}`)
		}, true},
		{"webhook with a url", func(r *Rule) {
			r.Handler = HandlerWebhook
			r.HandlerConfig = json.RawMessage(`{"url":"http://nas:8123/x"}`)
		}, false},
		{"webhook with a broken body template", func(r *Rule) {
			r.Handler = HandlerWebhook
			r.HandlerConfig = json.RawMessage(`{"url":"http://nas/x","body":"{{.Speaker"}`)
		}, true},
		{"webhook with a negative timeout", func(r *Rule) {
			r.Handler = HandlerWebhook
			r.HandlerConfig = json.RawMessage(`{"url":"http://nas/x","timeout_seconds":-1}`)
		}, true},
		{"llm needs nothing", func(r *Rule) {
			r.Handler = HandlerLLM
			r.HandlerConfig = nil
		}, false},
		{"llm with a negative word cap", func(r *Rule) {
			r.Handler = HandlerLLM
			r.HandlerConfig = json.RawMessage(`{"max_words":-5}`)
		}, true},
		{"llm with the wrong type for max_words", func(r *Rule) {
			r.Handler = HandlerLLM
			r.HandlerConfig = json.RawMessage(`{"max_words":"sixty"}`)
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidate := valid
			tc.mutate(&candidate)
			err := candidate.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
