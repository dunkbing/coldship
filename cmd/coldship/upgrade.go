package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/update"
)

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the CLI and server versions, and whether an update is available",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := map[string]any{"cli": version, "platform": update.Platform()}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			st, err := newClient().System(ctx)
			if err == nil {
				out["server"] = st
			} else {
				out["server_error"] = err.Error()
			}
			return emit(out, func() {
				fmt.Printf("coldship CLI %s (%s)\n", version, update.Platform())
				if err != nil {
					fmt.Printf("server: unreachable (%v)\n", err)
					return
				}
				fmt.Printf("server %s\n", st.Version)
				for _, l := range noticeLines(st) {
					fmt.Println(l)
				}
				switch {
				case !st.Enabled:
					fmt.Printf("Server updates are off: %s\n", st.DisabledReason)
				case st.CheckError != "":
					fmt.Printf("The last update check failed: %s\n", st.CheckError)
				case st.LastChecked == nil:
					fmt.Println("The server has not checked for updates yet.")
				case !st.UpdateAvailable:
					fmt.Printf("The server is up to date (checked %s ago).\n", time.Since(*st.LastChecked).Round(time.Second))
				}
			})
		},
	}
}

// upgradeCmd updates everything the user runs: the server, when one is
// reachable, and this CLI binary.
func upgradeCmd() *cobra.Command {
	var check, noWait, cliOnly bool
	var manifestURL string
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade Coldship to the latest release: the server, if one is running, and this CLI",
		Long: `Upgrade Coldship to the latest release.

If a server is running at COLDSHIP_URL, it is upgraded first (admin token): it
waits for running deployments, installs the new version and restarts itself.
Then this coldship binary is updated, using sudo if it lives in a folder only
root can write, such as /usr/local/bin.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if manifestURL == "" {
				manifestURL = defaultUpdateURL
			}
			out := map[string]any{}
			if !cliOnly {
				srv, err := upgradeServer(cmd, check, noWait)
				if err != nil {
					return err
				}
				out["server"] = srv
			}
			cli, err := upgradeCLI(cmd, manifestURL, check)
			if err != nil {
				return err
			}
			out["cli"] = cli
			clearNoticeCache()
			return emit(out, func() {})
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only check for a new version")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "start the server upgrade and return without waiting for the restart")
	cmd.Flags().StringVar(&manifestURL, "manifest-url", "", "release manifest to use instead of the built-in one")
	cmd.Flags().BoolVar(&cliOnly, "cli-only", false, "update only this binary, not the server")
	return cmd
}

// selfUpdateCmd is the old name of `upgrade --cli-only`, kept for scripts.
func selfUpdateCmd() *cobra.Command {
	cmd := upgradeCmd()
	cmd.Use, cmd.Hidden = "self-update", true
	cmd.Short = "Update this coldship binary (same as upgrade --cli-only)"
	cmd.Flags().Set("cli-only", "true")
	return cmd
}

func say(format string, a ...any) {
	if !flagJSON {
		fmt.Printf(format+"\n", a...)
	}
}

// upgradeServer upgrades the server at COLDSHIP_URL, if one is running.
func upgradeServer(cmd *cobra.Command, check, noWait bool) (map[string]any, error) {
	c := newClient()
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	st, err := c.SystemCheck(ctx) // admin: checks for a new version now
	cancel()
	if ae, ok := apierr.As(err); ok && ae.Code == "insufficient_scope" {
		st, err = c.System(cmd.Context())
	}
	if ae, ok := apierr.As(err); ok {
		switch ae.Code {
		case "server_unreachable":
			say("No Coldship server is running at %s, so only this CLI is checked.", c.BaseURL)
			return map[string]any{"reachable": false}, nil
		case "updates_disabled":
			say("The server at %s was built from source, so it doesn't update itself.", c.BaseURL)
			return map[string]any{"reachable": true, "skipped": "built from source"}, nil
		case "unauthenticated":
			say("Skipping the server at %s: set COLDSHIP_TOKEN to an admin token to upgrade it.", c.BaseURL)
			return map[string]any{"reachable": true, "skipped": "no token"}, nil
		}
	}
	if err != nil {
		return nil, err
	}
	switch {
	case !st.UpdateAvailable:
		say("The server is up to date (%s).", st.Version)
		return map[string]any{"version": st.Version, "updated": false}, nil
	case check:
		say("The server runs %s; %s is available.", st.Version, st.Latest)
		return map[string]any{"version": st.Version, "latest": st.Latest, "updated": false}, nil
	}
	st, err = c.SystemUpgrade(cmd.Context())
	if err != nil {
		return nil, err
	}
	from, target := st.Version, st.Latest
	say("Upgrading the server from %s to %s. It waits for running deployments, then restarts…", from, target)
	if noWait {
		return map[string]any{"version": from, "latest": target, "updated": false, "started": true}, nil
	}
	deadline := time.Now().Add(45 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
		cur, err := c.System(ctx)
		cancel()
		if err != nil {
			continue // restarting
		}
		if cur.Version == target {
			say("The server now runs %s.", cur.Version)
			return map[string]any{"version": cur.Version, "previous": from, "updated": true}, nil
		}
		if cur.State == "idle" && cur.UpgradeError != "" {
			return nil, apierr.New(500, "upgrade_failed", "%s", cur.UpgradeError).
				WithHint("the server kept running the current version")
		}
	}
	return nil, apierr.New(504, "upgrade_timeout", "the server did not report version %s in time", target)
}

// upgradeCLI updates this binary to the latest release.
func upgradeCLI(cmd *cobra.Command, manifestURL string, check bool) (map[string]any, error) {
	if manifestURL == "" {
		say("This CLI was built from source, so it doesn't update itself.")
		return map[string]any{"version": version, "updated": false, "reason": "built from source"}, nil
	}
	target, err := update.Executable()
	if err != nil {
		return nil, err
	}
	m, err := update.Fetch(cmd.Context(), manifestURL)
	if err != nil {
		return nil, err
	}
	// The server may have just replaced this same file (a service on this
	// machine shares it), so ask the file, not this process.
	cur := diskVersion(target)
	switch {
	case !update.Newer(m.Version, cur):
		say("The CLI is up to date (%s).", cur)
		return map[string]any{"version": cur, "updated": false}, nil
	case check:
		say("The CLI is %s; %s is available.", cur, m.Version)
		return map[string]any{"version": cur, "latest": m.Version, "updated": false}, nil
	case !canWrite(filepath.Dir(target)):
		return nil, upgradeAsRoot(cmd, target, manifestURL)
	}
	fmt.Fprintf(os.Stderr, "Downloading coldship %s for %s…\n", m.Version, update.Platform())
	bin, err := update.Download(cmd.Context(), m, filepath.Dir(target))
	if err != nil {
		return nil, err
	}
	if err := update.Install(cmd.Context(), bin, target, m.Version); err != nil {
		return nil, err
	}
	say("Updated the CLI (%s) from %s to %s.", target, cur, m.Version)
	return map[string]any{"version": m.Version, "previous": cur, "updated": true, "backup": target + ".old"}, nil
}

// diskVersion is the version of the coldship binary at path, or this
// process's version if it can't be run.
func diskVersion(path string) string {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return version
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return version
	}
	return f[len(f)-1]
}

// --- release tooling ---

func releaseCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "release", Short: "Release tooling (for maintainers)"}

	var relVersion, notes, notesURL, baseURL, out string
	manifest := &cobra.Command{
		Use:   "manifest GOOS/GOARCH=path [...]",
		Short: "Write the release manifest (latest.json) for the given binaries",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m := update.Manifest{Version: strings.TrimPrefix(relVersion, "v"), PublishedAt: time.Now().UTC().Truncate(time.Second),
				Notes: notes, NotesURL: notesURL, Assets: map[string]update.Asset{}}
			for _, a := range args {
				plat, path, ok := strings.Cut(a, "=")
				if !ok || strings.Count(plat, "/") != 1 {
					return fmt.Errorf("asset %q must look like linux/arm64=dist/coldship-linux-arm64", a)
				}
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				h := sha256.New()
				n, err := io.Copy(h, f)
				f.Close()
				if err != nil {
					return err
				}
				u := filepath.Base(path)
				if baseURL != "" {
					u = strings.TrimRight(baseURL, "/") + "/" + u
				}
				m.Assets[plat] = update.Asset{URL: u, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}
			}
			if err := m.Validate(); err != nil {
				return err
			}
			out_, err := json.MarshalIndent(m, "", "  ")
			if err != nil {
				return err
			}
			if out == "" || out == "-" {
				_, err = os.Stdout.Write(append(out_, '\n'))
				return err
			}
			return os.WriteFile(out, append(out_, '\n'), 0o644)
		},
	}
	manifest.Flags().StringVar(&relVersion, "version", "", "release version, e.g. 0.2.0")
	manifest.Flags().StringVar(&notes, "notes", "", "short release notes shown in the dashboard and CLI")
	manifest.Flags().StringVar(&notesURL, "notes-url", "", "link to full release notes")
	manifest.Flags().StringVar(&baseURL, "base-url", "", "URL the binaries are served from (default: next to the manifest)")
	manifest.Flags().StringVarP(&out, "out", "o", "", "write the manifest here instead of stdout")
	manifest.MarkFlagRequired("version")

	cmd.AddCommand(manifest)
	return cmd
}

// --- "new version available" note after CLI commands ---

type noticeCache struct {
	CheckedAt time.Time     `json:"checked_at"`
	Status    update.Status `json:"status"`
}

func noticePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "coldship", "update-notice.json")
}

func clearNoticeCache() {
	if p := noticePath(); p != "" {
		os.Remove(p)
	}
}

func noticeLines(st *update.Status) []string {
	var lines []string
	if st.UpdateAvailable {
		l := fmt.Sprintf("A new Coldship version is available: %s (the server runs %s).", st.Latest, st.Version)
		switch {
		case st.State != "" && st.State != "idle":
			l += " It is being installed now."
		case st.AutoUpgrade && st.AutoBlocked == "":
			l += " It will be installed automatically when no deployment is running."
		case st.AutoBlocked != "":
			l += " It is a major version change: read the release notes, then run `coldship upgrade`."
		default:
			l += " Run `coldship upgrade` to install it."
		}
		lines = append(lines, l)
		if st.NotesURL != "" {
			lines = append(lines, "Release notes: "+st.NotesURL)
		} else if st.Notes != "" {
			lines = append(lines, "What's new: "+st.Notes)
		}
	}
	if update.Newer(st.Version, version) {
		lines = append(lines, fmt.Sprintf("This coldship CLI (%s) is older than the server (%s): run `coldship upgrade`.", version, st.Version))
	}
	return lines
}

// maybeNotice prints update notes to stderr after a command. It never runs
// with --json or when stderr isn't a terminal, so scripts and agents get
// clean output, and it asks the server at most once an hour.
func maybeNotice(cmd *cobra.Command) {
	switch cmd.Name() {
	case "server", "mcp", "upgrade", "self-update", "version", "manifest", "completion", "help":
		return
	}
	if flagJSON || !isTerminal(os.Stderr) || os.Getenv("COLDSHIP_NO_UPDATE_NOTICE") != "" {
		return
	}
	c := newClient()
	path := noticePath()
	var cache map[string]noticeCache
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			json.Unmarshal(b, &cache)
		}
	}
	if cache == nil {
		cache = map[string]noticeCache{}
	}
	entry, ok := cache[c.BaseURL]
	if !ok || time.Since(entry.CheckedAt) > time.Hour {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		st, err := c.System(ctx)
		cancel()
		if err != nil {
			return
		}
		entry = noticeCache{CheckedAt: time.Now(), Status: *st}
		cache[c.BaseURL] = entry
		if path != "" {
			os.MkdirAll(filepath.Dir(path), 0o700)
			if b, err := json.Marshal(cache); err == nil {
				os.WriteFile(path, b, 0o600)
			}
		}
	}
	lines := noticeLines(&entry.Status)
	if len(lines) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr)
	for _, l := range lines {
		fmt.Fprintln(os.Stderr, "ⓘ "+l)
	}
}

// isTerminal reports whether f is an interactive terminal (not just any
// character device, such as /dev/null).
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// canWrite reports whether this user can create files in dir.
func canWrite(dir string) bool {
	f, err := os.CreateTemp(dir, ".coldship-write-check-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// upgradeAsRoot re-runs the CLI upgrade with sudo when the binary lives in a
// folder only root can write, such as /usr/local/bin.
func upgradeAsRoot(cmd *cobra.Command, target, manifestURL string) error {
	sudo, err := exec.LookPath("sudo")
	if os.Geteuid() == 0 || err != nil || !isTerminal(os.Stdin) || flagJSON {
		return apierr.New(403, "permission_denied", "this user can't replace %s", target).
			WithHint("run: sudo " + target + " upgrade --cli-only")
	}
	fmt.Fprintf(os.Stderr, "%s belongs to root, so updating it needs sudo.\n", target)
	args := []string{target, "upgrade", "--cli-only"}
	if manifestURL != defaultUpdateURL {
		args = append(args, "--manifest-url", manifestURL)
	}
	c := exec.CommandContext(cmd.Context(), sudo, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}
