// Package pipeline orchestrates one utterance: identify the speaker, transcribe
// the command, route it to an intent, speak the answer, and record everything.
//
// No stage is allowed to fail the request outright — the satellite always gets
// either audio or a 204, never a 500.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/t0mer/renfild/internal/clients"
	"github.com/t0mer/renfild/internal/config"
	"github.com/t0mer/renfild/internal/intent"
	"github.com/t0mer/renfild/internal/metrics"
	"github.com/t0mer/renfild/internal/speaker"
	"github.com/t0mer/renfild/internal/store"
)

// ErrorReply is spoken when a stage fails and there is nothing better to say.
const ErrorReply = "Sorry, something went wrong."

// Embedder produces speaker vectors from audio.
type Embedder interface {
	Embed(ctx context.Context, wav []byte) (speaker.Vector, float64, error)
}

// Transcriber turns command audio into text.
type Transcriber interface {
	Transcribe(ctx context.Context, wav []byte) (string, error)
}

// Store is the slice of the database the pipeline needs.
type Store interface {
	ListSpeakers(ctx context.Context) ([]speaker.Record, error)
	InsertUtterance(ctx context.Context, u store.Utterance) (int64, error)
}

// Pipeline wires the stages together.
type Pipeline struct {
	Store    Store
	Embedder Embedder
	Whisper  Transcriber
	Router   *intent.Router
	TTS      clients.TTS
	Config   *config.Config
	Runtime  *config.Runtime
	Log      *slog.Logger
	Metrics  *metrics.Metrics

	// Observer, when set, is called with every completed utterance. The web UI
	// dashboard subscribes through it.
	Observer func(store.Utterance)
}

// Request is one utterance submitted by a satellite.
type Request struct {
	SatelliteID string
	Wake        []byte
	Command     []byte
	Now         time.Time
}

// Response is what the satellite gets back.
type Response struct {
	Audio      []byte
	Speaker    string
	Transcript string
	Intent     string
	Reply      string
	Confidence float64
	Allowed    bool
	Utterance  store.Utterance
}

// Process runs the whole pipeline. The returned error is for logging only: the
// response is always usable.
func (p *Pipeline) Process(ctx context.Context, req Request) (Response, error) {
	started := time.Now()
	if req.Now.IsZero() {
		req.Now = started
	}
	log := p.logger().With("satellite", req.SatelliteID)
	tuning := p.Runtime.Get()

	// Speaker identification and transcription are independent; run them at the
	// same time so the round trip is bounded by the slower of the two.
	var (
		wg         sync.WaitGroup
		identity   speaker.Identity
		wakeVector speaker.Vector
		records    []speaker.Record
		spkErr     error
		transcript string
		sttErr     error
		spkMs      int
		sttMs      int
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		start := time.Now()
		identity, wakeVector, records, spkErr = p.identify(ctx, req.Wake, tuning.SpeakerThreshold)
		spkMs = int(time.Since(start).Milliseconds())
		p.Metrics.ObserveStage("speakerid", time.Since(start).Seconds())
		if spkErr != nil {
			p.Metrics.StageFailed("speakerid")
		}
	}()
	go func() {
		defer wg.Done()
		start := time.Now()
		transcript, sttErr = p.Whisper.Transcribe(ctx, req.Command)
		sttMs = int(time.Since(start).Milliseconds())
		p.Metrics.ObserveStage("stt", time.Since(start).Seconds())
		if sttErr != nil {
			p.Metrics.StageFailed("stt")
		}
	}()
	wg.Wait()

	if spkErr != nil {
		log.Warn("speaker identification failed", "error", spkErr)
	}

	// A borderline wake-audio match gets a second opinion from the command
	// audio before we commit to an identity.
	if spkErr == nil && speaker.Borderline(identity, tuning.LowConfidenceMargin) {
		if refined, ok := p.secondOpinion(ctx, req.Command, wakeVector, records, tuning.SpeakerThreshold); ok {
			log.Info("borderline match refined",
				"from", identity.Name, "from_score", identity.Score,
				"to", refined.Name, "to_score", refined.Score)
			identity = refined
		}
	}

	resolved, permitted := speaker.Apply(tuning.UnknownPolicy, identity)

	utterance := store.Utterance{
		TS:            req.Now.UTC(),
		SatelliteID:   req.SatelliteID,
		Speaker:       resolved.Name,
		Confidence:    resolved.Score,
		RunnerUp:      resolved.RunnerUp,
		RunnerUpScore: resolved.RunnerUpScore,
		Transcript:    transcript,
		Allowed:       true,
		LatencyMsSpk:  spkMs,
		LatencyMsSTT:  sttMs,
	}

	response := Response{
		Speaker:    resolved.Name,
		Transcript: transcript,
		Confidence: resolved.Score,
		Allowed:    true,
	}

	switch {
	case sttErr != nil:
		utterance.Error = fmt.Sprintf("stt: %v", sttErr)
		utterance.Intent = "error"
		utterance.Reply = ErrorReply
		log.Error("transcription failed", "error", sttErr)
	case transcript == "":
		utterance.Intent = "empty"
		utterance.Reply = ""
		log.Info("empty transcript — nothing to route")
	case !permitted:
		// unknown_policy: deny — the assistant stays silent for strangers.
		utterance.Intent = "denied"
		utterance.Allowed = false
		utterance.Reply = ""
		response.Allowed = false
		log.Info("unknown speaker denied by policy")
	default:
		start := time.Now()
		result, err := p.Router.Route(ctx, intent.Request{
			Speaker:     resolved.Name,
			Role:        resolved.Role,
			Known:       resolved.Known,
			Confidence:  resolved.Score,
			Transcript:  transcript,
			SatelliteID: req.SatelliteID,
			Now:         req.Now,
		})
		utterance.LatencyMsIntent = int(time.Since(start).Milliseconds())
		p.Metrics.ObserveStage("intent", time.Since(start).Seconds())
		if err != nil {
			p.Metrics.StageFailed("intent")
			log.Error("intent routing failed", "intent", result.Intent, "error", err)
			utterance.Error = fmt.Sprintf("intent: %v", err)
			utterance.Intent = orDefault(result.Intent, "error")
			utterance.Reply = ErrorReply
		} else {
			utterance.Intent = result.Intent
			utterance.Reply = result.Reply
			utterance.Allowed = result.Allowed
			response.Allowed = result.Allowed
		}
	}

	response.Intent = utterance.Intent
	response.Reply = utterance.Reply

	// Speak, unless there is nothing to say.
	if utterance.Reply != "" {
		start := time.Now()
		audio, err := p.TTS.Synthesize(ctx, utterance.Reply)
		utterance.LatencyMsTTS = int(time.Since(start).Milliseconds())
		p.Metrics.ObserveStage("tts", time.Since(start).Seconds())
		if err != nil {
			p.Metrics.StageFailed("tts")
			log.Error("synthesis failed", "error", err)
			utterance.Error = appendError(utterance.Error, fmt.Sprintf("tts: %v", err))
		} else {
			response.Audio = audio
		}
	}

	if path, err := p.retainAudio(req); err != nil {
		log.Warn("storing audio failed", "error", err)
	} else if path != "" {
		utterance.AudioPath = path
	}

	utterance.LatencyMsTotal = int(time.Since(started).Milliseconds())

	id, err := p.Store.InsertUtterance(ctx, utterance)
	if err != nil {
		log.Error("recording utterance failed", "error", err)
	} else {
		utterance.ID = id
	}
	response.Utterance = utterance

	p.Metrics.ObserveUtterance(
		utterance.Speaker, orDefault(utterance.Intent, "none"), outcome(utterance),
		time.Since(started).Seconds(), utterance.Confidence)

	if p.Observer != nil {
		p.Observer(utterance)
	}

	log.Info("utterance handled",
		"speaker", utterance.Speaker,
		"confidence", fmt.Sprintf("%.3f", utterance.Confidence),
		"runner_up", utterance.RunnerUp,
		"runner_up_score", fmt.Sprintf("%.3f", utterance.RunnerUpScore),
		"intent", utterance.Intent,
		"transcript", utterance.Transcript,
		"spk_ms", utterance.LatencyMsSpk,
		"stt_ms", utterance.LatencyMsSTT,
		"intent_ms", utterance.LatencyMsIntent,
		"tts_ms", utterance.LatencyMsTTS,
		"total_ms", utterance.LatencyMsTotal)

	if utterance.Error != "" {
		return response, fmt.Errorf("%s", utterance.Error)
	}
	return response, nil
}

