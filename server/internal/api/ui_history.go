package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/t0mer/renfild/internal/store"
)

type historyResponse struct {
	Items  []store.Utterance `json:"items"`
	Total  int               `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	filter := store.HistoryFilter{
		Speaker: strings.TrimSpace(r.URL.Query().Get("speaker")),
		Intent:  strings.TrimSpace(r.URL.Query().Get("intent")),
		Search:  strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:   queryInt(r, "limit", 50),
		Offset:  queryInt(r, "offset", 0),
	}
	items, total, err := s.Store.ListUtterances(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []store.Utterance{}
	}
	writeJSON(w, http.StatusOK, historyResponse{
		Items:  items,
		Total:  total,
		Limit:  filter.Limit,
		Offset: filter.Offset,
	})
}

// handleHistoryAudio serves a retained recording. It only ever serves files
// from inside the configured audio directory, whatever the database says.
func (s *Server) handleHistoryAudio(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid utterance id")
		return
	}
	utterance, err := s.Store.GetUtterance(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if utterance.AudioPath == "" {
		writeError(w, http.StatusNotFound, "no audio stored for this utterance")
		return
	}

	// Defence in depth: resolve the stored path and refuse anything that
	// escapes the audio directory, however it got into the database.
	root, err := filepath.Abs(s.Config.AudioDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	path, err := filepath.Abs(utterance.AudioPath)
	if err != nil || !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		writeError(w, http.StatusForbidden, "audio path is outside the audio directory")
		return
	}

	file, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "audio file is gone")
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.Store.Stats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
