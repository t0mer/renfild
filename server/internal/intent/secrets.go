package intent

import (
	"encoding/json"
	"fmt"

	"github.com/t0mer/renfild/internal/secret"
)

// Masked is what the web UI is shown in place of a stored credential. A rule
// saved without touching its headers comes back carrying these, and they are
// swapped for the values already in the database rather than stored.
const Masked = "••••••••"

// headersKey is the field of a webhook handler_config that holds credentials.
// Nothing else in a rule is secret: a URL and a body template are worth being
// able to read on the History page.
const headersKey = "headers"

// SealHandlerConfig encrypts the header values of a webhook rule before it is
// written. previous is whatever is already stored for this rule, so a header
// the user did not retype keeps the value it had.
func SealHandlerConfig(rule *Rule, box *secret.Box, previous json.RawMessage) error {
	if rule.Handler != HandlerWebhook {
		return nil
	}
	kept, err := headersOf(previous)
	if err != nil {
		// A previous config we cannot read is not a reason to refuse the new
		// one; the user is replacing it either way.
		kept = nil
	}

	return mapHeaders(rule, func(name, value string) (string, error) {
		if value == Masked {
			if existing, ok := kept[name]; ok {
				return existing, nil
			}
			// Nothing to restore: an empty header is better than storing the
			// mask and sending it as a credential.
			return "", nil
		}
		return box.Seal(value)
	})
}

// OpenHandlerConfig decrypts the header values so the handler can send them.
func OpenHandlerConfig(rule *Rule, box *secret.Box) error {
	if rule.Handler != HandlerWebhook {
		return nil
	}
	return mapHeaders(rule, func(_, value string) (string, error) {
		return box.Open(value)
	})
}

// MaskHandlerConfig replaces every header value with Masked. The web UI reads
// rules through this, so a credential never leaves the machine it is used on.
func MaskHandlerConfig(rule *Rule) {
	if rule.Handler != HandlerWebhook {
		return
	}
	// A failure here means the config is malformed, which the UI will show
	// anyway; there is nothing to leak in that case.
	_ = mapHeaders(rule, func(_, value string) (string, error) {
		if value == "" {
			return "", nil
		}
		return Masked, nil
	})
}

// headersOf pulls the headers map out of a stored config, values as they sit in
// the database.
func headersOf(config json.RawMessage) (map[string]string, error) {
	if len(config) == 0 {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(config, &fields); err != nil {
		return nil, fmt.Errorf("decoding handler_config: %w", err)
	}
	raw, ok := fields[headersKey]
	if !ok {
		return nil, nil
	}
	var headers map[string]string
	if err := json.Unmarshal(raw, &headers); err != nil {
		return nil, fmt.Errorf("decoding headers: %w", err)
	}
	return headers, nil
}

// mapHeaders rewrites every header value through fn, leaving the rest of the
// config byte-for-byte as it was found. Decoding the whole config into the
// handler's struct would quietly drop any field that struct does not know
// about, which is not a thing a save should do.
func mapHeaders(rule *Rule, fn func(name, value string) (string, error)) error {
	if len(rule.HandlerConfig) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rule.HandlerConfig, &fields); err != nil {
		return fmt.Errorf("decoding handler_config: %w", err)
	}
	raw, ok := fields[headersKey]
	if !ok {
		return nil
	}
	var headers map[string]string
	if err := json.Unmarshal(raw, &headers); err != nil {
		return fmt.Errorf("decoding headers: %w", err)
	}
	if len(headers) == 0 {
		return nil
	}

	for name, value := range headers {
		replaced, err := fn(name, value)
		if err != nil {
			return fmt.Errorf("header %q: %w", name, err)
		}
		headers[name] = replaced
	}

	encoded, err := json.Marshal(headers)
	if err != nil {
		return fmt.Errorf("encoding headers: %w", err)
	}
	fields[headersKey] = encoded

	config, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encoding handler_config: %w", err)
	}
	rule.HandlerConfig = config
	return nil
}
