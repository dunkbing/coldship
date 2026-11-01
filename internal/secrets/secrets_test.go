package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpen(t *testing.T) {
	k := New()
	s, err := k.Seal([]byte("hunter2"), "apps.spec:web")
	if err != nil || !Sealed(s) || strings.Contains(s, "hunter2") {
		t.Fatalf("seal: %q %v", s, err)
	}
	if b, err := k.Open(s, "apps.spec:web"); err != nil || string(b) != "hunter2" {
		t.Fatalf("open: %q %v", b, err)
	}
	if s2, _ := k.Seal([]byte("hunter2"), "apps.spec:web"); s2 == s {
		t.Fatal("every seal must use a fresh nonce")
	}
	if _, err := k.Open(s, "apps.spec:other"); !errors.Is(err, ErrUnreadable) {
		t.Fatal("a value moved to another row must not open")
	}
	if _, err := New().Open(s, "apps.spec:web"); !errors.Is(err, ErrUnreadable) {
		t.Fatal("another key must not open it")
	}
	if _, err := k.Open("plain text", "x"); err == nil {
		t.Fatal("plaintext is not a sealed value")
	}
}

func TestRotationKeyring(t *testing.T) {
	old := New()
	s, _ := old.Seal([]byte("v"), "a")
	both := old.WithNewKey()
	if both.CurrentID() == old.CurrentID() || len(both.IDs()) != 2 {
		t.Fatal("WithNewKey should add a current key")
	}
	if _, err := both.Open(s, "a"); err != nil {
		t.Fatal("the ring must still open values sealed with the old key")
	}
	only := both.OnlyCurrent()
	if len(only.IDs()) != 1 {
		t.Fatal("OnlyCurrent keeps one key")
	}
	if _, err := only.Open(s, "a"); err == nil {
		t.Fatal("after dropping the old key, its values must not open")
	}
}

func TestKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hangar.key")
	k := New()
	if err := k.Save(path); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v, want 0600", st.Mode().Perm())
	}
	loaded, err := Load(path)
	if err != nil || loaded.CurrentID() != k.CurrentID() {
		t.Fatalf("load: %v", err)
	}
	s, _ := k.Seal([]byte("v"), "a")
	if _, err := loaded.Open(s, "a"); err != nil {
		t.Fatal("a loaded key must open what the original sealed")
	}
	os.Chmod(path, 0o644)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("a key file readable by others must be refused: %v", err)
	}
	if _, err := FromEnv("too-short"); err == nil {
		t.Fatal("a malformed env key must be refused")
	}
}
