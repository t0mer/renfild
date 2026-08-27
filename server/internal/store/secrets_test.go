package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/t0mer/renfild/internal/intent"
	"github.com/t0mer/renfild/internal/secret"
	"github.com/t0mer/renfild/internal/speaker"
)

const webhookToken = "Bearer sk-do-not-store-me"

func sealedStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	db, ctx := newStore(t)

	key := make([]byte, secret.KeySize)
	for i := range key {
		key[i] = byte(i * 7)
	}
	box, err := secret.New(key)
	if err != nil {
		t.Fatalf("secret.New() error: %v", err)
	}
	db.UseSecrets(box)
	return db, ctx
}

func webhookRule(config string) intent.Rule {
	return intent.Rule{
		Name: "open the gate", Enabled: true, MatchType: intent.MatchContains,
		Patterns: []string{"open the gate"}, MinRole: speaker.RoleOwner,
		Handler: intent.HandlerWebhook, HandlerConfig: json.RawMessage(config),
	}
}

// rawConfig reads handler_config straight out of SQLite, bypassing every layer
// that might tidy it up on the way past.
func rawConfig(t *testing.T, db *Store, id int64) string {
	t.Helper()
	var config string
	if err := db.db.QueryRow(`SELECT handler_config FROM intents WHERE id = ?`, id).Scan(&config); err != nil {
		t.Fatalf("reading handler_config: %v", err)
	}
	return config
}

// findRule picks a rule out by name; the database ships with seeded defaults,
// so position says nothing.
func findRule(t *testing.T, rules []intent.Rule, name string) intent.Rule {
	t.Helper()
	for _, rule := range rules {
		if rule.Name == name {
			return rule
		}
	}
	t.Fatalf("rule %q is not in the %d returned", name, len(rules))
	return intent.Rule{}
}

func headerValue(t *testing.T, config, name string) string {
	t.Helper()
	var fields struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal([]byte(config), &fields); err != nil {
		t.Fatalf("decoding %s: %v", config, err)
	}
	return fields.Headers[name]
}

func TestWebhookCredentialsAreEncryptedAtRest(t *testing.T) {
	db, ctx := sealedStore(t)

	id, err := db.CreateIntent(ctx, webhookRule(
		`{"url":"http://nas:8123/gate","method":"POST","headers":{"Authorization":"`+webhookToken+`"}}`))
	if err != nil {
		t.Fatalf("CreateIntent() error: %v", err)
	}

	stored := rawConfig(t, db, id)
	if strings.Contains(stored, webhookToken) {
		t.Fatalf("the token is in the database in plaintext: %s", stored)
	}
	if !secret.IsSealed(headerValue(t, stored, "Authorization")) {
		t.Fatalf("the header was not sealed: %s", stored)
	}
	// The rest of the config is not a secret and stays readable.
	if !strings.Contains(stored, "http://nas:8123/gate") {
		t.Fatalf("the url was encrypted too: %s", stored)
	}
}

func TestRulesHandsTheRouterTheRealCredential(t *testing.T) {
	db, ctx := sealedStore(t)

	if _, err := db.CreateIntent(ctx, webhookRule(
		`{"url":"http://nas/gate","headers":{"Authorization":"`+webhookToken+`"}}`)); err != nil {
		t.Fatalf("CreateIntent() error: %v", err)
	}

	rules, err := db.Rules(ctx)
	if err != nil {
		t.Fatalf("Rules() error: %v", err)
	}
	rule := findRule(t, rules, "open the gate")
	if got := headerValue(t, string(rule.HandlerConfig), "Authorization"); got != webhookToken {
		t.Fatalf("Rules() gave the handler %q", got)
	}
}

func TestTheWebUINeverSeesTheCredential(t *testing.T) {
	db, ctx := sealedStore(t)

	id, err := db.CreateIntent(ctx, webhookRule(
		`{"url":"http://nas/gate","headers":{"Authorization":"`+webhookToken+`"}}`))
	if err != nil {
		t.Fatalf("CreateIntent() error: %v", err)
	}

	one, err := db.GetIntent(ctx, id)
	if err != nil {
		t.Fatalf("GetIntent() error: %v", err)
	}
	all, err := db.ListIntents(ctx)
	if err != nil {
		t.Fatalf("ListIntents() error: %v", err)
	}

	configs := []string{string(one.HandlerConfig)}
	for _, rule := range all {
		configs = append(configs, string(rule.HandlerConfig))
	}
	for _, config := range configs {
		if strings.Contains(config, webhookToken) {
			t.Fatalf("the UI was served the token: %s", config)
		}
		if strings.Contains(config, "enc:v1:") {
			t.Fatalf("the UI was served the ciphertext: %s", config)
		}
	}
	if got := headerValue(t, string(one.HandlerConfig), "Authorization"); got != intent.Masked {
		t.Fatalf("GetIntent() header = %q, want the mask", got)
	}
}

