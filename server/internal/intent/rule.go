// Package intent turns a transcript into something to say or do: a table of
// owner-defined rules first, a local LLM only as a fallback.
package intent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/t0mer/renfild/internal/speaker"
)

// Match types supported by the rule table.
const (
	MatchExact    = "exact"
	MatchContains = "contains"
	MatchRegex    = "regex"
)

// Handler names shipped in v1.
const (
	HandlerReply   = "reply"
	HandlerWebhook = "webhook"
	HandlerLLM     = "llm"
)

// Rule is one row of the intents table.
type Rule struct {
	ID            int64           `json:"id"`
	Name          string          `json:"name"`
	Enabled       bool            `json:"enabled"`
	MatchType     string          `json:"match_type"`
	Patterns      []string        `json:"patterns"`
	MinRole       speaker.Role    `json:"min_role"`
	Handler       string          `json:"handler"`
	HandlerConfig json.RawMessage `json:"handler_config"`
	Priority      int             `json:"priority"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`

	// compiled caches the regexes for MatchRegex rules.
	compiled []*regexp.Regexp
}

// Validate checks a rule before it is stored, so a bad regex is rejected at
// edit time rather than swallowed during an utterance.
func (r *Rule) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("name must not be empty")
	}
	switch r.MatchType {
	case MatchExact, MatchContains, MatchRegex:
	default:
		return fmt.Errorf("match_type %q must be one of exact, contains, regex", r.MatchType)
	}
	switch r.Handler {
	case HandlerReply, HandlerWebhook, HandlerLLM:
	default:
		return fmt.Errorf("handler %q must be one of reply, webhook, llm", r.Handler)
	}
	if !r.MinRole.Valid() {
		return fmt.Errorf("min_role %q must be one of any, kid, member, owner", r.MinRole)
	}
	if len(r.Patterns) == 0 {
		return fmt.Errorf("at least one pattern is required")
	}
	if len(r.HandlerConfig) > 0 && !json.Valid(r.HandlerConfig) {
		return fmt.Errorf("handler_config is not valid JSON")
	}
	if r.MatchType == MatchRegex {
		if err := r.Compile(); err != nil {
			return err
		}
	}
	return nil
}

// Compile prepares the regex patterns. Called by Validate and lazily by Matches.
func (r *Rule) Compile() error {
	if r.MatchType != MatchRegex {
		return nil
	}
	compiled := make([]*regexp.Regexp, 0, len(r.Patterns))
	for _, pattern := range r.Patterns {
		// (?i) so rules stay case-insensitive like the other match types.
		expr, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			return fmt.Errorf("pattern %q: %w", pattern, err)
		}
		compiled = append(compiled, expr)
	}
	r.compiled = compiled
	return nil
}

// Matches reports whether a normalized transcript triggers this rule.
func (r *Rule) Matches(transcript string) bool {
	if !r.Enabled {
		return false
	}
	switch r.MatchType {
	case MatchExact:
		for _, pattern := range r.Patterns {
			if transcript == Normalize(pattern) {
				return true
			}
		}
	case MatchContains:
		for _, pattern := range r.Patterns {
			needle := Normalize(pattern)
			if needle != "" && strings.Contains(transcript, needle) {
				return true
			}
		}
	case MatchRegex:
		if r.compiled == nil {
			if err := r.Compile(); err != nil {
				return false
			}
		}
		for _, expr := range r.compiled {
			if expr.MatchString(transcript) {
				return true
			}
		}
	}
	return false
}
