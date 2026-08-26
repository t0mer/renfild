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

	"github.com/t0mer/renfild/internal/speaker"
)

// Embedder talks to the Python ECAPA sidecar.
type Embedder struct {
	BaseURL string
	HTTP    *http.Client
}

// NewEmbedder builds a client with a bounded HTTP timeout.
func NewEmbedder(baseURL string, timeout time.Duration) *Embedder {
	return &Embedder{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: timeout},
	}
}

type embedResponse struct {
	Embedding []float32 `json:"embedding"`
	DurationS float64   `json:"duration_s"`
	Dims      int       `json:"dims"`
}

// Embed converts a WAV blob into a speaker vector.
func (e *Embedder) Embed(ctx context.Context, wav []byte) (speaker.Vector, float64, error) {
	if len(wav) == 0 {
		return nil, 0, fmt.Errorf("no audio to embed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.BaseURL+"/embed", bytes.NewReader(wav))
	if err != nil {
		return nil, 0, fmt.Errorf("building embed request: %w", err)
	}
	request.Header.Set("Content-Type", "audio/wav")

	response, err := e.client().Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("calling embedder: %w", err)
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("reading embedder response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("embedder returned %s: %s", response.Status, snippet(payload))
	}

	var decoded embedResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, 0, fmt.Errorf("decoding embedder response: %w", err)
	}
	if len(decoded.Embedding) == 0 {
		return nil, 0, fmt.Errorf("embedder returned an empty embedding")
	}
	return speaker.Vector(decoded.Embedding), decoded.DurationS, nil
}

// Health checks the sidecar's readiness endpoint.
func (e *Embedder) Health(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, e.BaseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	response, err := e.client().Do(request)
	if err != nil {
		return fmt.Errorf("embedder unreachable: %w", err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("embedder health returned %s", response.Status)
	}
	return nil
}

func (e *Embedder) client() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return http.DefaultClient
}
