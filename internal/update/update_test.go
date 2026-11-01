package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func keys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestSignVerify(t *testing.T) {
	pub, priv := keys(t)
	signed, err := Sign(Manifest{Version: "1.2.3", Assets: map[string]Asset{"linux/arm64": {URL: "x", SHA256: "ab"}}}, priv)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Verify(signed, pub)
	if err != nil || m.Version != "1.2.3" {
		t.Fatalf("verify: %v %+v", err, m)
	}
	// Any change to the manifest breaks the signature.
	tampered := bytes.Replace(signed, []byte(`"1.2.3"`), []byte(`"9.9.9"`), 1)
	if _, err := Verify(tampered, pub); err == nil {
		t.Fatal("a tampered manifest must be rejected")
	}
	otherPub, _ := keys(t)
	if _, err := Verify(signed, otherPub); err == nil {
		t.Fatal("a manifest signed with another key must be rejected")
	}
	if _, err := Sign(Manifest{Version: "latest"}, priv); err == nil {
		t.Fatal("non-semver versions must be refused")
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"0.2.0", "0.1.0", true},
		{"0.1.0", "0.1.0-dev", true},
		{"0.1.0", "0.1.0", false},
		{"0.1.0", "0.2.0", false},
		{"v1.0.0", "0.9.9", true},
		{"0.2.0", "dev", false}, // unversioned builds are never auto-replaced
	}
	for _, c := range cases {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.latest, c.current, got)
		}
	}
	if SameMajor("2.0.0", "1.9.0") || !SameMajor("1.4.0", "1.0.0") {
		t.Error("SameMajor")
	}
}

// fakeBinary is a shell script that answers --version like coldship.
func fakeBinary(t *testing.T, version string) []byte {
	t.Helper()
	return []byte("#!/bin/sh\necho \"coldship version " + version + "\"\n")
}

func serveRelease(t *testing.T, priv ed25519.PrivateKey, version string, bin []byte, shaOverride string) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(bin)
	sha := hex.EncodeToString(sum[:])
	if shaOverride != "" {
		sha = shaOverride
	}
	signed, err := Sign(Manifest{Version: version, Notes: "faster deploys",
		Assets: map[string]Asset{Platform(): {URL: "bin/coldship", SHA256: sha, Size: int64(len(bin))}}}, priv)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest.json", func(w http.ResponseWriter, _ *http.Request) { w.Write(signed) })
	mux.HandleFunc("/releases/bin/coldship", func(w http.ResponseWriter, _ *http.Request) { w.Write(bin) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchDownloadInstall(t *testing.T) {
	pub, priv := keys(t)
	bin := fakeBinary(t, "0.2.0")
	srv := serveRelease(t, priv, "0.2.0", bin, "")
	ctx := context.Background()

	m, _, err := Fetch(ctx, srv.URL+"/releases/latest.json", pub)
	if err != nil {
		t.Fatal(err)
	}
	if m.Assets[Platform()].URL != srv.URL+"/releases/bin/coldship" {
		t.Fatalf("relative asset URLs should resolve against the manifest: %s", m.Assets[Platform()].URL)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "coldship")
	os.WriteFile(target, fakeBinary(t, "0.1.0"), 0o755)

	newBin, err := Download(ctx, m, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(ctx, newBin, target, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	old, _ := os.ReadFile(target + ".old")
	if !bytes.Contains(got, []byte("0.2.0")) || !bytes.Contains(old, []byte("0.1.0")) {
		t.Fatalf("target should be the new binary with the old one kept as .old")
	}
}

func TestDownloadRejectsWrongHash(t *testing.T) {
	pub, priv := keys(t)
	srv := serveRelease(t, priv, "0.2.0", fakeBinary(t, "0.2.0"), strings.Repeat("0", 64))
	m, _, err := Fetch(context.Background(), srv.URL+"/releases/latest.json", pub)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := Download(context.Background(), m, dir); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("want a SHA-256 mismatch, got %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("a rejected download must be deleted")
	}
}

func TestInstallRejectsBinaryThatDoesNotRun(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "coldship")
	os.WriteFile(target, fakeBinary(t, "0.1.0"), 0o755)
	bad := filepath.Join(dir, "new")
	os.WriteFile(bad, fakeBinary(t, "0.1.9"), 0o755) // reports the wrong version
	if err := Install(context.Background(), bad, target, "0.2.0"); err == nil {
		t.Fatal("a binary that doesn't report the expected version must not be installed")
	}
	if got, _ := os.ReadFile(target); !bytes.Contains(got, []byte("0.1.0")) {
		t.Fatal("the current binary must be left alone")
	}
}

func TestCheckerStatusAndAutoBlock(t *testing.T) {
	pub, priv := keys(t)
	srv := serveRelease(t, priv, "1.0.0", fakeBinary(t, "1.0.0"), "")
	c := &Checker{URL: srv.URL + "/releases/latest.json", PublicKey: pub, Current: "0.9.0", Auto: true,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := c.Status()
	if !st.UpdateAvailable || st.Latest != "1.0.0" || st.Notes != "faster deploys" || st.AutoBlocked == "" {
		t.Fatalf("a major upgrade must be available but not automatic: %+v", st)
	}

	disabled := &Checker{URL: srv.URL, Current: "0.9.0"}
	if st := disabled.Status(); st.Enabled || !strings.Contains(st.DisabledReason, "signing key") {
		t.Fatalf("without a key, updates must be disabled: %+v", st)
	}
}

func TestCheckerUpgradeWaitsForIdle(t *testing.T) {
	pub, priv := keys(t)
	srv := serveRelease(t, priv, "0.2.0", fakeBinary(t, "0.2.0"), "")
	dir := t.TempDir()
	target := filepath.Join(dir, "coldship")
	os.WriteFile(target, fakeBinary(t, "0.1.0"), 0o755)

	var busy atomic.Bool
	busy.Store(true)
	restarted := make(chan string, 1)
	c := &Checker{URL: srv.URL + "/releases/latest.json", PublicKey: pub, Current: "0.1.0", Target: target,
		Busy: busy.Load, Restart: func(p string) { restarted <- p }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Upgrade(context.Background(), true) }()

	time.Sleep(300 * time.Millisecond)
	if st := c.Status(); st.State != "waiting" {
		t.Fatalf("while deployments run the upgrade must wait, state=%s", st.State)
	}
	if got, _ := os.ReadFile(target); !bytes.Contains(got, []byte("0.1.0")) {
		t.Fatal("nothing may be installed while busy")
	}
	busy.Store(false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("upgrade did not proceed once idle")
	}
	select {
	case p := <-restarted:
		if p != target {
			t.Fatalf("restart path = %s", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restart was not triggered")
	}
	if got, _ := os.ReadFile(target); !bytes.Contains(got, []byte("0.2.0")) {
		t.Fatal("the new binary should be installed")
	}
	if st := c.Status(); st.State != "restarting" {
		t.Fatalf("state = %s", st.State)
	}
}
