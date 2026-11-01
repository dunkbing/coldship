package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func serveRelease(t *testing.T, version string, bin []byte, shaOverride string) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(bin)
	sha := hex.EncodeToString(sum[:])
	if shaOverride != "" {
		sha = shaOverride
	}
	manifest, err := json.Marshal(Manifest{Version: version, Notes: "faster deploys",
		Assets: map[string]Asset{Platform(): {URL: "bin/coldship", SHA256: sha, Size: int64(len(bin))}}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest.json", func(w http.ResponseWriter, _ *http.Request) { w.Write(manifest) })
	mux.HandleFunc("/releases/bin/coldship", func(w http.ResponseWriter, _ *http.Request) { w.Write(bin) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchDownloadInstall(t *testing.T) {
	bin := fakeBinary(t, "0.2.0")
	srv := serveRelease(t, "0.2.0", bin, "")
	ctx := context.Background()

	m, err := Fetch(ctx, srv.URL+"/releases/latest.json")
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
	srv := serveRelease(t, "0.2.0", fakeBinary(t, "0.2.0"), strings.Repeat("0", 64))
	m, err := Fetch(context.Background(), srv.URL+"/releases/latest.json")
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
	srv := serveRelease(t, "1.0.0", fakeBinary(t, "1.0.0"), "")
	c := &Checker{URL: srv.URL + "/releases/latest.json", Current: "0.9.0", Auto: true,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := c.Status()
	if !st.UpdateAvailable || st.Latest != "1.0.0" || st.Notes != "faster deploys" || st.AutoBlocked == "" {
		t.Fatalf("a major upgrade must be available but not automatic: %+v", st)
	}

	disabled := &Checker{Current: "0.9.0"}
	if st := disabled.Status(); st.Enabled || !strings.Contains(st.DisabledReason, "update URL") {
		t.Fatalf("builds without an update URL must not update: %+v", st)
	}
}

func TestCheckerUpgradeWaitsForIdle(t *testing.T) {
	srv := serveRelease(t, "0.2.0", fakeBinary(t, "0.2.0"), "")
	dir := t.TempDir()
	target := filepath.Join(dir, "coldship")
	os.WriteFile(target, fakeBinary(t, "0.1.0"), 0o755)

	var busy atomic.Bool
	busy.Store(true)
	restarted := make(chan string, 1)
	c := &Checker{URL: srv.URL + "/releases/latest.json", Current: "0.1.0", Target: target,
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
