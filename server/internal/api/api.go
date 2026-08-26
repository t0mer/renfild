// Package api exposes the HTTP surface: the satellite endpoint, the web UI's
// REST API, health and metrics, and the embedded single-page app.
package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/t0mer/renfild/internal/clients"
	"github.com/t0mer/renfild/internal/config"
	"github.com/t0mer/renfild/internal/metrics"
	"github.com/t0mer/renfild/internal/pipeline"
	"github.com/t0mer/renfild/internal/store"
	"github.com/t0mer/renfild/internal/version"
)

// MaxUploadBytes bounds a satellite's multipart upload: a 10 s command plus a
// 2 s wake snapshot at 16 kHz mono is well under 1 MB, so this is generous.
const MaxUploadBytes = 25 << 20

// Server holds everything the handlers need.
type Server struct {
	Store    *store.Store
	Pipeline *pipeline.Pipeline
	Config   *config.Config
	Runtime  *config.Runtime
	Metrics  *metrics.Metrics
	Log      *slog.Logger
	Events   *EventHub

	// Embedder and TTS back the web UI's test buttons.
	Embedder pipeline.Embedder
	TTS      clients.TTS
}

// Routes builds the router.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	r.Get("/healthz", s.handleHealth)
	r.Handle("/metrics", promhttp.HandlerFor(s.Metrics.Registry, promhttp.HandlerOpts{}))

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/utterance", s.handleUtterance)
	})

	r.Route("/api/ui", func(r chi.Router) {
		r.Get("/stats", s.handleStats)
		r.Get("/events", s.handleEvents)

		r.Route("/speakers", func(r chi.Router) {
			r.Get("/", s.handleListSpeakers)
			r.Post("/", s.handleCreateSpeaker)
			r.Post("/identify", s.handleIdentify)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", s.handleGetSpeaker)
				r.Put("/", s.handleUpdateSpeaker)
				r.Delete("/", s.handleDeleteSpeaker)
				r.Get("/enrollments", s.handleListEnrollments)
				r.Post("/enrollments", s.handleCreateEnrollment)
				r.Delete("/enrollments/{enrollmentID}", s.handleDeleteEnrollment)
			})
		})

		r.Route("/intents", func(r chi.Router) {
			r.Get("/", s.handleListIntents)
			r.Post("/", s.handleCreateIntent)
			r.Post("/reorder", s.handleReorderIntents)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", s.handleGetIntent)
				r.Put("/", s.handleUpdateIntent)
				r.Delete("/", s.handleDeleteIntent)
			})
		})

		r.Get("/history", s.handleHistory)
		r.Get("/history/{id}/audio", s.handleHistoryAudio)

		r.Get("/settings", s.handleGetSettings)
		r.Put("/settings", s.handleUpdateSettings)

		r.Post("/test/tts", s.handleTestTTS)
		r.Post("/test/intent", s.handleTestIntent)
	})

	r.Handle("/*", SPAHandler())
	return r
}

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DB().PingContext(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "database unavailable",
			"error":  err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Version: version.Version})
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Default().Error("writing json response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func pathID(r *http.Request, key string) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, key), 10, 64)
}

func queryInt(r *http.Request, key string, fallback int) int {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// readAll drains a reader fully; a tiny helper so upload paths stay readable.
func readAll(r io.Reader) ([]byte, error) {
	return io.ReadAll(r)
}

func (s *Server) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}
