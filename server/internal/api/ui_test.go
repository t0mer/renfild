package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/t0mer/renfild/internal/intent"
	"github.com/t0mer/renfild/internal/speaker"
	"github.com/t0mer/renfild/internal/store"
)

func TestUpdateSpeaker(t *testing.T) {
	_, handler := newServer(t, "hello")
	handler.ServeHTTP(httptest.NewRecorder(),
		jsonRequest(http.MethodPost, "/api/ui/speakers", `{"name":"tomer","role":"owner"}`))

	updated := httptest.NewRecorder()
	handler.ServeHTTP(updated, jsonRequest(http.MethodPut, "/api/ui/speakers/1",
		`{"name":"Tomer","role":"member","threshold":0.62}`))
	if updated.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", updated.Code, updated.Body.String())
	}

	var record speaker.Record
	json.Unmarshal(updated.Body.Bytes(), &record)
	if record.Name != "Tomer" || record.Role != speaker.RoleMember {
		t.Fatalf("record = %+v", record)
	}
	if record.Threshold == nil || *record.Threshold != 0.62 {
		t.Fatalf("threshold = %v", record.Threshold)
	}
}

func TestSpeakerHandlersRejectBadInput(t *testing.T) {
	_, handler := newServer(t, "hello")

	tests := []struct {
		name    string
		request *http.Request
		want    int
	}{
		{"non-numeric id", httptest.NewRequest(http.MethodGet, "/api/ui/speakers/abc", nil), http.StatusBadRequest},
		{"missing speaker", httptest.NewRequest(http.MethodGet, "/api/ui/speakers/42", nil), http.StatusNotFound},
		{"nameless create", jsonRequest(http.MethodPost, "/api/ui/speakers", `{"name":"  ","role":"member"}`), http.StatusBadRequest},
		{"malformed json", jsonRequest(http.MethodPost, "/api/ui/speakers", `{`), http.StatusBadRequest},
		{"unknown field", jsonRequest(http.MethodPost, "/api/ui/speakers", `{"name":"x","nickname":"y"}`), http.StatusBadRequest},
		{"delete missing", httptest.NewRequest(http.MethodDelete, "/api/ui/speakers/42", nil), http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, tc.request)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, tc.want, recorder.Body.String())
			}
		})
	}
}

func TestEnrollmentListingAndDeletion(t *testing.T) {
	_, handler := newServer(t, "hello")
	handler.ServeHTTP(httptest.NewRecorder(),
		jsonRequest(http.MethodPost, "/api/ui/speakers", `{"name":"tomer","role":"owner"}`))
	handler.ServeHTTP(httptest.NewRecorder(), audioRequest(t, "/api/ui/speakers/1/enrollments", "wake-1"))

	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/ui/speakers/1/enrollments", nil))
	if listed.Code != http.StatusOK {
		t.Fatalf("status = %d", listed.Code)
	}

	var samples []enrollmentView
	json.Unmarshal(listed.Body.Bytes(), &samples)
	if len(samples) != 1 {
		t.Fatalf("listed %d samples", len(samples))
	}
	// A lone sample is its own centroid, so similarity is 1.
	if samples[0].Similarity < 0.99 {
		t.Fatalf("similarity = %v", samples[0].Similarity)
	}
	if samples[0].Label != "wake-1" {
		t.Fatalf("label = %q", samples[0].Label)
	}

	deleted := httptest.NewRecorder()
	handler.ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete, "/api/ui/speakers/1/enrollments/1", nil))
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", deleted.Code)
	}

	after := httptest.NewRecorder()
	handler.ServeHTTP(after, httptest.NewRequest(http.MethodGet, "/api/ui/speakers/1/enrollments", nil))
	var remaining []enrollmentView
	json.Unmarshal(after.Body.Bytes(), &remaining)
	if len(remaining) != 0 {
		t.Fatalf("%d samples survived", len(remaining))
	}
}