func TestSavingAnUntouchedHeaderKeepsTheCredential(t *testing.T) {
	db, ctx := sealedStore(t)

	id, err := db.CreateIntent(ctx, webhookRule(
		`{"url":"http://nas/gate","headers":{"Authorization":"`+webhookToken+`"}}`))
	if err != nil {
		t.Fatalf("CreateIntent() error: %v", err)
	}

	// What the intent editor sends back when the user changes the URL and
	// leaves the masked header alone.
	edited := webhookRule(`{"url":"http://nas/side-gate","headers":{"Authorization":"` + intent.Masked + `"}}`)
	if err := db.UpdateIntent(ctx, id, edited); err != nil {
		t.Fatalf("UpdateIntent() error: %v", err)
	}

	rules, err := db.Rules(ctx)
	if err != nil {
		t.Fatalf("Rules() error: %v", err)
	}
	config := string(findRule(t, rules, "open the gate").HandlerConfig)
	if got := headerValue(t, config, "Authorization"); got != webhookToken {
		t.Fatalf("the credential was lost on save: %q", got)
	}
	if !strings.Contains(config, "side-gate") {
		t.Fatalf("the edit was not applied: %s", config)
	}
}

func TestReplacingAHeaderStoresTheNewCredential(t *testing.T) {
	db, ctx := sealedStore(t)

	id, err := db.CreateIntent(ctx, webhookRule(
		`{"url":"http://nas/gate","headers":{"Authorization":"`+webhookToken+`"}}`))
	if err != nil {
		t.Fatalf("CreateIntent() error: %v", err)
	}

	const replacement = "Bearer sk-the-new-one"
	if err := db.UpdateIntent(ctx, id, webhookRule(
		`{"url":"http://nas/gate","headers":{"Authorization":"`+replacement+`"}}`)); err != nil {
		t.Fatalf("UpdateIntent() error: %v", err)
	}

	stored := rawConfig(t, db, id)
	if strings.Contains(stored, replacement) {
		t.Fatalf("the new token was stored in plaintext: %s", stored)
	}
	rules, _ := db.Rules(ctx)
	if got := headerValue(t, string(findRule(t, rules, "open the gate").HandlerConfig), "Authorization"); got != replacement {
		t.Fatalf("the new credential did not take: %q", got)
	}
}

func TestAPlaintextDatabaseKeepsWorkingAndIsUpgradedOnSave(t *testing.T) {
	db, ctx := sealedStore(t)

	// A row as it would have been written before any of this existed.
	config := `{"url":"http://nas/gate","headers":{"Authorization":"` + webhookToken + `"}}`
	result, err := db.db.ExecContext(ctx, `
		INSERT INTO intents (name, enabled, match_type, patterns, min_role, handler,
		                     handler_config, priority, created_at, updated_at)
		VALUES ('legacy', 1, 'contains', '["open the gate"]', 'owner', 'webhook', ?, 100,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, config)
	if err != nil {
		t.Fatalf("seeding the legacy row: %v", err)
	}
	id, _ := result.LastInsertId()

	rules, err := db.Rules(ctx)
	if err != nil {
		t.Fatalf("Rules() error: %v", err)
	}
	if got := headerValue(t, string(findRule(t, rules, "legacy").HandlerConfig), "Authorization"); got != webhookToken {
		t.Fatalf("the plaintext row did not survive the read: %q", got)
	}

	// Saving it seals the credential.
	upgraded := webhookRule(config)
	upgraded.Name = "legacy"
	if err := db.UpdateIntent(ctx, id, upgraded); err != nil {
		t.Fatalf("UpdateIntent() error: %v", err)
	}
	if strings.Contains(rawConfig(t, db, id), webhookToken) {
		t.Fatalf("the row was not upgraded: %s", rawConfig(t, db, id))
	}
}

func TestNonWebhookRulesAreLeftAlone(t *testing.T) {
	db, ctx := sealedStore(t)

	id, err := db.CreateIntent(ctx, intent.Rule{
		Name: "say hello back", Enabled: true, MatchType: intent.MatchContains,
		Patterns: []string{"hello"}, MinRole: speaker.RoleAny, Handler: intent.HandlerReply,
		HandlerConfig: json.RawMessage(`{"template":"Hello {{.Speaker}}."}`),
	})
	if err != nil {
		t.Fatalf("CreateIntent() error: %v", err)
	}
	if stored := rawConfig(t, db, id); !strings.Contains(stored, "Hello {{.Speaker}}.") {
		t.Fatalf("a reply template was touched: %s", stored)
	}
}

func TestSealingPreservesFieldsItDoesNotKnowAbout(t *testing.T) {
	db, ctx := sealedStore(t)

	id, err := db.CreateIntent(ctx, webhookRule(
		`{"url":"http://nas/gate","speak_response":true,"timeout_seconds":4,`+
			`"headers":{"Authorization":"`+webhookToken+`"}}`))
	if err != nil {
		t.Fatalf("CreateIntent() error: %v", err)
	}

	stored := rawConfig(t, db, id)
	for _, want := range []string{`"speak_response":true`, `"timeout_seconds":4`} {
		if !strings.Contains(stored, want) {
			t.Fatalf("%s was dropped: %s", want, stored)
		}
	}
}
