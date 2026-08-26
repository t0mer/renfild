package api

import (
	"net/http"

	"github.com/t0mer/renfild/internal/config"
)

// RuntimeSettingsKey is the settings-table key the mutable knobs live under.
const RuntimeSettingsKey = "runtime"

type settingsResponse struct {
	// Runtime holds the values the UI can change and the server applies live.
	Runtime config.RuntimeSettings `json:"runtime"`
	// Static holds the values that come from the config file. Changing them
	// means editing the file and restarting the service.
	Static staticSettings `json:"static"`
}

type staticSettings struct {
	Listen         string `json:"listen"`
	DB             string `json:"db"`
	AudioRetention string `json:"audio_retention"`
	AudioDir       string `json:"audio_dir"`
	WhisperURL     string `json:"whisper_url"`
	WhisperAPI     string `json:"whisper_api"`
	WhisperLang    string `json:"whisper_language"`
	EmbedderURL    string `json:"embedder_url"`
	OllamaURL      string `json:"ollama_url"`
	OllamaModel    string `json:"ollama_model"`
	PiperBinary    string `json:"piper_binary"`
	PiperVoice     string `json:"piper_voice"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, settingsResponse{
		Runtime: s.Runtime.Get(),
		Static: staticSettings{
			Listen:         s.Config.Listen,
			DB:             s.Config.DB,
			AudioRetention: s.Config.AudioRetention,
			AudioDir:       s.Config.AudioDir,
			WhisperURL:     s.Config.Whisper.URL,
			WhisperAPI:     s.Config.Whisper.API,
			WhisperLang:    s.Config.Whisper.Language,
			EmbedderURL:    s.Config.Embedder.URL,
			OllamaURL:      s.Config.Ollama.URL,
			OllamaModel:    s.Config.Ollama.Model,
			PiperBinary:    s.Config.Piper.Binary,
			PiperVoice:     s.Config.Piper.Voice,
		},
	})
}

// handleUpdateSettings applies the mutable settings immediately and persists
// them, so they survive a restart without editing the config file.
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var payload config.RuntimeSettings
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Runtime.Set(payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Store.PutSetting(r.Context(), RuntimeSettingsKey, s.Runtime.Get()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.handleGetSettings(w, r)
}
