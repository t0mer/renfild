package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Ollama is a minimal chat client for a local Ollama instance.
type Ollama struct {
	BaseURL string
	Model   string
	HTTP    *http.Client
}

// NewOllama builds a client with a bounded HTTP timeout.
func NewOllama(baseURL, model string, timeout time.Duration) *Ollama {
	return &Ollama{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Model:   model,
		HTTP:    &http.Client{Timeout: timeout},
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatResponse struct {
	Message chatMessage `json:"message"`
	Error   string      `json:"error,omitempty"`
}

// Chat sends a single-turn conversation and returns the assistant's answer.
// Streaming is deliberately off: the reply is spoken as one piece anyway.
func (o *Ollama) Chat(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	messages := make([]chatMessage, 0, 2)
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, chatMessage{Role: "system", Content: systemPrompt})
	}
	messages = append(messages, chatMessage{Role: "user", Content: userPrompt})

	payload, err := json.Marshal(chatRequest{Model: o.Model, Messages: messages, Stream: false})
	if err != nil {
		return "", fmt.Errorf("encoding chat request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.BaseURL+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("building chat request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := o.client().Do(request)
	if err != nil {
		return "", fmt.Errorf("calling ollama: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading ollama response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned %s: %s", response.Status, snippet(body))
	}

	var decoded chatResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("decoding ollama response: %w", err)
	}
	if decoded.Error != "" {
		return "", fmt.Errorf("ollama error: %s", decoded.Error)
	}
	answer := strings.TrimSpace(decoded.Message.Content)
	if answer == "" {
		return "", fmt.Errorf("ollama returned an empty answer")
	}
	return answer, nil
}

// Health checks that the Ollama API is up.
func (o *Ollama) Health(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"/api/tags", nil)
	if err != nil {
		return err
	}
	response, err := o.client().Do(request)
	if err != nil {
		return fmt.Errorf("ollama unreachable: %w", err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama health returned %s", response.Status)
	}
	return nil
}

func (o *Ollama) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return http.DefaultClient
}
