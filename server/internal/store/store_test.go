package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/t0mer/renfild/internal/intent"
	"github.com/t0mer/renfild/internal/speaker"
)

func newStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := OpenMemory(ctx)
	if err != nil {
		t.Fatalf("OpenMemory() error: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, ctx
}

func vector(values ...float32) speaker.Vector {
	v := make(speaker.Vector, speaker.Dims)
	copy(v, values)
	return speaker.Vector(v).Normalize()
}

func TestMigrationsRunAndSeedIntents(t *testing.T) {
	db, ctx := newStore(t)
	rules, err := db.Rules(ctx)
	if err != nil {
		t.Fatalf("Rules() error: %v", err)
	}
	if len(rules) == 0 {
		t.Fatal("expected the seeded intents to be present")
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	db, ctx := newStore(t)
	if err := db.migrate(ctx); err != nil {
		t.Fatalf("second migrate() error: %v", err)
	}
	rules, _ := db.ListIntents(ctx)
	names := map[string]int{}
	for _, rule := range rules {
		names[rule.Name]++
	}
	for name, count := range names {
		if count > 1 {
			t.Fatalf("intent %q was seeded %d times", name, count)
		}
	}
}

func TestSpeakerCRUD(t *testing.T) {
	db, ctx := newStore(t)

	id, err := db.CreateSpeaker(ctx, "tomer", speaker.RoleOwner, nil)
	if err != nil {
		t.Fatalf("CreateSpeaker() error: %v", err)
	}

	record, err := db.GetSpeaker(ctx, id)
	if err != nil {
		t.Fatalf("GetSpeaker() error: %v", err)
	}
	if record.Name != "tomer" || record.Role != speaker.RoleOwner || record.Samples != 0 {
		t.Fatalf("unexpected record %+v", record)
	}

	threshold := 0.6
	if err := db.UpdateSpeaker(ctx, id, "Tomer", speaker.RoleMember, &threshold); err != nil {
		t.Fatalf("UpdateSpeaker() error: %v", err)
	}
	record, _ = db.GetSpeaker(ctx, id)
	if record.Name != "Tomer" || record.Role != speaker.RoleMember || record.Threshold == nil || *record.Threshold != 0.6 {
		t.Fatalf("update did not stick: %+v", record)
	}

	if err := db.DeleteSpeaker(ctx, id); err != nil {
		t.Fatalf("DeleteSpeaker() error: %v", err)
	}
	if _, err := db.GetSpeaker(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSpeaker() after delete = %v, want ErrNotFound", err)
	}
}

func TestDuplicateSpeakerNameIsRejected(t *testing.T) {
	db, ctx := newStore(t)
	if _, err := db.CreateSpeaker(ctx, "dana", speaker.RoleMember, nil); err != nil {
		t.Fatalf("first CreateSpeaker() error: %v", err)
	}
	if _, err := db.CreateSpeaker(ctx, "dana", speaker.RoleMember, nil); err == nil {
		t.Fatal("expected the unique name constraint to fire")
	}
}

func TestEnrollmentUpdatesTheCentroid(t *testing.T) {
	db, ctx := newStore(t)
	id, _ := db.CreateSpeaker(ctx, "tomer", speaker.RoleOwner, nil)

	first := vector(1, 0, 0)
	second := vector(0.9, 0.1, 0)
	if _, err := db.AddEnrollment(ctx, id, first, 2.5, "wake-1"); err != nil {
		t.Fatalf("AddEnrollment() error: %v", err)
	}
	if _, err := db.AddEnrollment(ctx, id, second, 3.0, "wake-2"); err != nil {
		t.Fatalf("AddEnrollment() error: %v", err)
	}

	record, _ := db.GetSpeaker(ctx, id)
	if record.Samples != 2 {
		t.Fatalf("samples = %d, want 2", record.Samples)
	}
	expected, _ := speaker.Centroid([]speaker.Vector{first, second})
	if got := speaker.Cosine(record.Centroid, expected); math.Abs(got-1) > 1e-6 {
		t.Fatalf("stored centroid differs from the recomputed one (similarity %v)", got)
	}
}

func TestDeletingAnEnrollmentRecomputesTheCentroid(t *testing.T) {
	db, ctx := newStore(t)
	id, _ := db.CreateSpeaker(ctx, "tomer", speaker.RoleOwner, nil)

	good := vector(1, 0, 0)
	bad := vector(0, 1, 0)
	if _, err := db.AddEnrollment(ctx, id, good, 2, "good"); err != nil {
		t.Fatal(err)
	}
	badID, err := db.AddEnrollment(ctx, id, bad, 2, "bad")
	if err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteEnrollment(ctx, id, badID); err != nil {
		t.Fatalf("DeleteEnrollment() error: %v", err)
	}
	record, _ := db.GetSpeaker(ctx, id)
	if got := speaker.Cosine(record.Centroid, good); math.Abs(got-1) > 1e-6 {
		t.Fatalf("centroid still carries the deleted sample (similarity to the good one: %v)", got)
	}
}

func TestDeletingASpeakerCascadesToEnrollments(t *testing.T) {
	db, ctx := newStore(t)
	id, _ := db.CreateSpeaker(ctx, "tomer", speaker.RoleOwner, nil)
	if _, err := db.AddEnrollment(ctx, id, vector(1, 0, 0), 2, "wake"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSpeaker(ctx, id); err != nil {
		t.Fatal(err)
	}
	samples, err := db.ListEnrollments(ctx, id)
	if err != nil {
		t.Fatalf("ListEnrollments() error: %v", err)
	}
	if len(samples) != 0 {
		t.Fatalf("%d enrollments survived the cascade", len(samples))
	}
}

func TestIntentCRUDAndOrdering(t *testing.T) {
	db, ctx := newStore(t)

	id, err := db.CreateIntent(ctx, intent.Rule{
		Name: "lights", Enabled: true, MatchType: intent.MatchContains,
		Patterns: []string{"lights"}, MinRole: speaker.RoleMember,
		Handler: intent.HandlerReply, HandlerConfig: json.RawMessage(`{"template":"ok"}`),
		Priority: 5,
	})
	if err != nil {
		t.Fatalf("CreateIntent() error: %v", err)
	}

	rules, _ := db.Rules(ctx)
	if rules[0].Name != "lights" {
		t.Fatalf("lowest priority rule is %q, want lights", rules[0].Name)
	}

	rule, _ := db.GetIntent(ctx, id)
	rule.Priority = 900
	if err := db.UpdateIntent(ctx, id, rule); err != nil {
		t.Fatalf("UpdateIntent() error: %v", err)
	}
	rules, _ = db.Rules(ctx)
	if rules[len(rules)-1].Name != "lights" {
		t.Fatalf("reprioritised rule did not move to the end: %+v", rules)
	}

	if err := db.DeleteIntent(ctx, id); err != nil {
		t.Fatalf("DeleteIntent() error: %v", err)
	}
	if _, err := db.GetIntent(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetIntent() after delete = %v, want ErrNotFound", err)
	}
}

func TestCreateIntentRejectsAnInvalidRule(t *testing.T) {
	db, ctx := newStore(t)
	_, err := db.CreateIntent(ctx, intent.Rule{
		Name: "broken", Enabled: true, MatchType: intent.MatchRegex,
		Patterns: []string{"([a-z"}, MinRole: speaker.RoleMember, Handler: intent.HandlerReply,
	})
	if err == nil {
		t.Fatal("expected the invalid regex to be rejected before it reached the database")
	}
}

func TestRulesReturnsOnlyEnabledIntents(t *testing.T) {
	db, ctx := newStore(t)
	id, err := db.CreateIntent(ctx, intent.Rule{
		Name: "disabled", Enabled: false, MatchType: intent.MatchContains,
		Patterns: []string{"x"}, MinRole: speaker.RoleMember, Handler: intent.HandlerReply,
		HandlerConfig: json.RawMessage(`{"template":"ok"}`),
	})
	if err != nil {
		t.Fatalf("CreateIntent() error: %v", err)
	}

	enabled, _ := db.Rules(ctx)
	for _, rule := range enabled {
		if rule.ID == id {
			t.Fatal("Rules() returned a disabled intent")
		}
	}
	all, _ := db.ListIntents(ctx)
	found := false
	for _, rule := range all {
		if rule.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("ListIntents() should show disabled intents to the UI")
	}
}

func TestReorderIntents(t *testing.T) {
	db, ctx := newStore(t)
	first, err := db.CreateIntent(ctx, intent.Rule{
		Name: "a", Enabled: true, MatchType: intent.MatchContains, Patterns: []string{"a"},
		MinRole: speaker.RoleMember, Handler: intent.HandlerReply, Priority: 10,
		HandlerConfig: json.RawMessage(`{"template":"a"}`),
	})
	if err != nil {
		t.Fatalf("CreateIntent(a) error: %v", err)
	}
	second, err := db.CreateIntent(ctx, intent.Rule{
		Name: "b", Enabled: true, MatchType: intent.MatchContains, Patterns: []string{"b"},
		MinRole: speaker.RoleMember, Handler: intent.HandlerReply, Priority: 20,
		HandlerConfig: json.RawMessage(`{"template":"b"}`),
	})
	if err != nil {
		t.Fatalf("CreateIntent(b) error: %v", err)
	}

	if err := db.ReorderIntents(ctx, map[int64]int{second: 10, first: 20}); err != nil {
		t.Fatalf("ReorderIntents() error: %v", err)
	}
	b, _ := db.GetIntent(ctx, second)
	a, _ := db.GetIntent(ctx, first)
	if b.Priority >= a.Priority {
		t.Fatalf("order did not change: a=%d b=%d", a.Priority, b.Priority)
	}
}

func TestUtteranceHistoryFilteringAndPaging(t *testing.T) {
	db, ctx := newStore(t)

	for i, item := range []Utterance{
		{Speaker: "tomer", Transcript: "turn on the lights", Intent: "lights", Allowed: true},
		{Speaker: "dana", Transcript: "what time is it", Intent: "time", Allowed: true},
		{Speaker: "unknown", Transcript: "open the door", Intent: "denied", Allowed: false},
	} {
		item.LatencyMsTotal = (i + 1) * 100
		if _, err := db.InsertUtterance(ctx, item); err != nil {
			t.Fatalf("InsertUtterance() error: %v", err)
		}
	}

	all, total, err := db.ListUtterances(ctx, HistoryFilter{})
	if err != nil {
		t.Fatalf("ListUtterances() error: %v", err)
	}
	if total != 3 || len(all) != 3 {
		t.Fatalf("got %d of %d, want 3 of 3", len(all), total)
	}
	if all[0].Transcript != "open the door" {
		t.Fatalf("history is not newest-first: %q", all[0].Transcript)
	}

	bySpeaker, total, _ := db.ListUtterances(ctx, HistoryFilter{Speaker: "dana"})
	if total != 1 || bySpeaker[0].Intent != "time" {
		t.Fatalf("speaker filter returned %+v", bySpeaker)
	}

	search, _, _ := db.ListUtterances(ctx, HistoryFilter{Search: "lights"})
	if len(search) != 1 || search[0].Speaker != "tomer" {
		t.Fatalf("search returned %+v", search)
	}

	page, total, _ := db.ListUtterances(ctx, HistoryFilter{Limit: 2, Offset: 2})
	if total != 3 || len(page) != 1 {
		t.Fatalf("paging returned %d rows of %d", len(page), total)
	}
}

func TestGetUtterance(t *testing.T) {
	db, ctx := newStore(t)
	id, _ := db.InsertUtterance(ctx, Utterance{Speaker: "tomer", Transcript: "hello", Allowed: true})

	got, err := db.GetUtterance(ctx, id)
	if err != nil {
		t.Fatalf("GetUtterance() error: %v", err)
	}
	if got.Transcript != "hello" {
		t.Fatalf("transcript = %q", got.Transcript)
	}
	if _, err := db.GetUtterance(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetUtterance(missing) = %v, want ErrNotFound", err)
	}
}

func TestStats(t *testing.T) {
	db, ctx := newStore(t)
	db.CreateSpeaker(ctx, "tomer", speaker.RoleOwner, nil)
	for _, item := range []Utterance{
		{Speaker: "tomer", Intent: "lights", Allowed: true, LatencyMsTotal: 100},
		{Speaker: "tomer", Intent: "lights", Allowed: true, LatencyMsTotal: 300},
		{Speaker: "unknown", Intent: "denied", Allowed: false, LatencyMsTotal: 200},
	} {
		db.InsertUtterance(ctx, item)
	}

	stats, err := db.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if stats.Total != 3 || stats.Last24h != 3 {
		t.Fatalf("counts = %d total, %d recent", stats.Total, stats.Last24h)
	}
	if stats.AvgLatencyMs != 200 {
		t.Fatalf("average latency = %d, want 200", stats.AvgLatencyMs)
	}
	if stats.BySpeaker["tomer"] != 2 {
		t.Fatalf("by-speaker = %v", stats.BySpeaker)
	}
	if stats.UnknownRatePct != 33 {
		t.Fatalf("unknown rate = %d%%, want 33%%", stats.UnknownRatePct)
	}
	if stats.SpeakerCount != 1 {
		t.Fatalf("speaker count = %d, want 1", stats.SpeakerCount)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	db, ctx := newStore(t)

	type payload struct {
		Threshold float64 `json:"threshold"`
		Policy    string  `json:"policy"`
	}
	var loaded payload

	found, err := db.GetSetting(ctx, "runtime", &loaded)
	if err != nil || found {
		t.Fatalf("GetSetting() on a fresh database = %v, %v", found, err)
	}

	if err := db.PutSetting(ctx, "runtime", payload{Threshold: 0.5, Policy: "deny"}); err != nil {
		t.Fatalf("PutSetting() error: %v", err)
	}
	found, err = db.GetSetting(ctx, "runtime", &loaded)
	if err != nil || !found || loaded.Threshold != 0.5 || loaded.Policy != "deny" {
		t.Fatalf("GetSetting() = %+v, %v, %v", loaded, found, err)
	}

	// Writing again must overwrite rather than fail on the primary key.
	if err := db.PutSetting(ctx, "runtime", payload{Threshold: 0.7, Policy: "allow"}); err != nil {
		t.Fatalf("second PutSetting() error: %v", err)
	}
	db.GetSetting(ctx, "runtime", &loaded)
	if loaded.Threshold != 0.7 {
		t.Fatalf("setting was not overwritten: %+v", loaded)
	}
}

func TestListSpeakersIsSortedByNameAndCountsSamples(t *testing.T) {
	db, ctx := newStore(t)
	for _, name := range []string{"zoe", "adam", "mia"} {
		id, err := db.CreateSpeaker(ctx, name, speaker.RoleMember, nil)
		if err != nil {
			t.Fatalf("CreateSpeaker(%q) error: %v", name, err)
		}
		if name == "mia" {
			db.AddEnrollment(ctx, id, vector(1, 0, 0), 2, "wake")
		}
	}

	records, err := db.ListSpeakers(ctx)
	if err != nil {
		t.Fatalf("ListSpeakers() error: %v", err)
	}
	if len(records) != 3 || records[0].Name != "adam" || records[2].Name != "zoe" {
		t.Fatalf("order = %+v", records)
	}
	for _, record := range records {
		want := 0
		if record.Name == "mia" {
			want = 1
		}
		if record.Samples != want {
			t.Fatalf("%s has %d samples, want %d", record.Name, record.Samples, want)
		}
	}
}

func TestGetSpeakerByName(t *testing.T) {
	db, ctx := newStore(t)
	if _, err := db.CreateSpeaker(ctx, "tomer", speaker.RoleOwner, nil); err != nil {
		t.Fatal(err)
	}

	record, err := db.GetSpeakerByName(ctx, "tomer")
	if err != nil {
		t.Fatalf("GetSpeakerByName() error: %v", err)
	}
	if record.Role != speaker.RoleOwner {
		t.Fatalf("role = %q", record.Role)
	}
	if _, err := db.GetSpeakerByName(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSpeakerByName(missing) = %v, want ErrNotFound", err)
	}
}

func TestDeletingAMissingRowIsReportedAsNotFound(t *testing.T) {
	db, ctx := newStore(t)
	if err := db.DeleteSpeaker(ctx, 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteSpeaker(missing) = %v, want ErrNotFound", err)
	}
	if err := db.DeleteIntent(ctx, 4242); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteIntent(missing) = %v, want ErrNotFound", err)
	}
	if err := db.DeleteEnrollment(ctx, 1, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteEnrollment(missing) = %v, want ErrNotFound", err)
	}
}

func TestPruneAudioClearsExpiredRecordings(t *testing.T) {
	db, ctx := newStore(t)
	old := time.Now().UTC().Add(-48 * time.Hour)
	fresh := time.Now().UTC()

	if _, err := db.InsertUtterance(ctx, Utterance{TS: old, AudioPath: "/audio/old-command.wav"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertUtterance(ctx, Utterance{TS: fresh, AudioPath: "/audio/new-command.wav"}); err != nil {
		t.Fatal(err)
	}

	paths, err := db.PruneAudio(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("PruneAudio() error: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/audio/old-command.wav" {
		t.Fatalf("pruned %v", paths)
	}

	items, _, _ := db.ListUtterances(ctx, HistoryFilter{})
	for _, item := range items {
		if item.TS.Before(time.Now().Add(-24*time.Hour)) && item.AudioPath != "" {
			t.Fatalf("expired row still points at audio: %+v", item)
		}
		if item.TS.After(time.Now().Add(-24*time.Hour)) && item.AudioPath == "" {
			t.Fatalf("fresh row lost its audio: %+v", item)
		}
	}

	// A second run has nothing left to do.
	again, err := db.PruneAudio(ctx, time.Now().Add(-24*time.Hour))
	if err != nil || len(again) != 0 {
		t.Fatalf("second PruneAudio() = %v, %v", again, err)
	}
}

func TestHistoryFilterCapsThePageSize(t *testing.T) {
	db, ctx := newStore(t)
	for i := 0; i < 3; i++ {
		db.InsertUtterance(ctx, Utterance{Transcript: "hello", Allowed: true})
	}

	items, _, err := db.ListUtterances(ctx, HistoryFilter{Limit: 10_000, Offset: -5})
	if err != nil {
		t.Fatalf("ListUtterances() error: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d rows", len(items))
	}
}
