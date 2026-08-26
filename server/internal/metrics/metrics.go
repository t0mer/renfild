// Package metrics holds the Prometheus collectors the server exposes on
// /metrics. They are grouped in one struct so tests can build an isolated
// registry instead of touching the global one.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics is the server's collector set.
type Metrics struct {
	Registry *prometheus.Registry

	Utterances      *prometheus.CounterVec
	StageLatency    *prometheus.HistogramVec
	TotalLatency    prometheus.Histogram
	MatchConfidence prometheus.Histogram
	StageErrors     *prometheus.CounterVec
}

// New builds the collectors and registers them on a fresh registry.
func New() *Metrics {
	registry := prometheus.NewRegistry()
	m := &Metrics{
		Registry: registry,
		Utterances: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "renfild_utterances_total",
			Help: "Utterances handled, labelled by speaker, intent and outcome.",
		}, []string{"speaker", "intent", "outcome"}),
		StageLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "renfild_stage_duration_seconds",
			Help:    "Per-stage pipeline latency.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8, 15},
		}, []string{"stage"}),
		TotalLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "renfild_utterance_duration_seconds",
			Help:    "End-to-end latency from received audio to spoken reply.",
			Buckets: []float64{0.25, 0.5, 1, 1.5, 2, 2.5, 3, 5, 10, 20},
		}),
		MatchConfidence: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "renfild_speaker_match_confidence",
			Help:    "Cosine similarity of the best speaker match.",
			Buckets: []float64{0, 0.1, 0.2, 0.3, 0.35, 0.4, 0.45, 0.5, 0.55, 0.6, 0.7, 0.8, 0.9},
		}),
		StageErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "renfild_stage_errors_total",
			Help: "Pipeline stage failures.",
		}, []string{"stage"}),
	}

	registry.MustRegister(
		m.Utterances, m.StageLatency, m.TotalLatency, m.MatchConfidence, m.StageErrors,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// ObserveStage records the duration of a pipeline stage in seconds.
func (m *Metrics) ObserveStage(stage string, seconds float64) {
	if m == nil {
		return
	}
	m.StageLatency.WithLabelValues(stage).Observe(seconds)
}

// StageFailed counts a stage error.
func (m *Metrics) StageFailed(stage string) {
	if m == nil {
		return
	}
	m.StageErrors.WithLabelValues(stage).Inc()
}

// ObserveUtterance records one completed utterance.
func (m *Metrics) ObserveUtterance(speaker, intent, outcome string, seconds, confidence float64) {
	if m == nil {
		return
	}
	m.Utterances.WithLabelValues(speaker, intent, outcome).Inc()
	m.TotalLatency.Observe(seconds)
	m.MatchConfidence.Observe(confidence)
}
