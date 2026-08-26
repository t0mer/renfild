package intent

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/t0mer/renfild/internal/speaker"
)

// Default spoken responses. They are fields on Router so a household can change
// the wording without touching code.
const (
	DefaultUnknownReply   = "Sorry, I don't recognize your voice."
	DefaultDeniedReply    = "Sorry {{.Speaker}}, you are not allowed to do that."
	DefaultNoMatchReply   = "Sorry, I didn't get that."
	DefaultLLMSystem      = "You are Renfild, a concise household voice assistant. Answer briefly and plainly."
	fallbackIntentName    = "llm-fallback"
	noMatchIntentName     = "no-match"
	deniedIntentName      = "denied"
	unknownSpeakerRefusal = "unknown-speaker"
)

// RuleSource supplies the current rule table. The store implements it; tests
// pass a literal slice.
type RuleSource interface {
	Rules(ctx context.Context) ([]Rule, error)
}

// RuleSourceFunc adapts a function to RuleSource.
type RuleSourceFunc func(ctx context.Context) ([]Rule, error)

// Rules calls f.
func (f RuleSourceFunc) Rules(ctx context.Context) ([]Rule, error) { return f(ctx) }

// Router walks the rule table and dispatches to a handler.
type Router struct {
	Source   RuleSource
	Handlers map[string]Handler
	Log      *slog.Logger

	// LLMFallback reports whether unmatched transcripts go to the LLM. It is a
	// function because the web UI can flip the switch while the server runs.
	LLMFallback func() bool
	// Replies for the three "no" cases.
	UnknownReply string
	DeniedReply  string
	NoMatchReply string
}

// New builds a router with the standard handler set.
func New(source RuleSource, handlers map[string]Handler, llmFallback func() bool, log *slog.Logger) *Router {
	if log == nil {
		log = slog.Default()
	}
	return &Router{
		Source:       source,
		Handlers:     handlers,
		Log:          log,
		LLMFallback:  llmFallback,
		UnknownReply: DefaultUnknownReply,
		DeniedReply:  DefaultDeniedReply,
		NoMatchReply: DefaultNoMatchReply,
	}
}

// Route picks the first enabled rule that matches, enforces its role floor and
// runs its handler. A handler failure is not fatal: the caller gets a spoken
// apology and the error, and decides what to log.
func (r *Router) Route(ctx context.Context, req Request) (Result, error) {
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	transcript := Normalize(req.Transcript)

	rules, err := r.Source.Rules(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("loading intents: %w", err)
	}
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Priority != rules[j].Priority {
			return rules[i].Priority < rules[j].Priority
		}
		return rules[i].ID < rules[j].ID
	})

	for i := range rules {
		rule := rules[i]
		if !rule.Matches(transcript) {
			continue
		}
		if !req.Role.Allows(rule.MinRole) {
			return r.deny(rule, req), nil
		}
		handler, ok := r.Handlers[rule.Handler]
		if !ok {
			return Result{}, fmt.Errorf("intent %q: no handler registered for %q", rule.Name, rule.Handler)
		}
		result, err := handler.Handle(ctx, rule, req)
		if err != nil {
			return Result{Intent: rule.Name, Handler: rule.Handler, Matched: true, Allowed: true}, err
		}
		if result.Intent == "" {
			result.Intent = rule.Name
		}
		return result, nil
	}

	return r.fallback(ctx, req)
}

// deny produces the spoken refusal for a speaker below the rule's role floor.
func (r *Router) deny(rule Rule, req Request) Result {
	text := r.UnknownReply
	name := unknownSpeakerRefusal
	if req.Known {
		text = r.DeniedReply
		name = deniedIntentName
		if rendered, err := render("denied", text, req); err == nil {
			text = rendered
		}
	}
	r.Log.Info("intent denied",
		"intent", rule.Name,
		"speaker", req.Speaker,
		"role", string(req.Role),
		"min_role", string(rule.MinRole))
	return Result{Intent: name, Handler: rule.Handler, Reply: text, Allowed: false, Matched: true}
}

// fallback handles a transcript that matched nothing.
func (r *Router) fallback(ctx context.Context, req Request) (Result, error) {
	if r.LLMFallback != nil && r.LLMFallback() {
		if handler, ok := r.Handlers[HandlerLLM]; ok {
			// An unidentified voice does not get to talk to the model.
			if req.Role.Allows(speaker.RoleMember) {
				rule := Rule{Name: fallbackIntentName, Enabled: true, Handler: HandlerLLM, MinRole: speaker.RoleMember}
				result, err := handler.Handle(ctx, rule, req)
				if err != nil {
					return Result{Intent: fallbackIntentName, Handler: HandlerLLM, Matched: false, Allowed: true}, err
				}
				result.Matched = false
				return result, nil
			}
			return Result{
				Intent:  unknownSpeakerRefusal,
				Handler: HandlerLLM,
				Reply:   r.UnknownReply,
				Allowed: false,
			}, nil
		}
	}
	return Result{Intent: noMatchIntentName, Reply: r.NoMatchReply, Allowed: true}, nil
}
