// Package update checks for, downloads and installs new Coldship releases,
// the way `bun upgrade` does: releases come from GitHub over HTTPS.
//
// A release publishes latest.json next to its binaries:
//
//	{"version": "0.2.0", "assets": {"linux/arm64": {"url": "coldship-linux-arm64", "sha256": ...}}, ...}
//
// Nothing is installed unless the download matches its SHA-256 and the new
// binary runs and reports the expected version.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

type Asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Manifest struct {
	Version     string           `json:"version"`
	PublishedAt time.Time        `json:"published_at"`
	Notes       string           `json:"notes,omitempty"`
	NotesURL    string           `json:"notes_url,omitempty"`
	Assets      map[string]Asset `json:"assets"` // keyed by "GOOS/GOARCH"
}

// Platform is this binary's asset key, e.g. "linux/arm64".
func Platform() string { return goruntime.GOOS + "/" + goruntime.GOARCH }

// Validate checks a manifest's version.
func (m *Manifest) Validate() error {
	if !semver.IsValid(v(m.Version)) {
		return fmt.Errorf("release version %q is not semver (e.g. 1.4.2)", m.Version)
	}
	return nil
}

func v(s string) string {
	if strings.HasPrefix(s, "v") {
		return s
	}
	return "v" + s
}

// Newer reports whether latest is a newer version than current. An invalid
// current version (e.g. an unversioned dev build) is never upgraded.
func Newer(latest, current string) bool {
	if !semver.IsValid(v(current)) || !semver.IsValid(v(latest)) {
		return false
	}
	return semver.Compare(v(latest), v(current)) > 0
}

// SameMajor reports whether two versions share a major version. Automatic
// upgrades never cross a major version.
func SameMajor(a, b string) bool { return semver.Major(v(a)) == semver.Major(v(b)) }

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Fetch downloads the release manifest at manifestURL. Relative asset URLs
// are resolved against it.
func Fetch(ctx context.Context, manifestURL string) (*Manifest, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", manifestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "coldship-update-check")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching the release manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fetching the release manifest: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("the release manifest is not valid JSON: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if err := ResolveAssets(&m, manifestURL); err != nil {
		return nil, err
	}
	return &m, nil
}

// ResolveAssets makes relative asset URLs absolute against the manifest's
// URL (releases usually ship the binaries next to the manifest).
func ResolveAssets(m *Manifest, manifestURL string) error {
	base, err := url.Parse(manifestURL)
	if err != nil {
		return err
	}
	for k, a := range m.Assets {
		if u, err := base.Parse(a.URL); err == nil {
			a.URL = u.String()
			m.Assets[k] = a
		}
	}
	return nil
}

// Download fetches this platform's asset into dir and verifies its SHA-256.
func Download(ctx context.Context, m *Manifest, dir string) (string, error) {
	a, ok := m.Assets[Platform()]
	if !ok {
		return "", fmt.Errorf("release %s has no binary for %s", m.Version, Platform())
	}
	req, err := http.NewRequestWithContext(ctx, "GET", a.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", a.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("downloading %s: HTTP %d", a.URL, resp.StatusCode)
	}
	f, err := os.CreateTemp(dir, ".coldship-update-*")
	if err != nil {
		return "", fmt.Errorf("cannot write next to the current binary (%s): %w", dir, err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 512<<20))
	f.Close()
	fail := func(err error) (string, error) { os.Remove(f.Name()); return "", err }
	if err != nil {
		return fail(err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, a.SHA256) {
		return fail(fmt.Errorf("downloaded binary has SHA-256 %s, but the release manifest says %s: refusing to install", got, a.SHA256))
	}
	if a.Size > 0 && n != a.Size {
		return fail(fmt.Errorf("downloaded binary is %d bytes, manifest says %d", n, a.Size))
	}
	if err := os.Chmod(f.Name(), 0o755); err != nil {
		return fail(err)
	}
	return f.Name(), nil
}

// Install checks that newBin runs and reports wantVersion, then replaces
// target with it, keeping the previous binary as target+".old".
func Install(ctx context.Context, newBin, target, wantVersion string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, newBin, "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), wantVersion) {
		os.Remove(newBin)
		return fmt.Errorf("the downloaded binary did not run correctly (%v: %q): refusing to install", err, strings.TrimSpace(string(out)))
	}
	old := target + ".old"
	os.Remove(old)
	if err := os.Link(target, old); err != nil {
		// Hard links can fail across filesystems or on some mounts; copy.
		if err := copyFile(target, old); err != nil {
			os.Remove(newBin)
			return fmt.Errorf("could not back up %s: %w", target, err)
		}
	}
	if err := os.Rename(newBin, target); err != nil {
		os.Remove(newBin)
		return fmt.Errorf("could not replace %s: %w", target, err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Executable is the path of the running binary with symlinks resolved.
func Executable() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}
