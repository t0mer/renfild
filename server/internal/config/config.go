// Package config loads the server configuration.
//
// Precedence is flags > environment (prefix RENFILD_) > config file > built-in
// defaults. Nested keys map to environment variables by replacing dots with
// underscores: whisper.url becomes RENFILD_WHISPER_URL.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// EnvPrefix is prepended to every environment variable the server reads.
const EnvPrefix = "RENFILD"

// Config is the whole server configuration.
type Config struct {
	Listen         string   `mapstructure:"listen" json:"listen"`
	DB             string   `mapstructure:"db" json:"db"`
	LogLevel       string   `mapstructure:"log_level" json:"log_level"`
	AudioRetention string   `mapstructure:"audio_retention" json:"audio_retention"`
	AudioDir       string   `mapstructure:"audio_dir" json:"audio_dir"`
	Whisper        Whisper  `mapstructure:"whisper" json:"whisper"`
	Embedder       Embedder `mapstructure:"embedder" json:"embedder"`
	Ollama         Ollama   `mapstructure:"ollama" json:"ollama"`
	Piper          Piper    `mapstructure:"piper" json:"piper"`
	Speaker        Speaker  `mapstructure:"speaker" json:"speaker"`
}

// Whisper describes the speech-to-text endpoint.
type Whisper struct {
	URL string `mapstructure:"url" json:"url"`
	// API selects the request shape: "openai" for /v1/audio/transcriptions,
	// "asr" for the whisper-asr-webservice /asr endpoint.
	API      string        `mapstructure:"api" json:"api"`
	Model    string        `mapstructure:"model" json:"model"`
	Language string        `mapstructure:"language" json:"language"`
	APIKey   string        `mapstructure:"api_key" json:"-"`
	Timeout  time.Duration `mapstructure:"timeout" json:"timeout"`
}

// Embedder describes the speaker-embedding sidecar.
type Embedder struct {
	URL     string        `mapstructure:"url" json:"url"`
	Timeout time.Duration `mapstructure:"timeout" json:"timeout"`
}

// Ollama describes the local LLM used by the fallback intent handler.
type Ollama struct {
	URL              string        `mapstructure:"url" json:"url"`
	Model            string        `mapstructure:"model" json:"model"`
	SystemPromptFile string        `mapstructure:"system_prompt_file" json:"system_prompt_file"`
	Timeout          time.Duration `mapstructure:"timeout" json:"timeout"`
	// MaxWords caps the spoken answer. Nobody wants a lecture from a speaker.
	MaxWords int `mapstructure:"max_words" json:"max_words"`
	// Fallback enables routing unmatched transcripts to the LLM.
	Fallback bool `mapstructure:"fallback" json:"fallback"`
}

// Piper describes the local text-to-speech binary.
type Piper struct {
	Binary    string        `mapstructure:"binary" json:"binary"`
	Voice     string        `mapstructure:"voice" json:"voice"`
	SpeakerID int           `mapstructure:"speaker_id" json:"speaker_id"`
	Timeout   time.Duration `mapstructure:"timeout" json:"timeout"`
	// ExtraArgs is passed verbatim to the binary, for voice-specific tuning
	// such as --length_scale.
	ExtraArgs []string `mapstructure:"extra_args" json:"extra_args"`
}

// Speaker holds the speaker-recognition tuning knobs.
type Speaker struct {
	DefaultThreshold float64 `mapstructure:"default_threshold" json:"default_threshold"`
	// UnknownPolicy is one of "restricted", "deny" or "allow".
	UnknownPolicy string `mapstructure:"unknown_policy" json:"unknown_policy"`
	// LowConfidenceMargin: when the best match lands within this distance of
	// the threshold, the command audio is embedded too and the two are averaged
	// before deciding.
	LowConfidenceMargin float64 `mapstructure:"low_confidence_margin" json:"low_confidence_margin"`
	// EnrollmentSamples is how many recordings the enrollment wizard collects.
	EnrollmentSamples int `mapstructure:"enrollment_samples" json:"enrollment_samples"`
	// MinSampleSimilarity rejects an enrollment sample that does not resemble
	// the ones already collected.
	MinSampleSimilarity float64 `mapstructure:"min_sample_similarity" json:"min_sample_similarity"`
}

// Unknown speaker policies.
const (
	PolicyRestricted = "restricted"
	PolicyDeny       = "deny"
	PolicyAllow      = "allow"
)

// Whisper API shapes.
const (
	WhisperAPIOpenAI = "openai"
	WhisperAPIASR    = "asr"
)