func TestEnrollmentRequiresAnExistingSpeaker(t *testing.T) {
	_, handler := newServer(t, "hello")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, audioRequest(t, "/api/ui/speakers/9/enrollments", "wake"))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestEnrollmentRejectsAnEmptyUpload(t *testing.T) {
	_, handler := newServer(t, "hello")
	handler.ServeHTTP(httptest.NewRecorder(),
		jsonRequest(http.MethodPost, "/api/ui/speakers", `{"name":"tomer","role":"owner"}`))

	request := httptest.NewRequest(http.MethodPost, "/api/ui/speakers/1/enrollments", strings.NewReader(""))
	request.Header.Set("Content-Type", "audio/wav")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestIntentUpdateDeleteAndReorder(t *testing.T) {
	_, handler := newServer(t, "hello")

	create := func(name string, priority int) int64 {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, jsonRequest(http.MethodPost, "/api/ui/intents", `{
			"name":"`+name+`","enabled":true,"match_type":"contains","patterns":["`+name+`"],
			"min_role":"member","handler":"reply","handler_config":{"template":"ok"},
			"priority":`+strconv.Itoa(priority)+`}`))
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create %s: status %d, body %s", name, recorder.Code, recorder.Body.String())
		}
		var rule intent.Rule
		json.Unmarshal(recorder.Body.Bytes(), &rule)
		return rule.ID
	}

	first := create("alpha", 10)
	second := create("beta", 20)

	updated := httptest.NewRecorder()
	handler.ServeHTTP(updated, jsonRequest(http.MethodPut, "/api/ui/intents/"+strconv.FormatInt(first, 10), `{
		"name":"alpha","enabled":false,"match_type":"contains","patterns":["alpha"],
		"min_role":"owner","handler":"reply","handler_config":{"template":"changed"},"priority":90}`))
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, body %s", updated.Code, updated.Body.String())
	}
	var rule intent.Rule
	json.Unmarshal(updated.Body.Bytes(), &rule)
	if rule.Enabled || rule.MinRole != speaker.RoleOwner {
		t.Fatalf("update did not apply: %+v", rule)
	}

	reordered := httptest.NewRecorder()
	handler.ServeHTTP(reordered, jsonRequest(http.MethodPost, "/api/ui/intents/reorder",
		`{"order":[`+strconv.FormatInt(second, 10)+`,`+strconv.FormatInt(first, 10)+`]}`))
	if reordered.Code != http.StatusOK {
		t.Fatalf("reorder status = %d, body %s", reordered.Code, reordered.Body.String())
	}
	var rules []intent.Rule
	json.Unmarshal(reordered.Body.Bytes(), &rules)
	// Only the ids passed in are renumbered; what matters is that beta now
	// comes before alpha.
	positions := map[int64]int{}
	for index, rule := range rules {
		positions[rule.ID] = index
	}
	if positions[second] > positions[first] {
		t.Fatalf("beta (%d) should now precede alpha (%d)", positions[second], positions[first])
	}

	deleted := httptest.NewRecorder()
	handler.ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete, "/api/ui/intents/"+strconv.FormatInt(first, 10), nil))
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", deleted.Code)
	}
}

func TestRenamingAnIntentOntoAnExistingNameConflicts(t *testing.T) {
	_, handler := newServer(t, "hello")
	// The seeded "greeting" intent already exists; renaming another onto it
	// must be reported as a conflict, not a server error.
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, jsonRequest(http.MethodPost, "/api/ui/intents", `{
		"name":"temporary","enabled":true,"match_type":"contains","patterns":["x"],
		"min_role":"member","handler":"reply","handler_config":{"template":"ok"},"priority":50}`))
	var rule intent.Rule
	json.Unmarshal(created.Body.Bytes(), &rule)

	conflict := httptest.NewRecorder()
	handler.ServeHTTP(conflict, jsonRequest(http.MethodPut, "/api/ui/intents/"+strconv.FormatInt(rule.ID, 10), `{
		"name":"greeting","enabled":true,"match_type":"contains","patterns":["x"],
		"min_role":"member","handler":"reply","handler_config":{"template":"ok"},"priority":50}`))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", conflict.Code, conflict.Body.String())
	}
}

func TestReorderRejectsAnEmptyOrder(t *testing.T) {
	_, handler := newServer(t, "hello")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, jsonRequest(http.MethodPost, "/api/ui/intents/reorder", `{"order":[]}`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestGetIntent(t *testing.T) {
	_, handler := newServer(t, "hello")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/ui/intents/1", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d — the seeded intents should be readable", recorder.Code)
	}

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/ui/intents/999", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", missing.Code)
	}
}

