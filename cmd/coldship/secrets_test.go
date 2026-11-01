package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dunkbing/coldship/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestOpenStoreKeyHandling(t *testing.T) {
	dir := t.TempDir()
	data, keyFile := filepath.Join(dir, "coldship.db"), filepath.Join(dir, "coldship.key")
	t.Setenv("COLDSHIP_SECRET_KEY", "")

	// First start: a key is created with mode 0600.
	st, _, err := openStore(data, keyFile, quiet)
	if err != nil {
		t.Fatal(err)
	}
	st.SaveWebhook(context.Background(), &store.Webhook{App: "web", Secret: "s", CreatedAt: time.Now()})
	st.Close()
	if fi, err := os.Stat(keyFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}

	// Restart with the key: fine.
	st, _, err = openStore(data, keyFile, quiet)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	// The key file goes missing: refuse, and don't create a replacement.
	os.Rename(keyFile, keyFile+".bak")
	if _, _, err := openStore(data, keyFile, quiet); err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("a missing key for an encrypted database must be refused: %v", err)
	}
	if _, err := os.Stat(keyFile); err == nil {
		t.Fatal("a new key must not be left behind for an encrypted database")
	}
	os.Rename(keyFile+".bak", keyFile)

	// A key file other users can read is refused.
	os.Chmod(keyFile, 0o644)
	if _, _, err := openStore(data, keyFile, quiet); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("loose permissions: %v", err)
	}
	os.Chmod(keyFile, 0o600)
}

func TestKeyRotatorRefusesEnvKey(t *testing.T) {
	r := &keyRotator{fromEnv: true}
	if _, err := r.rotate(context.Background()); err == nil || !strings.Contains(err.Error(), "COLDSHIP_SECRET_KEY") {
		t.Fatalf("env keys can't be rotated in place: %v", err)
	}
}

func TestNewKeyKeptOnOtherErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COLDSHIP_SECRET_KEY", "")
	keyFile := filepath.Join(dir, "coldship.key")
	data := filepath.Join(dir, "missing-dir", "coldship.db") // can't be created
	if _, _, err := openStore(data, keyFile, quiet); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(keyFile); err != nil {
		t.Fatal("a new key file must only be removed when the key provably wasn't used")
	}
}
