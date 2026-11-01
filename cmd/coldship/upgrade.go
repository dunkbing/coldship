package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

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

func upgradeCmd() *cobra.Command {
	var check, noWait bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade the Coldship server to the latest release (admin)",
		Long: "Upgrade the Coldship server to the latest release. The server waits for running\n" +
			"deployments to finish, verifies and installs the new binary, and restarts itself.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := newClient()
			if check {
				st, err := c.SystemCheck(cmd.Context())
				if err != nil {
					return err
				}
				clearNoticeCache()
				return emit(st, func() {
					if st.UpdateAvailable {
						fmt.Printf("Coldship %s is available (server runs %s). Run `coldship upgrade` to install it.\n", st.Latest, st.Version)
					} else {
						fmt.Printf("The server runs the latest version (%s).\n", st.Version)
					}
				})
			}
			st, err := c.SystemUpgrade(cmd.Context())
			if ae, ok := apierr.As(err); ok && ae.Code == "server_unreachable" {
				return ae.WithHint("`coldship upgrade` asks a running server to upgrade itself. " +
					"With the server stopped, update this binary with `coldship self-update`, then start the server again")
			}
			if err != nil {
				return err
			}
			target := st.Latest
			if !flagJSON {
				fmt.Printf("Upgrading the server from %s to %s. It waits for running deployments, then restarts.\n", st.Version, target)
			}
			if noWait {
				return emit(st, nil)
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
					clearNoticeCache()
					return emit(cur, func() { fmt.Printf("The server now runs %s.\n", cur.Version) })
				}
				if cur.State == "idle" && cur.UpgradeError != "" {
					return apierr.New(500, "upgrade_failed", "%s", cur.UpgradeError).
						WithHint("the server kept running the current version")
				}
			}
			return apierr.New(504, "upgrade_timeout", "the server did not report version %s in time", target)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only check for a new version now")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "start the upgrade and return without waiting for the restart")
	return cmd
}

func selfUpdateCmd() *cobra.Command {
	var manifestURL string
	cmd := &cobra.Command{
		Use:   "self-update",
		Short: "Update this coldship binary to the latest release (like bun upgrade)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if manifestURL == "" {
				manifestURL = defaultUpdateURL
			}
			if manifestURL == "" {
				return apierr.New(400, "updates_disabled", "this coldship was built from source, so it doesn't know where releases are").
					WithHint("install a release with install.sh, or pass --manifest-url")
			}
			m, err := update.Fetch(cmd.Context(), manifestURL)
			if err != nil {
				return err
			}
			if !update.Newer(m.Version, version) {
				return emit(map[string]any{"version": version, "latest": m.Version, "updated": false}, func() {
					fmt.Printf("coldship %s is up to date.\n", version)
				})
			}
			target, err := update.Executable()
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Downloading coldship %s for %s…\n", m.Version, update.Platform())
			bin, err := update.Download(cmd.Context(), m, filepath.Dir(target))
			if err != nil {
				return err
			}
			if err := update.Install(cmd.Context(), bin, target, m.Version); err != nil {
				return err
			}
			clearNoticeCache()
			return emit(map[string]any{"version": m.Version, "previous": version, "updated": true, "backup": target + ".old"}, func() {
				fmt.Printf("Updated %s from %s to %s (previous binary kept as %s.old).\n", target, version, m.Version, target)
			})
		},
	}
	cmd.Flags().StringVar(&manifestURL, "manifest-url", "", "release manifest to use instead of the built-in one")
	return cmd
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
		lines = append(lines, fmt.Sprintf("This coldship CLI (%s) is older than the server (%s): run `coldship self-update`.", version, st.Version))
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

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
