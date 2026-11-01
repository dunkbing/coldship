package update

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"
)

// Status is what the dashboard and CLI show about updates.
type Status struct {
	Version         string     `json:"version"`
	Enabled         bool       `json:"enabled"`
	DisabledReason  string     `json:"disabled_reason,omitempty"`
	Latest          string     `json:"latest,omitempty"`
	UpdateAvailable bool       `json:"update_available"`
	Notes           string     `json:"notes,omitempty"`
	NotesURL        string     `json:"notes_url,omitempty"`
	PublishedAt     *time.Time `json:"published_at,omitempty"`
	AutoUpgrade     bool       `json:"auto_upgrade"`
	// AutoBlocked explains why an available update won't be installed
	// automatically (e.g. a major version change).
	AutoBlocked  string     `json:"auto_blocked,omitempty"`
	LastChecked  *time.Time `json:"last_checked,omitempty"`
	CheckError   string     `json:"check_error,omitempty"`
	State        string     `json:"upgrade_state"` // idle | waiting | downloading | installing | restarting
	UpgradeError string     `json:"upgrade_error,omitempty"`
}

// Checker periodically checks for releases and, if enabled, installs them
// when the server is idle.
type Checker struct {
	URL       string
	PublicKey ed25519.PublicKey
	Current   string
	Interval  time.Duration
	Auto      bool
	// Busy reports whether deployments are in progress; upgrades wait.
	Busy func() bool
	// Restart replaces the running process with the binary at path.
	Restart func(path string)
	Log     *slog.Logger
	// Target is the binary to replace (default: the running executable).
	Target string

	mu       sync.Mutex
	latest   *Manifest
	raw      []byte
	checked  time.Time
	checkErr string
	state    string
	upErr    string
	wake     chan struct{}
}

func (c *Checker) disabledReason() string {
	switch {
	case c.URL == "":
		return "no update URL configured (coldship server --update-url)"
	case c.PublicKey == nil:
		return "no update signing key built in or configured (--update-public-key), so releases cannot be verified"
	}
	return ""
}

func (c *Checker) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := Status{Version: c.Current, AutoUpgrade: c.Auto, State: c.state, UpgradeError: c.upErr, CheckError: c.checkErr}
	if st.State == "" {
		st.State = "idle"
	}
	if r := c.disabledReason(); r != "" {
		st.DisabledReason = r
		return st
	}
	st.Enabled = true
	if !c.checked.IsZero() {
		t := c.checked
		st.LastChecked = &t
	}
	if m := c.latest; m != nil {
		st.Latest, st.Notes, st.NotesURL = m.Version, m.Notes, m.NotesURL
		if !m.PublishedAt.IsZero() {
			t := m.PublishedAt
			st.PublishedAt = &t
		}
		st.UpdateAvailable = Newer(m.Version, c.Current)
		if st.UpdateAvailable && c.Auto && !SameMajor(m.Version, c.Current) {
			st.AutoBlocked = "major version change: install it manually with `coldship upgrade` after reading the release notes"
		}
	}
	return st
}

// Published returns the latest verified manifest as fetched, or nil.
func (c *Checker) Published() *Published {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.raw == nil {
		return nil
	}
	return &Published{URL: c.URL, Signed: c.raw}
}

func (c *Checker) Check(ctx context.Context) error {
	if r := c.disabledReason(); r != "" {
		return fmt.Errorf("updates are disabled: %s", r)
	}
	m, raw, err := Fetch(ctx, c.URL, c.PublicKey)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checked = time.Now()
	if err != nil {
		c.checkErr = err.Error()
		return err
	}
	c.latest, c.raw, c.checkErr = m, raw, ""
	return nil
}

// Run checks now and then every Interval until ctx ends, upgrading
// automatically when allowed.
func (c *Checker) Run(ctx context.Context) {
	if c.disabledReason() != "" {
		c.Log.Info("update checks disabled", "reason", c.disabledReason())
		return
	}
	c.mu.Lock()
	c.wake = make(chan struct{}, 1)
	c.mu.Unlock()
	for {
		if err := c.Check(ctx); err != nil && ctx.Err() == nil {
			c.Log.Warn("update check failed", "err", err)
		}
		if st := c.Status(); st.UpdateAvailable {
			c.Log.Info("new Coldship version available", "current", c.Current, "latest", st.Latest, "auto", c.Auto && st.AutoBlocked == "")
			if c.Auto && st.AutoBlocked == "" {
				if err := c.Upgrade(ctx, true); err != nil && ctx.Err() == nil {
					c.Log.Error("automatic upgrade failed", "err", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.Interval):
		case <-c.wake:
		}
	}
}

// Upgrade installs the latest release and restarts. With waitIdle it first
// waits until no deployment is in progress.
func (c *Checker) Upgrade(ctx context.Context, waitIdle bool) error {
	c.mu.Lock()
	m := c.latest
	if c.state != "" && c.state != "idle" {
		c.mu.Unlock()
		return fmt.Errorf("an upgrade is already %s", c.state)
	}
	if m == nil || !Newer(m.Version, c.Current) {
		c.mu.Unlock()
		return fmt.Errorf("no newer version available (running %s)", c.Current)
	}
	c.state, c.upErr = "waiting", ""
	c.mu.Unlock()

	err := c.upgrade(ctx, m, waitIdle)
	c.mu.Lock()
	if err != nil {
		c.state, c.upErr = "idle", err.Error()
	}
	c.mu.Unlock()
	return err
}

func (c *Checker) setState(s string) {
	c.mu.Lock()
	c.state = s
	c.mu.Unlock()
}

func (c *Checker) upgrade(ctx context.Context, m *Manifest, waitIdle bool) error {
	for waitIdle && c.Busy != nil && c.Busy() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	target := c.Target
	if target == "" {
		var err error
		if target, err = Executable(); err != nil {
			return err
		}
	}
	c.setState("downloading")
	c.Log.Info("downloading Coldship", "version", m.Version)
	newBin, err := Download(ctx, m, filepath.Dir(target))
	if err != nil {
		return err
	}
	c.setState("installing")
	if err := Install(ctx, newBin, target, m.Version); err != nil {
		return err
	}
	c.setState("restarting")
	c.Log.Info("installed Coldship; restarting", "version", m.Version, "previous_binary", target+".old")
	if c.Restart != nil {
		go c.Restart(target)
	}
	return nil
}

// CheckSoon wakes the Run loop for an immediate check.
func (c *Checker) CheckSoon() {
	c.mu.Lock()
	w := c.wake
	c.mu.Unlock()
	if w != nil {
		select {
		case w <- struct{}{}:
		default:
		}
	}
}
