package intent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/t0mer/renfild/internal/speaker"
)

// Request is everything a handler knows about the utterance it is answering.
type Request struct {
	Speaker     string
	Role        speaker.Role
	Known       bool
	Confidence  float64
	Transcript  string
	SatelliteID string
	Now         time.Time
}

// Result is what the router hands back to the pipeline.
type Result struct {
	Intent  string `json:"intent"`
	Handler string `json:"handler"`
	Reply   string `json:"reply"`
	Allowed bool   `json:"allowed"`
	// Matched is false when no rule fired and the fallback answered.
	Matched bool `json:"matched"`
}

// Handler executes one kind of intent. Adding a new handler type — MQTT, exec —
// means implementing this interface and registering it; nothing else changes.
type Handler interface {
	Handle(ctx context.Context, rule Rule, req Request) (Result, error)
}

// templateData is the value templates are executed against.
type templateData struct {
	Speaker     string
	Role        string
	Transcript  string
	SatelliteID string
	Confidence  float64
	Known       bool
	Now         time.Time
}

func newTemplateData(req Request) templateData {
	return templateData{
		Speaker:     req.Speaker,
		Role:        string(req.Role),
		Transcript:  req.Transcript,
		SatelliteID: req.SatelliteID,
		Confidence:  req.Confidence,
		Known:       req.Known,
		Now:         req.Now,
	}
}

// render executes a text/template against the request data.
func render(name, text string, req Request) (string, error) {
	tmpl, err := template.New(name).Option("missingkey=zero").Parse(text)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, newTemplateData(req)); err != nil {
		return "", fmt.Errorf("executing template: %w", err)
	}
	return strings.TrimSpace(buf.String()), nil
}

// validateHandlerConfig checks handler_config against the shape the chosen
// handler will decode it into. Without this a config that is valid JSON but the
// wrong shape — a quoted string instead of an object is the easy mistake —
// stores happily and only surfaces days later as a spoken "Sorry, something
// went wrong". The templates are parsed here too, for the same reason.
func validateHandlerConfig(rule Rule) error {
	switch rule.Handler {
	case HandlerReply:
		var cfg replyConfig
		if err := decodeConfig(rule.HandlerConfig, &cfg); err != nil {
			return err
		}
		if strings.TrimSpace(cfg.Template) == "" {
			return fmt.Errorf("reply handler needs a template")
		}
		return parseTemplate("template", cfg.Template)

	case HandlerWebhook:
		var cfg webhookConfig
		if err := decodeConfig(rule.HandlerConfig, &cfg); err != nil {
			return err
		}
		if strings.TrimSpace(cfg.URL) == "" {
			return fmt.Errorf("webhook handler needs a url")
		}
		// Checked in a fixed order so a rule with two broken templates always
		// reports the same one.
		for _, field := range []struct{ name, text string }{
			{"url", cfg.URL}, {"body", cfg.Body}, {"reply", cfg.Reply},
		} {
			if err := parseTemplate(field.name, field.text); err != nil {
				return err
			}
		}
		if method := strings.ToUpper(strings.TrimSpace(cfg.Method)); method != "" {
			// http.NewRequest accepts anything token-shaped, so this only
			// catches the obvious typo of a method with a space in it.
			if strings.ContainsAny(method, " \t") {
				return fmt.Errorf("webhook method %q is not a method", cfg.Method)
			}
		}
		if cfg.TimeoutSeconds < 0 {
			return fmt.Errorf("webhook timeout_seconds must not be negative")
		}

	case HandlerLLM:
		var cfg llmConfig
		if err := decodeConfig(rule.HandlerConfig, &cfg); err != nil {
			return err
		}
		if cfg.MaxWords < 0 {
			return fmt.Errorf("llm max_words must not be negative")
		}
		return parseTemplate("prompt", cfg.Prompt)
	}
	return nil
}

// parseTemplate reports whether a template field would render at all.
func parseTemplate(field, text string) error {
	if text == "" {
		return nil
	}
	_, err := template.New(field).Option("missingkey=zero").Parse(text)
	if err == nil {
		return nil
	}
	// text/template writes "template: <name>:<line>: <what>", and the name is
	// already the field, so its own prefix would only repeat the word.
	return errors.New(strings.TrimPrefix(err.Error(), "template: "))
}

// ReplyHandler speaks a template. The simplest and most common handler.
type ReplyHandler struct{}

type replyConfig struct {
	Template string `json:"template"`
}

// Handle renders the configured template.
func (ReplyHandler) Handle(_ context.Context, rule Rule, req Request) (Result, error) {
	var cfg replyConfig
	if err := decodeConfig(rule.HandlerConfig, &cfg); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(cfg.Template) == "" {
		return Result{}, fmt.Errorf("intent %q: reply handler needs a template", rule.Name)
	}
	text, err := render(rule.Name, cfg.Template, req)
	if err != nil {
		return Result{}, fmt.Errorf("intent %q: %w", rule.Name, err)
	}
	return Result{Intent: rule.Name, Handler: HandlerReply, Reply: text, Allowed: true, Matched: true}, nil
}

// WebhookHandler calls an HTTP endpoint, optionally speaking its response.
type WebhookHandler struct {
	Client *http.Client
	// MaxSpokenBytes caps how much of a webhook response is read back aloud.
	MaxSpokenBytes int64
}

