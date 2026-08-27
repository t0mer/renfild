package secret

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testBox(t *testing.T) *Box {
	t.Helper()
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	box, err := New(key)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	return box
}

func TestSealAndOpenRoundTrip(t *testing.T) {
	box := testBox(t)

	for _, plaintext := range []string{
		"hunter2",
		"Bearer eyJhbGciOiJIUzI1NiJ9.payload.signature",
		"סיסמה בעברית",
		strings.Repeat("x", 4096),
	} {
		sealed, err := box.Seal(plaintext)
		if err != nil {
			t.Fatalf("Seal() error: %v", err)
		}
		if strings.Contains(sealed, plaintext) {
			t.Fatalf("the plaintext is still visible in %q", sealed)
		}
		if !IsSealed(sealed) {
			t.Fatalf("Seal() produced an unmarked value: %q", sealed)
		}

		opened, err := box.Open(sealed)
		if err != nil {
			t.Fatalf("Open() error: %v", err)
		}
		if opened != plaintext {
			t.Fatalf("Open() = %q, want %q", opened, plaintext)
		}
	}
}

func TestSealUsesAFreshNonceEveryTime(t *testing.T) {
	box := testBox(t)

	first, _ := box.Seal("hunter2")
	second, _ := box.Seal("hunter2")
	if first == second {
		t.Fatal("the same plaintext sealed to the same ciphertext twice")
	}
}

func TestOpenPassesPlaintextThrough(t *testing.T) {
	box := testBox(t)

	// What a database written before any of this existed holds.
	opened, err := box.Open("hunter2")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	if opened != "hunter2" {
		t.Fatalf("Open() = %q", opened)
	}
}

func TestSealLeavesAnAlreadySealedValueAlone(t *testing.T) {
	box := testBox(t)

	once, _ := box.Seal("hunter2")
	twice, err := box.Seal(once)
	if err != nil {
		t.Fatalf("Seal() error: %v", err)
	}
	if twice != once {
		t.Fatal("sealing a sealed value wrapped it again")
	}
}

func TestSealLeavesAnEmptyValueEmpty(t *testing.T) {
	box := testBox(t)
	sealed, err := box.Seal("")
	if err != nil || sealed != "" {
		t.Fatalf("Seal(\"\") = %q, %v", sealed, err)
	}
}

func TestOpenRefusesTheWrongKey(t *testing.T) {
	sealed, _ := testBox(t).Seal("hunter2")

	other := make([]byte, KeySize)
	for i := range other {
		other[i] = byte(255 - i)
	}
	box, err := New(other)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if _, err := box.Open(sealed); err == nil {
		t.Fatal("a value decrypted under the wrong key")
	}
}

func TestOpenRefusesATamperedValue(t *testing.T) {
	box := testBox(t)
	sealed, _ := box.Seal("hunter2")

	// Flip the last character of the base64 payload.
	runes := []rune(sealed)
	if runes[len(runes)-1] == 'A' {
		runes[len(runes)-1] = 'B'
	} else {
		runes[len(runes)-1] = 'A'
	}
	if _, err := box.Open(string(runes)); err == nil {
		t.Fatal("a tampered value was accepted")
	}
}

func TestOpenWithoutAKey(t *testing.T) {
	sealed, _ := testBox(t).Seal("hunter2")

	var box *Box
	if _, err := box.Open(sealed); err == nil {
		t.Fatal("a sealed value opened without a key")
	}
	// Plaintext still passes through, so a server with no key configured can
	// still read a database that never had any secrets in it.
	if got, err := box.Open("plain"); err != nil || got != "plain" {
		t.Fatalf("Open() = %q, %v", got, err)
	}
}

func TestNewRejectsAKeyOfTheWrongLength(t *testing.T) {
	for _, size := range []int{0, 16, 31, 33, 64} {
		if _, err := New(make([]byte, size)); err == nil {
			t.Fatalf("New() accepted a %d-byte key", size)
		}
	}
}

func TestLoadOrCreateKeyGeneratesOnceAndIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "secret.key")

	first, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreateKey() error: %v", err)
	}
	if len(first) != KeySize {
		t.Fatalf("key is %d bytes", len(first))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("key file mode = %04o, want 0600", mode)
	}

	second, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("second LoadOrCreateKey() error: %v", err)
	}
	if hex.EncodeToString(first) != hex.EncodeToString(second) {
		t.Fatal("the key changed between calls, which would orphan every stored secret")
	}
}

func TestLoadOrCreateKeyRejectsARuinedFile(t *testing.T) {
	dir := t.TempDir()

	for name, body := range map[string]string{
		"not-hex.key": "nonsense!!",
		"short.key":   hex.EncodeToString(make([]byte, 16)),
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
		if _, err := LoadOrCreateKey(path); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestParseKey(t *testing.T) {
	good := hex.EncodeToString(make([]byte, KeySize))
	if _, err := ParseKey("  " + good + "\n"); err != nil {
		t.Fatalf("ParseKey() error: %v", err)
	}
	for _, bad := range []string{"", "zz", hex.EncodeToString(make([]byte, 16))} {
		if _, err := ParseKey(bad); err == nil {
			t.Fatalf("ParseKey(%q) was accepted", bad)
		}
	}
}
