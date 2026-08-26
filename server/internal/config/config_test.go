package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

func TestDefaults(t *testing.T) {
	cfg, err := Load("", nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Listen != ":8080" {
		t.Fatalf("listen = %q", cfg.Listen)
	}
	if cfg.Speaker.DefaultThreshold != 0.45 {
		t.Fatalf("default threshold = %v, want 0.45", cfg.Speaker.DefaultThreshold)
	}
	if cfg.Speaker.UnknownPolicy != PolicyRestricted {
		t.Fatalf("unknown policy = %q", cfg.Speaker.UnknownPolicy)
	}
	if cfg.Ollama.MaxWords != 60 {
		t.Fatalf("max words = %d, want 60", cfg.Ollama.MaxWords)
	}
	if cfg.Piper.Timeout != 10*time.Second {
		t.Fatalf("piper timeout = %v", cfg.Piper.Timeout)
	}
}

func TestFileOverridesDefaults(t *testing.T) {
	path := writeConfig(t, `
listen: ":9999"
whisper:
  url: "http://gandalf:9000"
  language: "he"
speaker:
  default_threshold: 0.55
`)
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Listen != ":9999" || cfg.Whisper.URL != "http://gandalf:9000" || cfg.Whisper.Language != "he" {
		t.Fatalf("config file was not applied: %+v", cfg)
	}
	if cfg.Speaker.DefaultThreshold != 0.55 {
		t.Fatalf("threshold = %v, want 0.55", cfg.Speaker.DefaultThreshold)
	}
	// Untouched keys keep their defaults.
	if cfg.Embedder.URL != "http://127.0.0.1:8100" {
		t.Fatalf("embedder url = %q", cfg.Embedder.URL)
	}
}

func TestEnvironmentOverridesFile(t *testing.T) {
	path := writeConfig(t, "listen: \":9999\"\n")
	t.Setenv("RENFILD_LISTEN", ":7777")
	t.Setenv("RENFILD_WHISPER_URL", "http://env:9000")

	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Listen != ":7777" {
		t.Fatalf("listen = %q, want the environment value", cfg.Listen)
	}
	if cfg.Whisper.URL != "http://env:9000" {
		t.Fatalf("whisper url = %q", cfg.Whisper.URL)
	}
}

func TestFlagsOverrideEverything(t *testing.T) {
	path := writeConfig(t, "listen: \":9999\"\n")
	t.Setenv("RENFILD_LISTEN", ":7777")

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("listen", ":8080", "")
	flags.String("unknown-policy", "", "")
	if err := flags.Parse([]string{"--listen", ":6666", "--unknown-policy", "deny"}); err != nil {
		t.Fatalf("parsing flags: %v", err)
	}

	cfg, err := Load(path, flags)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Listen != ":6666" {
		t.Fatalf("listen = %q, want the flag value", cfg.Listen)
	}
	if cfg.Speaker.UnknownPolicy != PolicyDeny {
		t.Fatalf("unknown policy = %q", cfg.Speaker.UnknownPolicy)
	}
}

func TestUnsetFlagsDoNotClobberTheFile(t *testing.T) {
	path := writeConfig(t, "listen: \":9999\"\n")

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("listen", ":8080", "")
	if err := flags.Parse(nil); err != nil {
		t.Fatalf("parsing flags: %v", err)
	}

	cfg, err := Load(path, flags)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Listen != ":9999" {
		t.Fatalf("listen = %q — an unset flag overwrote the config file", cfg.Listen)
	}
}

func TestMissingConfigFileIsAnError(t *testing.T) {
	if _, err := Load("/nonexistent/renfild.yaml", nil); err == nil {
		t.Fatal("expected an error for a missing config file")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"defaults are valid", func(*Config) {}, false},
		{"bad unknown policy", func(c *Config) { c.Speaker.UnknownPolicy = "maybe" }, true},
		{"bad whisper api", func(c *Config) { c.Whisper.API = "grpc" }, true},
		{"bad retention", func(c *Config) { c.AudioRetention = "forever" }, true},
		{"threshold out of range", func(c *Config) { c.Speaker.DefaultThreshold = 2 }, true},
		{"empty listen", func(c *Config) { c.Listen = "" }, true},
		{"empty db", func(c *Config) { c.DB = "" }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load("", nil)
			if err != nil {
				t.Fatalf("Load() error: %v", err)
			}
			tc.mutate(cfg)
			if err := cfg.Validate(); tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestRetentionWindow(t *testing.T) {
	tests := map[string]time.Duration{
		"none": 0,
		"24h":  24 * time.Hour,
		"7d":   7 * 24 * time.Hour,
	}
	for value, want := range tests {
		cfg := &Config{AudioRetention: value}
		if got := cfg.RetentionWindow(); got != want {
			t.Fatalf("RetentionWindow(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestRuntimeSettings(t *testing.T) {
	cfg, _ := Load("", nil)
	runtime := NewRuntime(cfg)

	if got := runtime.Get(); got.SpeakerThreshold != 0.45 || got.UnknownPolicy != PolicyRestricted {
		t.Fatalf("runtime seeded as %+v", got)
	}

	next := runtime.Get()
	next.SpeakerThreshold = 0.6
	next.UnknownPolicy = PolicyAllow
	if err := runtime.Set(next); err != nil {
		t.Fatalf("Set() error: %v", err)
	}
	if runtime.Get().SpeakerThreshold != 0.6 {
		t.Fatal("Set() did not apply")
	}

	invalid := runtime.Get()
	invalid.UnknownPolicy = "maybe"
	if err := runtime.Set(invalid); err == nil {
		t.Fatal("expected an invalid policy to be rejected")
	}
	if runtime.Get().UnknownPolicy != PolicyAllow {
		t.Fatal("a rejected update must not change the live settings")
	}
}
