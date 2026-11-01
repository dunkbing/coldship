// Package update checks for, verifies and installs new Coldship releases.
//
// A release is described by a signed manifest:
//
//	{"manifest": {"version": "0.2.0", "assets": {"linux/arm64": {"url": ..., "sha256": ...}}, ...},
//	 "signature": "<base64 ed25519 signature of the compacted manifest JSON>"}
//
// The public key is built into the binary. Nothing is installed unless the
// manifest signature, the asset's SHA-256 and the new binary's own --version
// output all check out.
package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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

type Envelope struct {
	Manifest  json.RawMessage `json:"manifest"`
	Signature string          `json:"signature"`
}

// Platform is this binary's asset key, e.g. "linux/arm64".
func Platform() string { return goruntime.GOOS + "/" + goruntime.GOARCH }

// ParsePublicKey decodes a base64 Ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("update public key must be a base64-encoded 32-byte Ed25519 key")
	}
	return ed25519.PublicKey(b), nil
}

func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PrivateKeySize {
		return nil, errors.New("update private key must be a base64-encoded 64-byte Ed25519 key")
	}
	return ed25519.PrivateKey(b), nil
}

// Sign produces a signed envelope for m.
func Sign(m Manifest, priv ed25519.PrivateKey) ([]byte, error) {
	if !semver.IsValid(v(m.Version)) {
		return nil, fmt.Errorf("version %q is not semver (e.g. 1.4.2)", m.Version)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	env := Envelope{Manifest: raw, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw))}
	return json.MarshalIndent(env, "", "  ")
}

// Verify checks the envelope's signature and returns the manifest.
func Verify(data []byte, pub ed25519.PublicKey) (*Manifest, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("update manifest is not valid JSON: %w", err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, env.Manifest); err != nil {
		return nil, fmt.Errorf("update manifest is not valid JSON: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signature)
	if err != nil || !ed25519.Verify(pub, compact.Bytes(), sig) {
		return nil, errors.New("update manifest signature is invalid: refusing to trust it")
	}
	var m Manifest
	if err := json.Unmarshal(env.Manifest, &m); err != nil {
		return nil, err
	}
	if !semver.IsValid(v(m.Version)) {
		return nil, fmt.Errorf("update manifest version %q is not semver", m.Version)
	}
	return &m, nil
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

// Fetch downloads and verifies the manifest at manifestURL. Relative asset
// URLs are resolved against it. It also returns the raw signed bytes.
func Fetch(ctx context.Context, manifestURL string, pub ed25519.PublicKey) (*Manifest, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", manifestURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "coldship-update-check")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching update manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, nil, fmt.Errorf("fetching update manifest: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, err
	}
	m, err := Verify(data, pub)
	if err != nil {
		return nil, nil, err
	}
	if err := ResolveAssets(m, manifestURL); err != nil {
		return nil, nil, err
	}
	return m, data, nil
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

// Published is what a server hands to remote CLIs for self-update: the
// signed manifest exactly as fetched, and where it came from.
type Published struct {
	URL    string          `json:"url"`
	Signed json.RawMessage `json:"signed"`
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
		return fail(fmt.Errorf("downloaded binary has SHA-256 %s, but the signed manifest says %s: refusing to install", got, a.SHA256))
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
