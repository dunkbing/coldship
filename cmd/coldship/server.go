package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/dunkbing/coldship/internal/agents"
	"github.com/dunkbing/coldship/internal/api"
	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/builder"
	"github.com/dunkbing/coldship/internal/engine"
	"github.com/dunkbing/coldship/internal/mcpserver"
	"github.com/dunkbing/coldship/internal/proxy"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/store"
	"github.com/dunkbing/coldship/internal/update"
	"github.com/dunkbing/coldship/internal/web"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func serverCmd() *cobra.Command {
	var addr, data, gitCreds, buildDir, acmeEmail string
	var grace, buildTimeout time.Duration
	var proxyOn, autoUpgrade bool
	var updURL string
	var updInterval time.Duration
	var previewDomain string
	var previewTTL, previewMaxTTL time.Duration
	var maxPreviews int
	var snapshotDir string
	var snapshotKeep int
	var snapshotInterval time.Duration
	var publicURL string
	var keyFile string
	var httpPort, httpsPort int
	var adminTokenFile string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the Coldship server in this terminal (the installer runs it as a service instead)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			log := slog.New(slog.NewTextHandler(os.Stderr, nil))
			// Installed as a service, Coldship is already running: say that,
			// rather than a confusing error about Docker or the port.
			if alreadyRunning(addr) {
				hint := "it's already running, so there's nothing to start: use coldship commands directly (coldship apps, coldship deploy)"
				if _, err := os.Stat("/etc/systemd/system/coldship.service"); err == nil {
					hint += ". It runs as the coldship service: sudo systemctl status|restart|stop coldship"
				}
				return apierr.New(409, "already_running", "Coldship is already running at %s", addr).WithHint(hint)
			}
			// Without Docker nothing can run: say so now, with the fix,
			// before creating any files and instead of failing every deploy.
			rt := runtime.NewDocker()
			pingCtx, cancelPing := context.WithTimeout(cmd.Context(), 15*time.Second)
			err := rt.Ping(pingCtx)
			cancelPing()
			if err != nil {
				return err
			}
			podman := rt.IsPodman(cmd.Context())
			engineName := "docker"
			if podman {
				engineName = "podman"
			}
			log.Info("container engine", "engine", engineName, "host", rt.Host)
			buildCmd := "docker"
			if podman {
				buildCmd = "podman"
			}
			if _, err := exec.LookPath(buildCmd); err != nil {
				log.Warn("the " + buildCmd + " command isn't installed: apps from images work, but builds from Git need it")
			}
			if keyFile == "" {
				keyFile = defaultKeyFile(data)
			}
			st, kr, err := openStore(data, keyFile, log)
			if err != nil {
				return err
			}
			defer st.Close()
			if st.ScrubWarning != nil {
				log.Warn("secrets are encrypted, but old plaintext copies may remain in free space; retrying on next start", "err", st.ScrubWarning)
			}
			rotator := &keyRotator{st: st, kr: kr, keyFile: keyFile, fromEnv: os.Getenv("COLDSHIP_SECRET_KEY") != ""}
			rt.StartupGrace = grace
			bd := builder.NewGit()
			bd.CredentialsFile, bd.WorkDir, bd.Timeout = gitCreds, buildDir, buildTimeout
			if podman {
				bd.UsePodman()
			}
			var engRef *engine.Engine
			bd.GitHubToken = func(ctx context.Context) string {
				if engRef == nil {
					return ""
				}
				return engRef.GitHubToken(ctx)
			}
			var px proxy.Proxy
			if proxyOn {
				dataAbs, err := filepath.Abs(data)
				if err != nil {
					return err
				}
				px = proxy.NewCaddy(filepath.Join(filepath.Dir(dataAbs), "coldship-proxy"), httpPort, httpsPort, acmeEmail)
			}
			eng := engine.New(st, rt, bd, px)
			engRef = eng
			if snapshotDir == "" {
				dataAbs, err := filepath.Abs(data)
				if err != nil {
					return err
				}
				snapshotDir = filepath.Join(filepath.Dir(dataAbs), "coldship-snapshots")
			}
			eng.SnapshotDir, eng.SnapshotKeep, eng.SnapshotInterval = snapshotDir, snapshotKeep, snapshotInterval
			if abs, err := filepath.Abs(data); err == nil {
				eng.DataDir = filepath.Dir(abs)
			}
			eng.Previews = engine.PreviewConfig{Domain: previewDomain, DefaultTTL: previewTTL, MaxTTL: previewMaxTTL, Max: maxPreviews}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if tok, err := eng.EnsureAdminToken(ctx); err != nil {
				return err
			} else if tok != "" && adminTokenFile != "" {
				// For installers and services: keep the token out of logs.
				if err := os.WriteFile(adminTokenFile, []byte(tok+"\n"), 0o600); err != nil {
					return fmt.Errorf("writing the admin token to %s: %w", adminTokenFile, err)
				}
				log.Info("created the admin token", "file", adminTokenFile)
			} else if tok != "" {
				fmt.Fprintf(os.Stderr, "\nCreated admin token (shown once, store it safely):\n\n  %s\n\n"+
					"Give agents their own deploy-scoped token instead:\n  COLDSHIP_TOKEN=%s coldship token create --name my-agent --scope deploy\n\n", tok, tok)
			}

			if n, err := eng.RecoverDeployments(ctx); err != nil {
				return err
			} else if n > 0 {
				log.Warn("marked deployments interrupted by the last shutdown as failed", "count", n)
			}
			if n, err := eng.RecoverHooks(ctx); err != nil {
				return err
			} else if n > 0 {
				log.Warn("marked webhook deliveries interrupted by the last shutdown as failed", "count", n)
			}
			if proxyOn {
				if err := eng.StartProxy(ctx); err != nil {
					// Keep serving: apps without domains still work, and
					// diagnose reports proxy_down for apps that need it.
					log.Error("proxy not started; apps with domains are unreachable", "err", err)
				} else {
					log.Info("proxy running", "http_port", httpPort, "https_port", httpsPort)
				}
			}

			apiSrv := api.New(eng, st, log, web.Handler())
			// No WriteTimeout: deployments run inside requests. api.Server bounds
			// body reads instead.
			srv := &http.Server{Addr: addr, Handler: apiSrv, ReadHeaderTimeout: 10 * time.Second,
				IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 64 << 10}
			srv.RegisterOnShutdown(apiSrv.StopStreams)

			// AI agents run in containers and reach the API through a Unix
			// socket in a directory of its own, the only thing mounted in.
			if mgr, ln, err := startAgents(ctx, eng.DataDir, st, rt, log); err != nil {
				log.Warn("AI agents are off", "err", err)
			} else {
				apiSrv.SetAgents(mgr)
				go srv.Serve(ln)
			}

			// Self-upgrade: the checker installs a new binary, then calls
			// restart, which drains in-flight requests (deployments run
			// inside them) and re-executes the new binary in this process.
			var restartTo string
			drained := make(chan struct{})
			restart := func(path string) {
				restartTo = path
				shCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
				defer cancel()
				srv.Shutdown(shCtx)
				// Webhook builds run outside requests; let them finish too.
				eng.StopHooks(30 * time.Minute)
				close(drained)
			}
			checker := &update.Checker{URL: updURL, Current: version, Interval: updInterval, Auto: autoUpgrade,
				Busy: eng.Busy, Restart: restart, Log: log}
			apiSrv.SetUpdater(checker)
			apiSrv.SetPublicURL(publicURL)
			eng.SetPublicURL(publicURL)
			apiSrv.SetKeyRotation(rotator.rotate)
			go checker.Run(ctx)
			if snapshotInterval > 0 {
				go func() {
					t := time.NewTicker(snapshotInterval)
					defer t.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-t.C:
						}
						if n, err := eng.ScheduledSnapshots(ctx); err != nil {
							log.Error("scheduled database snapshots", "err", err, "done", n)
						} else if n > 0 {
							log.Info("scheduled database snapshots", "databases", n)
						}
					}
				}()
			}
			go eng.RunMetrics(ctx)
			go eng.RunBackups(ctx, log)
			go eng.RunNotifications(ctx, log)
			go eng.RunWatch(ctx)
			go func() {
				// Delete previews whose time is up.
				t := time.NewTicker(time.Minute)
				defer t.Stop()
				for {
					if deleted, err := eng.ReapPreviews(ctx); err != nil {
						log.Error("cleaning up previews", "err", err)
					} else if len(deleted) > 0 {
						log.Info("deleted expired previews", "previews", deleted)
					}
					select {
					case <-ctx.Done():
						return
					case <-t.C:
					}
				}
			}()

			go func() {
				<-ctx.Done()
				shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				srv.Shutdown(shCtx)
			}()
			log.Info("coldship listening", "addr", addr, "data", data, "version", version)
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			if restartTo == "" {
				// Plain shutdown: give webhook builds a minute to finish;
				// unfinished ones are marked interrupted on the next start.
				if !eng.StopHooks(time.Minute) {
					log.Warn("shutting down with webhook work still running")
				}
			}
			if restartTo != "" {
				<-drained
				st.Close()
				log.Info("restarting into the new version", "binary", restartTo)
				return syscall.Exec(restartTo, os.Args, os.Environ())
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", envOr("COLDSHIP_ADDR", "127.0.0.1:7070"), "listen address")
	cmd.Flags().StringVar(&data, "data", envOr("COLDSHIP_DATA", "coldship.db"), "SQLite state file")
	cmd.Flags().StringVar(&adminTokenFile, "admin-token-file", "", "on first start, write the admin token to this file (0600) instead of printing it")
	cmd.Flags().DurationVar(&grace, "startup-grace", 3*time.Second, "how long a new container must stay up to count as deployed")
	cmd.Flags().StringVar(&updURL, "update-url", envOr("COLDSHIP_UPDATE_URL", defaultUpdateURL), "release manifest to check for new versions")
	cmd.Flags().BoolVar(&autoUpgrade, "auto-upgrade", true, "install new minor and patch versions automatically when no deployment is running")
	cmd.Flags().DurationVar(&updInterval, "update-interval", 6*time.Hour, "how often to check for new versions")
	cmd.Flags().StringVar(&previewDomain, "preview-domain", envOr("COLDSHIP_PREVIEW_DOMAIN", ""), "give previews URLs under this domain (needs a wildcard DNS record *.<domain> pointing here)")
	cmd.Flags().DurationVar(&previewTTL, "preview-ttl", 24*time.Hour, "default lifetime of a preview")
	cmd.Flags().DurationVar(&previewMaxTTL, "preview-max-ttl", 7*24*time.Hour, "longest lifetime a preview may ask for")
	cmd.Flags().IntVar(&maxPreviews, "max-previews", 20, "most previews that may exist at once")
	cmd.Flags().StringVar(&keyFile, "secret-key-file", envOr("COLDSHIP_SECRET_KEY_FILE", ""), "key file that encrypts stored secrets (default: coldship.key next to --data; or set COLDSHIP_SECRET_KEY)")
	cmd.Flags().StringVar(&publicURL, "public-url", envOr("COLDSHIP_PUBLIC_URL", ""), "URL people and Git hosts use to reach this server, for webhook URLs and links in notifications (e.g. https://coldship.example.com)")
	cmd.Flags().StringVar(&snapshotDir, "snapshot-dir", envOr("COLDSHIP_SNAPSHOT_DIR", ""), "where database snapshots are kept (default: coldship-snapshots next to --data)")
	cmd.Flags().IntVar(&snapshotKeep, "snapshot-keep", 20, "snapshots to keep per database, manual and automatic counted separately (older ones are deleted)")
	cmd.Flags().DurationVar(&snapshotInterval, "db-snapshot-interval", 24*time.Hour, "how often to snapshot every database (0 disables scheduled snapshots)")
	cmd.Flags().BoolVar(&proxyOn, "proxy", true, "run the Caddy proxy that serves app domains with automatic HTTPS")
	cmd.Flags().IntVar(&httpPort, "proxy-http-port", 80, "host port for the proxy's HTTP listener")
	cmd.Flags().IntVar(&httpsPort, "proxy-https-port", 443, "host port for the proxy's HTTPS listener")
	cmd.Flags().StringVar(&acmeEmail, "acme-email", envOr("COLDSHIP_ACME_EMAIL", ""), "email for Let's Encrypt certificate notices (optional)")
	cmd.Flags().DurationVar(&buildTimeout, "build-timeout", 20*time.Minute, "maximum time for one Git build")
	cmd.Flags().StringVar(&gitCreds, "git-credentials", envOr("COLDSHIP_GIT_CREDENTIALS", ""), "git-credential-store file for private https repositories")
	cmd.Flags().StringVar(&buildDir, "build-dir", envOr("COLDSHIP_BUILD_DIR", ""), "directory for temporary build checkouts (default: system temp dir)")
	return cmd
}

func mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP server on stdio for AI agents (uses COLDSHIP_URL / COLDSHIP_TOKEN)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := mcpserver.New(newClient(), version)
			return s.Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// alreadyRunning reports whether a Coldship server answers at addr.
func alreadyRunning(addr string) bool {
	c := &http.Client{Timeout: time.Second}
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// startAgents listens on <data dir>/agent-run/api.sock and starts the agent
// manager.
func startAgents(ctx context.Context, dataDir string, st *store.Store, rt *runtime.Docker, log *slog.Logger) (*agents.Manager, net.Listener, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return nil, nil, err
	}
	dir := filepath.Join(dataDir, "agent-run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	sock := filepath.Join(dir, "api.sock")
	os.Remove(sock) // left by the last run
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return nil, nil, err
	}
	m := agents.New(st, rt, log)
	m.SocketDir, m.Binary, m.Version = dir, exe, version
	m.Start(ctx)
	return m, ln, nil
}