func TestIntentDryRunNeedsATranscript(t *testing.T) {
	_, handler := newServer(t, "hello")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, jsonRequest(http.MethodPost, "/api/ui/test/intent", `{"transcript":"  "}`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestIntentDryRunTreatsABlankSpeakerAsUnknown(t *testing.T) {
	_, handler := newServer(t, "hello")
	handler.ServeHTTP(httptest.NewRecorder(), jsonRequest(http.MethodPost, "/api/ui/intents", `{
		"name":"owner-only","enabled":true,"match_type":"contains","patterns":["unlock"],
		"min_role":"owner","handler":"reply","handler_config":{"template":"unlocked"},"priority":1}`))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, jsonRequest(http.MethodPost, "/api/ui/test/intent",
		`{"transcript":"unlock the door","speaker":"","role":"owner"}`))

	var result testIntentResponse
	json.Unmarshal(recorder.Body.Bytes(), &result)
	if result.Result.Allowed {
		t.Fatalf("an unidentified voice was allowed: %+v", result)
	}
}

func TestTestTTSReturnsAudio(t *testing.T) {
	_, handler := newServer(t, "hello")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, jsonRequest(http.MethodPost, "/api/ui/test/tts", `{"text":"testing"}`))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if recorder.Header().Get("Content-Type") != "audio/wav" {
		t.Fatalf("content type = %q", recorder.Header().Get("Content-Type"))
	}
	if recorder.Body.Len() == 0 {
		t.Fatal("no audio returned")
	}
}

func TestStatsEndpoint(t *testing.T) {
	_, handler := newServer(t, "hello there")
	handler.ServeHTTP(httptest.NewRecorder(), utteranceRequest(t))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/ui/stats", nil))

	var stats store.Stats
	json.Unmarshal(recorder.Body.Bytes(), &stats)
	if stats.Total != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestHistorySearchAndPaging(t *testing.T) {
	server, handler := newServer(t, "hello")
	for _, transcript := range []string{"turn on the lights", "what time is it"} {
		server.Store.InsertUtterance(t.Context(), store.Utterance{
			TS: time.Now(), Speaker: "tomer", Transcript: transcript, Intent: "x", Allowed: true,
		})
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/ui/history?q=lights&limit=5", nil))

	var page historyResponse
	json.Unmarshal(recorder.Body.Bytes(), &page)
	if page.Total != 1 || page.Items[0].Transcript != "turn on the lights" {
		t.Fatalf("search returned %+v", page)
	}
	if page.Limit != 5 {
		t.Fatalf("limit = %d", page.Limit)
	}
}

func TestEventHubDeliversAndUnsubscribes(t *testing.T) {
	hub := NewEventHub()
	events, unsubscribe := hub.Subscribe()

	hub.Publish(store.Utterance{ID: 1, Transcript: "hello"})
	select {
	case got := <-events:
		if got.Transcript != "hello" {
			t.Fatalf("got %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no event delivered")
	}

	unsubscribe()
	// Publishing after an unsubscribe must not panic on a closed channel.
	hub.Publish(store.Utterance{ID: 2})
	if _, open := <-events; open {
		t.Fatal("channel should be closed after unsubscribing")
	}
	// A second unsubscribe is harmless.
	unsubscribe()
}

func TestEventHubDropsEventsForASlowSubscriber(t *testing.T) {
	hub := NewEventHub()
	_, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	// Far more than the buffer: Publish must never block the pipeline.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			hub.Publish(store.Utterance{ID: int64(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
}

func TestUtteranceStreamSendsEvents(t *testing.T) {
	server, handler := newServer(t, "hello there")

	request := httptest.NewRequest(http.MethodGet, "/api/ui/events", nil)
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	request = request.WithContext(ctx)

	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(recorder, request)
		close(done)
	}()

	// Give the handler a moment to subscribe, then publish.
	waitFor(t, func() bool { return server.Events.subscriberCount() > 0 })
	server.Events.Publish(store.Utterance{ID: 1, Transcript: "hello there"})
	waitFor(t, func() bool { return strings.Contains(recorder.Body.String(), "hello there") })

	cancel()
	<-done
	if !strings.Contains(recorder.Body.String(), "event: utterance") {
		t.Fatalf("stream body = %q", recorder.Body.String())
	}
}
