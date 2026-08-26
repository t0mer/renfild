package config

import "sync"

// Runtime holds the handful of settings the web UI can change without a
// restart. Everything else (endpoints, ports, paths) comes from the config file
// and needs the service restarted, which the Settings page says plainly.
type Runtime struct {
	mu       sync.RWMutex
	settings RuntimeSettings
}

// RuntimeSettings is the mutable subset, and the shape of the settings API.
type RuntimeSettings struct {
	SpeakerThreshold    float64 `json:"speaker_threshold"`
	UnknownPolicy       string  `json:"unknown_policy"`
	LowConfidenceMargin float64 `json:"low_confidence_margin"`
	LLMFallback         bool    `json:"llm_fallback"`
	MinSampleSimilarity float64 `json:"min_sample_similarity"`
	EnrollmentSamples   int     `json:"enrollment_samples"`
}

// NewRuntime seeds the mutable settings from the static configuration.
func NewRuntime(cfg *Config) *Runtime {
	return &Runtime{settings: RuntimeSettings{
		SpeakerThreshold:    cfg.Speaker.DefaultThreshold,
		UnknownPolicy:       cfg.Speaker.UnknownPolicy,
		LowConfidenceMargin: cfg.Speaker.LowConfidenceMargin,
		LLMFallback:         cfg.Ollama.Fallback,
		MinSampleSimilarity: cfg.Speaker.MinSampleSimilarity,
		EnrollmentSamples:   cfg.Speaker.EnrollmentSamples,
	}}
}

// Get returns a copy of the current settings.
func (r *Runtime) Get() RuntimeSettings {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.settings
}

// Set replaces the settings after validating them.
func (r *Runtime) Set(next RuntimeSettings) error {
	if err := next.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settings = next
	return nil
}

// Validate checks a proposed settings update.
func (s RuntimeSettings) Validate() error {
	if s.SpeakerThreshold < -1 || s.SpeakerThreshold > 1 {
		return &ValidationError{Field: "speaker_threshold", Msg: "must be between -1 and 1"}
	}
	switch s.UnknownPolicy {
	case PolicyRestricted, PolicyDeny, PolicyAllow:
	default:
		return &ValidationError{Field: "unknown_policy", Msg: "must be restricted, deny or allow"}
	}
	if s.LowConfidenceMargin < 0 || s.LowConfidenceMargin > 0.5 {
		return &ValidationError{Field: "low_confidence_margin", Msg: "must be between 0 and 0.5"}
	}
	if s.MinSampleSimilarity < 0 || s.MinSampleSimilarity > 1 {
		return &ValidationError{Field: "min_sample_similarity", Msg: "must be between 0 and 1"}
	}
	if s.EnrollmentSamples < 1 || s.EnrollmentSamples > 20 {
		return &ValidationError{Field: "enrollment_samples", Msg: "must be between 1 and 20"}
	}
	return nil
}

// ValidationError reports an invalid settings field.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Msg }
