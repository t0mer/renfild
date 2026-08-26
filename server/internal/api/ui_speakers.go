package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/t0mer/renfild/internal/speaker"
	"github.com/t0mer/renfild/internal/store"
)

// MaxEnrollmentBytes bounds an enrollment upload from the browser.
const MaxEnrollmentBytes = 16 << 20

type speakerPayload struct {
	Name      string   `json:"name"`
	Role      string   `json:"role"`
	Threshold *float64 `json:"threshold"`
}

func (s *Server) handleListSpeakers(w http.ResponseWriter, r *http.Request) {
	records, err := s.Store.ListSpeakers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if records == nil {
		records = []speaker.Record{}
	}
	writeJSON(w, http.StatusOK, records)
}

func (s *Server) handleGetSpeaker(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid speaker id")
		return
	}
	record, err := s.Store.GetSpeaker(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) handleCreateSpeaker(w http.ResponseWriter, r *http.Request) {
	var payload speakerPayload
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(payload.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	role := speaker.ParseRole(payload.Role)
	if role == speaker.RoleAny {
		// "any" is a permission floor, not something a person can be.
		writeError(w, http.StatusBadRequest, "role must be owner, member or kid")
		return
	}
	id, err := s.Store.CreateSpeaker(r.Context(), name, role, payload.Threshold)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	record, err := s.Store.GetSpeaker(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, record)
}

func (s *Server) handleUpdateSpeaker(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid speaker id")
		return
	}
	var payload speakerPayload
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(payload.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.Store.UpdateSpeaker(r.Context(), id, name, speaker.ParseRole(payload.Role), payload.Threshold); err != nil {
		writeStoreError(w, err)
		return
	}
	record, err := s.Store.GetSpeaker(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) handleDeleteSpeaker(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid speaker id")
		return
	}
	if err := s.Store.DeleteSpeaker(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type enrollmentView struct {
	speaker.Enrollment
	Similarity float64 `json:"similarity"`
}

func (s *Server) handleListEnrollments(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid speaker id")
		return
	}
	record, err := s.Store.GetSpeaker(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	samples, err := s.Store.ListEnrollments(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Each sample's similarity to the voice print tells the user which
	// recording to throw away.
	views := make([]enrollmentView, 0, len(samples))
	for _, sample := range samples {
		views = append(views, enrollmentView{
			Enrollment: sample,
			Similarity: speaker.Cosine(sample.Embedding, record.Centroid),
		})
	}
	writeJSON(w, http.StatusOK, views)
}

type enrollmentResult struct {
	Accepted   bool    `json:"accepted"`
	Similarity float64 `json:"similarity"`
	Reason     string  `json:"reason,omitempty"`
	ID         int64   `json:"id,omitempty"`
	Samples    int     `json:"samples"`
	Target     int     `json:"target"`
	DurationS  float64 `json:"duration_s"`
}

// handleCreateEnrollment embeds one recording and keeps it only if it resembles
// the samples already collected — a cough or a slammed door is rejected here
// rather than quietly poisoning the voice print.
func (s *Server) handleCreateEnrollment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid speaker id")
		return
	}
	if _, err := s.Store.GetSpeaker(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}

	audio, label, err := readAudioUpload(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.Embedder == nil {
		writeError(w, http.StatusServiceUnavailable, "embedder is not configured")
		return
	}

	vector, duration, err := s.Embedder.Embed(r.Context(), audio)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("embedder: %v", err))
		return
	}

	existing, err := s.Store.ListEnrollments(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	previous := make([]speaker.Vector, 0, len(existing))
	for _, sample := range existing {
		previous = append(previous, sample.Embedding)
	}

	tuning := s.Runtime.Get()
	check := speaker.CheckSample(vector, previous, tuning.MinSampleSimilarity)
	result := enrollmentResult{
		Accepted:   check.Accepted,
		Similarity: check.Similarity,
		Reason:     check.Reason,
		Samples:    len(existing),
		Target:     tuning.EnrollmentSamples,
		DurationS:  duration,
	}
	if !check.Accepted {
		writeJSON(w, http.StatusOK, result)
		return
	}

	enrollmentID, err := s.Store.AddEnrollment(r.Context(), id, vector, duration, label)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result.ID = enrollmentID
	result.Samples = len(existing) + 1
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) handleDeleteEnrollment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid speaker id")
		return
	}
	enrollmentID, err := pathID(r, "enrollmentID")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid enrollment id")
		return
	}
	if err := s.Store.DeleteEnrollment(r.Context(), id, enrollmentID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleIdentify is the "test my voice" button: embed a recording and report
// who it looks like, without storing anything.
func (s *Server) handleIdentify(w http.ResponseWriter, r *http.Request) {
	audio, _, err := readAudioUpload(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.Embedder == nil {
		writeError(w, http.StatusServiceUnavailable, "embedder is not configured")
		return
	}
	vector, duration, err := s.Embedder.Embed(r.Context(), audio)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("embedder: %v", err))
		return
	}
	records, err := s.Store.ListSpeakers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tuning := s.Runtime.Get()
	identity := speaker.Match(vector, records, tuning.SpeakerThreshold)

	// Every speaker's score, so a household can see how close the field is.
	scores := make(map[string]float64, len(records))
	for _, record := range records {
		scores[record.Name] = speaker.Cosine(vector, record.Centroid)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"identity":   identity,
		"scores":     scores,
		"duration_s": duration,
	})
}

// readAudioUpload accepts either a multipart "audio" file or a raw body.
func readAudioUpload(r *http.Request) ([]byte, string, error) {
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		r.Body = http.MaxBytesReader(nil, r.Body, MaxEnrollmentBytes)
		if err := r.ParseMultipartForm(MaxEnrollmentBytes); err != nil {
			return nil, "", fmt.Errorf("invalid multipart body: %w", err)
		}
		defer r.MultipartForm.RemoveAll()
		audio, err := readPart(r, "audio")
		if err != nil {
			return nil, "", err
		}
		return audio, strings.TrimSpace(r.FormValue("label")), nil
	}

	body := http.MaxBytesReader(nil, r.Body, MaxEnrollmentBytes)
	defer body.Close()
	audio, err := readAll(body)
	if err != nil {
		return nil, "", fmt.Errorf("reading audio: %w", err)
	}
	if len(audio) == 0 {
		return nil, "", fmt.Errorf("no audio submitted")
	}
	return audio, "", nil
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case strings.Contains(err.Error(), "UNIQUE constraint failed"):
		// A duplicate name is the user's mistake, not the server's.
		writeError(w, http.StatusConflict, "that name is already taken")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
