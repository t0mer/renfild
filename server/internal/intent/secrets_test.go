package intent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/t0mer/renfild/internal/secret"
)

func testBox(t *testing.T) *secret.Box {
	t.Helper()
	key := make([]byte, secret.KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	box, err := secret.New(key)
	if err != nil {
		t.Fatalf("secret.New() error: %v", err)
	}
	return box
}

func hookRule(config string) Rule {
	return Rule{Name: "gate", Handler: HandlerWebhook, HandlerConfig: json.RawMessage(config)}
}

func header(t *testing.T, rule Rule, name string) string {
	t.Helper()
	var fields struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(rule.HandlerConfig, &fields); err != nil {
		t.Fatalf("decoding %s: %v", rule.HandlerConfig, err)
	}
	return fields.Headers[name]
}

func TestSealAndOpenAWebhookHeader(t *testing.T) {
	box := testBox(t)
	rule := hookRule(`{"url":"http://nas/x","headers":{"X-Token":"hunter2"}}`)

	if err := SealHandlerConfig(&rule, box, nil); err != nil {
		t.Fatalf("SealHandlerConfig() error: %v", err)
	}
	if strings.Contains(string(rule.HandlerConfig), "hunter2") {
		t.Fatalf("the value survived in the clear: %s", rule.HandlerConfig)
	}
	if !secret.IsSealed(header(t, rule, "X-Token")) {
		t.Fatalf("not sealed: %s", rule.HandlerConfig)
	}

	if err := OpenHandlerConfig(&rule, box); err != nil {
		t.Fatalf("OpenHandlerConfig() error: %v", err)
	}
	if got := header(t, rule, "X-Token"); got != "hunter2" {
		t.Fatalf("Open gave %q", got)
	}
}

func TestMaskHidesEveryHeaderButKeepsTheRest(t *testing.T) {
	rule := hookRule(`{"url":"http://nas/x","body":"{{.Speaker}}","headers":{"X-Token":"hunter2","X-Other":"abc"}}`)

	MaskHandlerConfig(&rule)

	for _, name := range []string{"X-Token", "X-Other"} {
		if got := header(t, rule, name); got != Masked {
			t.Fatalf("%s = %q, want the mask", name, got)
		}
	}
	for _, want := range []string{"http://nas/x", "{{.Speaker}}"} {
		if !strings.Contains(string(rule.HandlerConfig), want) {
			t.Fatalf("%s was masked too: %s", want, rule.HandlerConfig)
		}
	}
}

func TestMaskLeavesAnEmptyHeaderEmpty(t *testing.T) {
	rule := hookRule(`{"url":"http://nas/x","headers":{"X-Token":""}}`)
	MaskHandlerConfig(&rule)
	if got := header(t, rule, "X-Token"); got != "" {
		t.Fatalf("an empty header was masked into %q, which reads as a stored secret", got)
	}
}

func TestSealRestoresAMaskedHeaderFromWhatIsStored(t *testing.T) {
	box := testBox(t)
	stored := hookRule(`{"url":"http://nas/x","headers":{"X-Token":"hunter2"}}`)
	if err := SealHandlerConfig(&stored, box, nil); err != nil {
		t.Fatalf("SealHandlerConfig() error: %v", err)
	}

	// What the editor sends back after the user changed only the URL.
	edited := hookRule(`{"url":"http://nas/y","headers":{"X-Token":"` + Masked + `"}}`)
	if err := SealHandlerConfig(&edited, box, stored.HandlerConfig); err != nil {
		t.Fatalf("SealHandlerConfig() error: %v", err)
	}
	if err := OpenHandlerConfig(&edited, box); err != nil {
		t.Fatalf("OpenHandlerConfig() error: %v", err)
	}
	if got := header(t, edited, "X-Token"); got != "hunter2" {
		t.Fatalf("the credential was lost: %q", got)
	}
}

func TestSealDropsAMaskWithNothingBehindIt(t *testing.T) {
	box := testBox(t)
	// A header that was never stored: the mask is not a credential, and
	// sending it as one to a real endpoint would be worse than sending nothing.
	rule := hookRule(`{"url":"http://nas/x","headers":{"X-Token":"` + Masked + `"}}`)

	if err := SealHandlerConfig(&rule, box, nil); err != nil {
		t.Fatalf("SealHandlerConfig() error: %v", err)
	}
	if got := header(t, rule, "X-Token"); got != "" {
		t.Fatalf("X-Token = %q, want it dropped", got)
	}
}

func TestSealIgnoresAnUnreadablePreviousConfig(t *testing.T) {
	box := testBox(t)
	rule := hookRule(`{"url":"http://nas/x","headers":{"X-Token":"hunter2"}}`)

	// A previous config we cannot parse is no reason to refuse the new one.
	if err := SealHandlerConfig(&rule, box, json.RawMessage(`{ broken`)); err != nil {
		t.Fatalf("SealHandlerConfig() error: %v", err)
	}
	if err := OpenHandlerConfig(&rule, box); err != nil {
		t.Fatalf("OpenHandlerConfig() error: %v", err)
	}
	if got := header(t, rule, "X-Token"); got != "hunter2" {
		t.Fatalf("header = %q", got)
	}
}

func TestNonWebhookHandlersAreUntouched(t *testing.T) {
	box := testBox(t)
	const config = `{"template":"Hello {{.Speaker}}.","headers":{"X-Token":"hunter2"}}`

	for _, handler := range []string{HandlerReply, HandlerLLM} {
		rule := Rule{Name: "x", Handler: handler, HandlerConfig: json.RawMessage(config)}
		if err := SealHandlerConfig(&rule, box, nil); err != nil {
			t.Fatalf("SealHandlerConfig() error: %v", err)
		}
		MaskHandlerConfig(&rule)
		if string(rule.HandlerConfig) != config {
			t.Fatalf("%s config was rewritten: %s", handler, rule.HandlerConfig)
		}
	}
}

func TestConfigsWithNoHeadersAreLeftAlone(t *testing.T) {
	box := testBox(t)

	for _, config := range []string{"", "{}", `{"url":"http://nas/x"}`, `{"url":"http://nas/x","headers":{}}`} {
		rule := hookRule(config)
		before := string(rule.HandlerConfig)
		if err := SealHandlerConfig(&rule, box, nil); err != nil {
			t.Fatalf("SealHandlerConfig(%q) error: %v", config, err)
		}
		if err := OpenHandlerConfig(&rule, box); err != nil {
			t.Fatalf("OpenHandlerConfig(%q) error: %v", config, err)
		}
		MaskHandlerConfig(&rule)
		if string(rule.HandlerConfig) != before {
			t.Fatalf("%q became %q", before, rule.HandlerConfig)
		}
	}
}

func TestMalformedConfigsAreReported(t *testing.T) {
	box := testBox(t)

	for _, config := range []string{`{ broken`, `"a string"`, `{"headers":"not a map"}`} {
		rule := hookRule(config)
		if err := SealHandlerConfig(&rule, box, nil); err == nil {
			t.Fatalf("SealHandlerConfig(%q) was accepted", config)
		}
	}
}

func TestOpenWithoutAKeyFailsLoudly(t *testing.T) {
	sealed := hookRule(`{"url":"http://nas/x","headers":{"X-Token":"hunter2"}}`)
	if err := SealHandlerConfig(&sealed, testBox(t), nil); err != nil {
		t.Fatalf("SealHandlerConfig() error: %v", err)
	}

	// A server started against a database whose key has gone missing must say
	// so, not quietly send ciphertext as a credential.
	if err := OpenHandlerConfig(&sealed, nil); err == nil {
		t.Fatal("a sealed header opened with no key")
	}
}