func setDefaults(v *viper.Viper) {
	v.SetDefault("listen", ":8080")
	v.SetDefault("db", "/var/lib/renfild/renfild.db")
	v.SetDefault("log_level", "info")
	v.SetDefault("audio_retention", "none")
	v.SetDefault("audio_dir", "/var/lib/renfild/audio")

	v.SetDefault("whisper.url", "http://127.0.0.1:9000")
	v.SetDefault("whisper.api", WhisperAPIOpenAI)
	v.SetDefault("whisper.model", "whisper-1")
	v.SetDefault("whisper.language", "auto")
	v.SetDefault("whisper.timeout", 20*time.Second)

	v.SetDefault("embedder.url", "http://127.0.0.1:8100")
	v.SetDefault("embedder.timeout", 15*time.Second)

	v.SetDefault("ollama.url", "http://127.0.0.1:11434")
	v.SetDefault("ollama.model", "qwen2.5:7b")
	v.SetDefault("ollama.timeout", 30*time.Second)
	v.SetDefault("ollama.max_words", 60)
	v.SetDefault("ollama.fallback", true)

	v.SetDefault("piper.binary", "/opt/renfild/piper/piper")
	v.SetDefault("piper.voice", "/opt/renfild/piper/voices/en_US-lessac-medium.onnx")
	v.SetDefault("piper.speaker_id", 0)
	v.SetDefault("piper.timeout", 10*time.Second)

	v.SetDefault("speaker.default_threshold", 0.45)
	v.SetDefault("speaker.unknown_policy", PolicyRestricted)
	v.SetDefault("speaker.low_confidence_margin", 0.05)
	v.SetDefault("speaker.enrollment_samples", 5)
	v.SetDefault("speaker.min_sample_similarity", 0.30)
}

// Load reads the configuration from the given file (optional), the environment
// and the supplied flag set, in increasing order of precedence.
func Load(file string, flags *pflag.FlagSet) (*Config, error) {
	v := viper.New()
	setDefaults(v)

	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if file != "" {
		v.SetConfigFile(file)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("reading config %s: %w", file, err)
		}
	}

	if flags != nil {
		if err := bindChangedFlags(v, flags); err != nil {
			return nil, err
		}
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("decoding config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// bindChangedFlags lets explicitly-set flags win over environment and file.
// Unset flags are left alone so their zero values do not clobber real config.
func bindChangedFlags(v *viper.Viper, flags *pflag.FlagSet) error {
	var bindErr error
	flags.Visit(func(f *pflag.Flag) {
		key, ok := flagToKey(f.Name)
		if !ok {
			return
		}
		if err := v.BindPFlag(key, f); err != nil && bindErr == nil {
			bindErr = fmt.Errorf("binding flag %s: %w", f.Name, err)
		}
	})
	return bindErr
}

// flagKeys maps CLI flag names onto configuration keys. Flags that do not
// appear here (--config, --help, --version) are not configuration.
var flagKeys = map[string]string{
	"listen":            "listen",
	"db":                "db",
	"log-level":         "log_level",
	"audio-retention":   "audio_retention",
	"audio-dir":         "audio_dir",
	"whisper-url":       "whisper.url",
	"whisper-api":       "whisper.api",
	"whisper-language":  "whisper.language",
	"embedder-url":      "embedder.url",
	"ollama-url":        "ollama.url",
	"ollama-model":      "ollama.model",
	"piper-binary":      "piper.binary",
	"piper-voice":       "piper.voice",
	"speaker-threshold": "speaker.default_threshold",
	"unknown-policy":    "speaker.unknown_policy",
}

// flagToKey maps a CLI flag name onto a configuration key.
func flagToKey(name string) (string, bool) {
	key, ok := flagKeys[name]
	return key, ok
}

// Validate checks the values that would otherwise fail deep inside a request.
func (c *Config) Validate() error {
	switch c.Speaker.UnknownPolicy {
	case PolicyRestricted, PolicyDeny, PolicyAllow:
	default:
		return fmt.Errorf("speaker.unknown_policy: unknown value %q", c.Speaker.UnknownPolicy)
	}
	switch c.Whisper.API {
	case WhisperAPIOpenAI, WhisperAPIASR:
	default:
		return fmt.Errorf("whisper.api: unknown value %q (want %q or %q)",
			c.Whisper.API, WhisperAPIOpenAI, WhisperAPIASR)
	}
	switch c.AudioRetention {
	case "none", "24h", "7d":
	default:
		return fmt.Errorf("audio_retention: unknown value %q (want none, 24h or 7d)", c.AudioRetention)
	}
	if c.Speaker.DefaultThreshold < -1 || c.Speaker.DefaultThreshold > 1 {
		return fmt.Errorf("speaker.default_threshold: %v is outside [-1, 1]", c.Speaker.DefaultThreshold)
	}
	if c.Listen == "" {
		return fmt.Errorf("listen: must not be empty")
	}
	if c.DB == "" {
		return fmt.Errorf("db: must not be empty")
	}
	return nil
}

// RetentionWindow converts audio_retention into a duration. Zero means audio is
// not stored at all.
func (c *Config) RetentionWindow() time.Duration {
	switch c.AudioRetention {
	case "24h":
		return 24 * time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	default:
		return 0
	}
}
