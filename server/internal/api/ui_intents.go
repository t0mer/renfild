package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/t0mer/renfild/internal/intent"
	"github.com/t0mer/renfild/internal/speaker"
)

type intentPayload struct {
	Name          string          `json:"name"`
	Enabled       bool            `json:"enabled"`
	MatchType     string          `json:"match_type"`
	Patterns      []string        `json:"patterns"`
	MinRole       string          `json:"min_role"`
	Handler       string          `json:"handler"`
	HandlerConfig json.RawMessage `json:"handler_config"`
	Priority      int             `json:"priority"`
}

func (p intentPayload) toRule() intent.Rule {
	return intent.Rule{
		Name:          strings.TrimSpace(p.Name),
		Enabled:       p.Enabled,
		MatchType:     strings.TrimSpace(p.MatchType),
		Patterns:      p.Patterns,
		MinRole:       speaker.ParseRole(p.MinRole),
		Handler:       strings.TrimSpace(p.Handler),
		HandlerConfig: p.HandlerConfig,
		Priority:      p.Priority,
	}
}

func (s *Server) handleListIntents(w http.ResponseWriter, r *http.Request) {
	rules, err := s.Store.ListIntents(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rules == nil {
		rules = []intent.Rule{}
	}
	writeJSON(w, http.StatusOK, rules)
}

func (s *Server) handleGetIntent(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid intent id")
		return
	}
	rule, err := s.Store.GetIntent(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (s *Server) handleCreateIntent(w http.ResponseWriter, r *http.Request) {
	var payload intentPayload
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.Store.CreateIntent(r.Context(), payload.toRule())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rule, err := s.Store.GetIntent(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

func (s *Server) handleUpdateIntent(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid intent id")
		return
	}
	var payload intentPayload
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Store.UpdateIntent(r.Context(), id, payload.toRule()); err != nil {
		writeStoreError(w, err)
		return
	}
	rule, err := s.Store.GetIntent(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (s *Server) handleDeleteIntent(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid intent id")
		return
	}
	if err := s.Store.DeleteIntent(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type reorderPayload struct {
	// Order lists intent ids in the priority order the user dragged them into.
	Order []int64 `json:"order"`
}

func (s *Server) handleReorderIntents(w http.ResponseWriter, r *http.Request) {
	var payload reorderPayload
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(payload.Order) == 0 {
		writeError(w, http.StatusBadRequest, "order must not be empty")
		return
	}
	// Space priorities out by ten so a single rule can later be slotted in
	// between two others without renumbering everything.
	priorities := make(map[int64]int, len(payload.Order))
	for index, id := range payload.Order {
		priorities[id] = (index + 1) * 10
	}
	if err := s.Store.ReorderIntents(r.Context(), priorities); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.handleListIntents(w, r)
}

type testIntentPayload struct {
	Transcript  string `json:"transcript"`
	Speaker     string `json:"speaker"`
	Role        string `json:"role"`
	SatelliteID string `json:"satellite_id"`
	Known       *bool  `json:"known"`
}

type testIntentResponse struct {
	Normalized string        `json:"normalized"`
	Result     intent.Result `json:"result"`
	Error      string        `json:"error,omitempty"`
}

// handleTestIntent is a dry run: route a transcript as if a given speaker had
// said it, without speaking or recording anything.
func (s *Server) handleTestIntent(w http.ResponseWriter, r *http.Request) {
	var payload testIntentPayload
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(payload.Transcript) == "" {
		writeError(w, http.StatusBadRequest, "transcript is required")
		return
	}

	role := speaker.ParseRole(payload.Role)
	known := payload.Speaker != "" && payload.Speaker != speaker.Unknown
	if payload.Known != nil {
		known = *payload.Known
	}
	if !known {
		role = speaker.UnknownRole
	}

	result, err := s.Pipeline.Router.Route(r.Context(), intent.Request{
		Speaker:     payload.Speaker,
		Role:        role,
		Known:       known,
		Transcript:  payload.Transcript,
		SatelliteID: payload.SatelliteID,
		Now:         time.Now(),
	})
	response := testIntentResponse{
		Normalized: intent.Normalize(payload.Transcript),
		Result:     result,
	}
	if err != nil {
		response.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, response)
}

type testTTSPayload struct {
	Text string `json:"text"`
}

// handleTestTTS synthesises a phrase and returns the WAV, so the Settings page
// can prove the voice works.
func (s *Server) handleTestTTS(w http.ResponseWriter, r *http.Request) {
	var payload testTTSPayload
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		text = "Renfild is listening."
	}
	if s.TTS == nil {
		writeError(w, http.StatusServiceUnavailable, "no TTS engine configured")
		return
	}
	audio, err := s.TTS.Synthesize(r.Context(), text)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.WriteHeader(http.StatusOK)
	w.Write(audio)
}
