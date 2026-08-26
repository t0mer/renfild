// Package clients wraps the external services the pipeline talks to: the
// Whisper transcription endpoint, the embedder sidecar, Ollama, and the Piper
// binary. Each one hides its wire format behind a small interface so swapping
// an implementation touches exactly one file.
package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Whisper API shapes.
const (
	WhisperAPIOpenAI = "openai"
	WhisperAPIASR    = "asr"
)

// Whisper transcribes command audio.
type Whisper struct {
	BaseURL  string
	API      string
	Model    string
	Language string
	APIKey   string
	HTTP     *http.Client
}

// NewWhisper builds a client with a bounded HTTP timeout.
func NewWhisper(baseURL, api, model, language, apiKey string, timeout time.Duration) *Whisper {
	return &Whisper{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		API:      api,
		Model:    model,
		Language: language,
		APIKey:   apiKey,
		HTTP:     &http.Client{Timeout: timeout},
	}
}

type transcriptionResponse struct {
	Text string `json:"text"`
}

// Transcribe sends a WAV blob and returns the recognised text.
func (w *Whisper) Transcribe(ctx context.Context, wav []byte) (string, error) {
	if len(wav) == 0 {
		return "", fmt.Errorf("no audio to transcribe")
	}
	endpoint, fieldName, err := w.endpoint()
	if err != nil {
		return "", err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(fieldName, "command.wav")
	if err != nil {
		return "", fmt.Errorf("building transcription request: %w", err)
	}
	if _, err := part.Write(wav); err != nil {
		return "", fmt.Errorf("writing audio: %w", err)
	}
	if w.API == WhisperAPIOpenAI {
		fields := map[string]string{"model": w.Model, "response_format": "json"}
		if lang := w.languageHint(); lang != "" {
			fields["language"] = lang
		}
		for key, value := range fields {
			if value == "" {
				continue
			}
			if err := writer.WriteField(key, value); err != nil {
				return "", fmt.Errorf("writing field %s: %w", key, err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("closing multipart writer: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", fmt.Errorf("building transcription request: %w", err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if w.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+w.APIKey)
	}

	response, err := w.client().Do(request)
	if err != nil {
		return "", fmt.Errorf("calling whisper: %w", err)
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading whisper response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whisper returned %s: %s", response.Status, snippet(payload))
	}

	var decoded transcriptionResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		// Some deployments answer text/plain; treat the body as the transcript.
		text := strings.TrimSpace(string(payload))
		if text == "" {
			return "", fmt.Errorf("decoding whisper response: %w", err)
		}
		return text, nil
	}
	return strings.TrimSpace(decoded.Text), nil
}

// endpoint returns the URL to post to and the multipart field name for the audio.
func (w *Whisper) endpoint() (string, string, error) {
	switch w.API {
	case WhisperAPIOpenAI, "":
		return w.BaseURL + "/v1/audio/transcriptions", "file", nil
	case WhisperAPIASR:
		endpoint, err := url.Parse(w.BaseURL + "/asr")
		if err != nil {
			return "", "", fmt.Errorf("parsing whisper url: %w", err)
		}
		query := endpoint.Query()
		query.Set("task", "transcribe")
		query.Set("output", "json")
		query.Set("encode", "true")
		if lang := w.languageHint(); lang != "" {
			query.Set("language", lang)
		}
		endpoint.RawQuery = query.Encode()
		return endpoint.String(), "audio_file", nil
	default:
		return "", "", fmt.Errorf("unknown whisper api %q", w.API)
	}
}

// languageHint returns the configured language, or "" for automatic detection.
func (w *Whisper) languageHint() string {
	lang := strings.TrimSpace(strings.ToLower(w.Language))
	if lang == "" || lang == "auto" {
		return ""
	}
	return lang
}

func (w *Whisper) client() *http.Client {
	if w.HTTP != nil {
		return w.HTTP
	}
	return http.DefaultClient
}

// Health reports whether the endpoint answers at all.
func (w *Whisper) Health(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, w.BaseURL+"/", nil)
	if err != nil {
		return err
	}
	response, err := w.client().Do(request)
	if err != nil {
		return fmt.Errorf("whisper unreachable: %w", err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return nil
}

func snippet(payload []byte) string {
	text := strings.TrimSpace(string(payload))
	if len(text) > 200 {
		return text[:200] + "…"
	}
	return text
}
