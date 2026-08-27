package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/t0mer/renfild/internal/api"
	"github.com/t0mer/renfild/internal/clients"
	"github.com/t0mer/renfild/internal/config"
	"github.com/t0mer/renfild/internal/intent"
	"github.com/t0mer/renfild/internal/metrics"
	"github.com/t0mer/renfild/internal/pipeline"
	"github.com/t0mer/renfild/internal/store"
	"github.com/t0mer/renfild/internal/version"
)

func newServeCmd() *cobra.Command {
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run the Renfild server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd)
		},
	}
	addServeFlags(serve.Flags())
	return serve
}

// addServeFlags declares the flags that override configuration keys. Only flags
// the user actually sets take effect, so the config file is not clobbered by
// unset defaults.
func addServeFlags(flags *pflag.FlagSet) {
	flags.String("listen", ":8080", "address to listen on")
	flags.String("db", "", "path to the SQLite database")
	flags.String("log-level", "", "debug, info, warn or error")
	flags.String("audio-retention", "", "none, 24h or 7d")
	flags.String("audio-dir", "", "directory for retained audio")
	flags.String("whisper-url", "", "Whisper endpoint base URL")
	flags.String("whisper-api", "", "whisper API shape: openai or asr")
	flags.String("whisper-language", "", "language hint: he, en or auto")
	flags.String("embedder-url", "", "embedder sidecar base URL")
	flags.String("ollama-url", "", "Ollama base URL")
	flags.String("ollama-model", "", "Ollama model name")
	flags.String("piper-binary", "", "path to the piper binary")
	flags.String("piper-voice", "", "path to the piper voice model")
	flags.Bool("piper-persistent", true, "keep one piper process alive between replies")
	flags.Float64("speaker-threshold", 0, "cosine similarity floor for speaker matching")
	flags.String("unknown-policy", "", "restricted, deny or allow")
}

func runServe(cmd *cobra.Command) error {
	cfg, err := config.Load(configFile, cmd.Flags())
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)
	log.Info("starting renfild", "version", version.Version, "listen", cfg.Listen)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DB)
	if err != nil {
		return err
	}
	defer db.Close()
	log.Info("database ready", "path", cfg.DB)

	runtime := config.NewRuntime(cfg)
	var persisted config.RuntimeSettings
	if found, err := db.GetSetting(ctx, api.RuntimeSettingsKey, &persisted); err != nil {
		log.Warn("reading stored settings failed", "error", err)
	} else if found {
		if err := runtime.Set(persisted); err != nil {
			log.Warn("stored settings are invalid, using config defaults", "error", err)
		} else {
			log.Info("applied stored runtime settings")
		}
	}

	whisper := clients.NewWhisper(cfg.Whisper.URL, cfg.Whisper.API, cfg.Whisper.Model,
		cfg.Whisper.Language, cfg.Whisper.APIKey, cfg.Whisper.Timeout)
	embedder := clients.NewEmbedder(cfg.Embedder.URL, cfg.Embedder.Timeout)
	ollama := clients.NewOllama(cfg.Ollama.URL, cfg.Ollama.Model, cfg.Ollama.Timeout)
	piper := clients.NewPiper(clients.PiperOptions{
		Binary:     cfg.Piper.Binary,
		Voice:      cfg.Piper.Voice,
		SpeakerID:  cfg.Piper.SpeakerID,
		Timeout:    cfg.Piper.Timeout,
		ExtraArgs:  cfg.Piper.ExtraArgs,
		Persistent: cfg.Piper.Persistent,
	})
	defer piper.Close()
	if err := piper.Available(); err != nil {
		// Not fatal: the server is still useful for enrollment and the UI, and
		// the operator gets a clear line in the log about what is missing.
		log.Warn("text to speech is unavailable", "error", err)
	} else if err := piper.Warm(); err != nil {
		log.Warn("starting the piper process failed", "error", err)
	} else if cfg.Piper.Persistent {
		log.Info("text to speech ready", "voice", cfg.Piper.Voice, "persistent", true)
	}

	systemPrompt, err := loadSystemPrompt(cfg.Ollama.SystemPromptFile)
	if err != nil {
		return err
	}

	handlers := map[string]intent.Handler{
		intent.HandlerReply:   intent.ReplyHandler{},
		intent.HandlerWebhook: intent.WebhookHandler{Client: &http.Client{Timeout: 10 * time.Second}},
		intent.HandlerLLM: intent.LLMHandler{
			Client:       ollama,
			SystemPrompt: systemPrompt,
			MaxWords:     cfg.Ollama.MaxWords,
		},
	}
	router := intent.New(db, handlers, func() bool { return runtime.Get().LLMFallback }, log)

	collectors := metrics.New()
	events := api.NewEventHub()

	processor := &pipeline.Pipeline{
		Store:    db,
		Embedder: embedder,
		Whisper:  whisper,
		Router:   router,
		TTS:      piper,
		Config:   cfg,
		Runtime:  runtime,
		Log:      log,
		Metrics:  collectors,
		Observer: events.Publish,
	}

	server := &api.Server{
		Store:    db,
		Pipeline: processor,
		Config:   cfg,
		Runtime:  runtime,
		Metrics:  collectors,
		Log:      log,
		Events:   events,
		Embedder: embedder,
		TTS:      piper,
	}

	if window := cfg.RetentionWindow(); window > 0 {
		go pruneAudio(ctx, db, window, log)
	}

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// Utterances carry audio uploads and wait on Whisper, so the write
		// timeout has to be generous.
		WriteTimeout: 90 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", cfg.Listen)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down http server: %w", err)
	}
	return nil
}

// pruneAudio deletes retained recordings once they age out.
func pruneAudio(ctx context.Context, db *store.Store, window time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			paths, err := db.PruneAudio(ctx, time.Now().Add(-window))
			if err != nil {
				log.Warn("pruning audio failed", "error", err)
				continue
			}
			for _, path := range paths {
				// Remove the command recording and its wake companion.
				for _, candidate := range []string{path, strings.Replace(path, "-command.wav", "-wake.wav", 1)} {
					if err := os.Remove(candidate); err != nil && !os.IsNotExist(err) {
						log.Warn("removing expired audio failed", "path", candidate, "error", err)
					}
				}
			}
			if len(paths) > 0 {
				log.Info("pruned expired audio", "count", len(paths))
			}
		}
	}
}

func loadSystemPrompt(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return intent.DefaultLLMSystem, nil
	}
	body, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("reading system prompt %s: %w", path, err)
	}
	return strings.TrimSpace(string(body)), nil
}

func newLogger(level string) *slog.Logger {
	var parsed slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		parsed = slog.LevelDebug
	case "warn", "warning":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	default:
		parsed = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: parsed}))
}