type webhookConfig struct {
	URL            string            `json:"url"`
	Method         string            `json:"method"`
	Headers        map[string]string `json:"headers"`
	Body           string            `json:"body"`
	SpeakResponse  bool              `json:"speak_response"`
	Reply          string            `json:"reply"`
	ContentType    string            `json:"content_type"`
	TimeoutSeconds float64           `json:"timeout_seconds"`
}

// Handle performs the configured request.
func (h WebhookHandler) Handle(ctx context.Context, rule Rule, req Request) (Result, error) {
	var cfg webhookConfig
	if err := decodeConfig(rule.HandlerConfig, &cfg); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return Result{}, fmt.Errorf("intent %q: webhook handler needs a url", rule.Name)
	}

	url, err := render(rule.Name+":url", cfg.URL, req)
	if err != nil {
		return Result{}, err
	}
	method := strings.ToUpper(strings.TrimSpace(cfg.Method))
	if method == "" {
		method = http.MethodPost
	}

	var body io.Reader
	if cfg.Body != "" {
		rendered, err := render(rule.Name+":body", cfg.Body, req)
		if err != nil {
			return Result{}, err
		}
		body = strings.NewReader(rendered)
	}

	if cfg.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds*float64(time.Second)))
		defer cancel()
	}

	request, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return Result{}, fmt.Errorf("intent %q: building request: %w", rule.Name, err)
	}
	if cfg.ContentType != "" {
		request.Header.Set("Content-Type", cfg.ContentType)
	} else if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range cfg.Headers {
		request.Header.Set(key, value)
	}

	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return Result{}, fmt.Errorf("intent %q: calling webhook: %w", rule.Name, err)
	}
	defer response.Body.Close()

	limit := h.MaxSpokenBytes
	if limit <= 0 {
		limit = 4096
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, limit))
	if err != nil {
		return Result{}, fmt.Errorf("intent %q: reading webhook response: %w", rule.Name, err)
	}
	if response.StatusCode >= 400 {
		return Result{}, fmt.Errorf("intent %q: webhook returned %s", rule.Name, response.Status)
	}

	result := Result{Intent: rule.Name, Handler: HandlerWebhook, Allowed: true, Matched: true}
	switch {
	case cfg.SpeakResponse:
		result.Reply = strings.TrimSpace(string(payload))
	case cfg.Reply != "":
		text, err := render(rule.Name+":reply", cfg.Reply, req)
		if err != nil {
			return Result{}, err
		}
		result.Reply = text
	}
	return result, nil
}

// LLM is the slice of an Ollama client the intent router needs.
type LLM interface {
	Chat(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// LLMHandler asks the local model. Replies are capped: nobody wants a lecture
// from a speaker.
type LLMHandler struct {
	Client       LLM
	SystemPrompt string
	MaxWords     int
}

type llmConfig struct {
	SystemPrompt string `json:"system_prompt"`
	MaxWords     int    `json:"max_words"`
	Prompt       string `json:"prompt"`
}

// Handle sends the transcript to the model with speaker context attached.
func (h LLMHandler) Handle(ctx context.Context, rule Rule, req Request) (Result, error) {
	if h.Client == nil {
		return Result{}, fmt.Errorf("intent %q: no LLM configured", rule.Name)
	}
	var cfg llmConfig
	if err := decodeConfig(rule.HandlerConfig, &cfg); err != nil {
		return Result{}, err
	}

	maxWords := cfg.MaxWords
	if maxWords <= 0 {
		maxWords = h.MaxWords
	}
	system := cfg.SystemPrompt
	if system == "" {
		system = h.SystemPrompt
	}
	system = strings.TrimSpace(strings.Join([]string{
		system,
		fmt.Sprintf("You are talking to %s (role: %s).", speakerLabel(req), req.Role),
		fmt.Sprintf("Answer out loud in at most %d words. Plain text only, no markdown.", maxWords),
	}, " "))

	prompt := req.Transcript
	if cfg.Prompt != "" {
		rendered, err := render(rule.Name+":prompt", cfg.Prompt, req)
		if err != nil {
			return Result{}, err
		}
		prompt = rendered
	}

	answer, err := h.Client.Chat(ctx, system, prompt)
	if err != nil {
		return Result{}, fmt.Errorf("intent %q: %w", rule.Name, err)
	}
	return Result{
		Intent:  rule.Name,
		Handler: HandlerLLM,
		Reply:   TruncateWords(strings.TrimSpace(answer), maxWords),
		Allowed: true,
		Matched: true,
	}, nil
}

func speakerLabel(req Request) string {
	if req.Known && req.Speaker != "" {
		return req.Speaker
	}
	return "an unidentified person"
}

// TruncateWords trims a reply to at most n words, ending it cleanly.
func TruncateWords(text string, n int) string {
	if n <= 0 {
		return text
	}
	fields := strings.Fields(text)
	if len(fields) <= n {
		return text
	}
	trimmed := strings.Join(fields[:n], " ")
	if !strings.HasSuffix(trimmed, ".") {
		trimmed += "."
	}
	return trimmed
}

func decodeConfig(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decoding handler_config: %w", err)
	}
	return nil
}
