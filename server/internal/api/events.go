package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/t0mer/renfild/internal/store"
)

// EventHub fans utterances out to connected dashboards over SSE.
//
// Subscribers have a small buffered channel each: a browser that stops reading
// gets its events dropped rather than blocking the pipeline.
type EventHub struct {
	mu          sync.RWMutex
	subscribers map[chan store.Utterance]struct{}
}

// NewEventHub creates an empty hub.
func NewEventHub() *EventHub {
	return &EventHub{subscribers: map[chan store.Utterance]struct{}{}}
}

// Subscribe registers a listener and returns it with its unsubscribe function.
func (h *EventHub) Subscribe() (<-chan store.Utterance, func()) {
	channel := make(chan store.Utterance, 8)
	h.mu.Lock()
	h.subscribers[channel] = struct{}{}
	h.mu.Unlock()

	return channel, func() {
		h.mu.Lock()
		if _, ok := h.subscribers[channel]; ok {
			delete(h.subscribers, channel)
			close(channel)
		}
		h.mu.Unlock()
	}
}

// Publish delivers an utterance to every subscriber, never blocking.
func (h *EventHub) Publish(u store.Utterance) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for channel := range h.subscribers {
		select {
		case channel <- u:
		default:
		}
	}
}

// handleEvents streams utterances to the dashboard as Server-Sent Events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events, unsubscribe := s.Events.Subscribe()
	defer unsubscribe()

	// A periodic comment keeps proxies and browsers from closing an idle stream.
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case utterance, ok := <-events:
			if !ok {
				return
			}
			payload, err := json.Marshal(utterance)
			if err != nil {
				continue
			}
			if _, err := w.Write([]byte("event: utterance\ndata: " + string(payload) + "\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// subscriberCount reports how many dashboards are attached. Used by tests.
func (h *EventHub) subscriberCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers)
}