// identify embeds the wake snapshot and matches it against enrolled speakers.
func (p *Pipeline) identify(ctx context.Context, wake []byte, threshold float64) (speaker.Identity, speaker.Vector, []speaker.Record, error) {
	records, err := p.Store.ListSpeakers(ctx)
	if err != nil {
		return speaker.UnknownIdentity(), nil, nil, fmt.Errorf("loading speakers: %w", err)
	}
	if len(records) == 0 {
		return speaker.UnknownIdentity(), nil, records, nil
	}
	if len(wake) == 0 {
		return speaker.UnknownIdentity(), nil, records, fmt.Errorf("no wake audio submitted")
	}
	vector, _, err := p.Embedder.Embed(ctx, wake)
	if err != nil {
		return speaker.UnknownIdentity(), nil, records, fmt.Errorf("embedding wake audio: %w", err)
	}
	return speaker.Match(vector, records, threshold), vector, records, nil
}

// secondOpinion averages the wake and command embeddings and re-matches.
func (p *Pipeline) secondOpinion(ctx context.Context, command []byte, wakeVector speaker.Vector, records []speaker.Record, threshold float64) (speaker.Identity, bool) {
	if len(command) == 0 || len(wakeVector) == 0 || len(records) == 0 {
		return speaker.Identity{}, false
	}
	start := time.Now()
	commandVector, _, err := p.Embedder.Embed(ctx, command)
	p.Metrics.ObserveStage("speakerid_second", time.Since(start).Seconds())
	if err != nil {
		p.logger().Warn("second-opinion embedding failed", "error", err)
		return speaker.Identity{}, false
	}
	averaged := speaker.Mean(wakeVector, commandVector)
	return speaker.Match(averaged, records, threshold), true
}

// retainAudio writes the submitted audio to disk when retention is enabled.
func (p *Pipeline) retainAudio(req Request) (string, error) {
	if p.Config.RetentionWindow() == 0 {
		return "", nil
	}
	dir := filepath.Join(p.Config.AudioDir, time.Now().UTC().Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("creating audio directory: %w", err)
	}
	stamp := time.Now().UTC().Format("150405.000")
	base := filepath.Join(dir, stamp)
	if err := os.WriteFile(base+"-command.wav", req.Command, 0o640); err != nil {
		return "", fmt.Errorf("writing command audio: %w", err)
	}
	if len(req.Wake) > 0 {
		if err := os.WriteFile(base+"-wake.wav", req.Wake, 0o640); err != nil {
			return "", fmt.Errorf("writing wake audio: %w", err)
		}
	}
	return base + "-command.wav", nil
}

func (p *Pipeline) logger() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

func outcome(u store.Utterance) string {
	switch {
	case u.Error != "":
		return "error"
	case !u.Allowed:
		return "denied"
	default:
		return "ok"
	}
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func appendError(existing, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + "; " + addition
}
