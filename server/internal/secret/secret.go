// Package secret encrypts the credentials Renfild has to keep: webhook headers
// carry API tokens for whatever they call, and those sit in the intents table
// until somebody triggers the rule.
//
// Values are sealed with AES-256-GCM and written as "enc:v1:<base64>". Anything
// without that prefix is treated as plaintext and passed through, so a database
// written before this existed keeps working and is upgraded the next time its
// rule is saved.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

// prefix marks a sealed value. The version lets a future algorithm coexist with
// everything already in the database.
const prefix = "enc:v1:"

// Box seals and opens values with one key.
type Box struct {
	aead cipher.AEAD
}

// New builds a Box from a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("building cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("building GCM: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts a value. An empty string stays empty — there is nothing to
// protect, and a blank header is easier to read back as blank.
func (b *Box) Seal(plaintext string) (string, error) {
	if b == nil || plaintext == "" {
		return plaintext, nil
	}
	if IsSealed(plaintext) {
		// Already sealed: re-sealing would double-wrap it.
		return plaintext, nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a value. A value that was never sealed is returned unchanged,
// which is what makes an existing database readable.
func (b *Box) Open(value string) (string, error) {
	if !IsSealed(value) {
		return value, nil
	}
	if b == nil {
		return "", fmt.Errorf("value is encrypted but no key is configured")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil {
		return "", fmt.Errorf("decoding sealed value: %w", err)
	}
	if len(raw) < b.aead.NonceSize() {
		return "", fmt.Errorf("sealed value is too short")
	}
	nonce, ciphertext := raw[:b.aead.NonceSize()], raw[b.aead.NonceSize():]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// Almost always the wrong key, which is worth saying plainly.
		return "", fmt.Errorf("cannot decrypt: %w", err)
	}
	return string(plaintext), nil
}

// IsSealed reports whether a value came out of Seal.
func IsSealed(value string) bool {
	return strings.HasPrefix(value, prefix)
}

// LoadOrCreateKey reads the key file, generating one on first run. The file is
// owner-only: it is the one thing in the data directory that must not be
// readable by anything else.
func LoadOrCreateKey(path string) ([]byte, error) {
	body, err := os.ReadFile(filepath.Clean(path))
	if err == nil {
		key, err := hex.DecodeString(strings.TrimSpace(string(body)))
		if err != nil {
			return nil, fmt.Errorf("key file %s is not hex: %w", path, err)
		}
		if len(key) != KeySize {
			return nil, fmt.Errorf("key file %s holds %d bytes, want %d", path, len(key), KeySize)
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading key file %s: %w", path, err)
	}

	key := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generating key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("creating key directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("writing key file %s: %w", path, err)
	}
	return key, nil
}

// ParseKey reads a key supplied as hex, for deployments that would rather hand
// one in through the environment than keep a file.
func ParseKey(value string) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("key is not hex: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("key is %d bytes, want %d", len(key), KeySize)
	}
	return key, nil
}
