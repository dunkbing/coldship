package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"hangar/internal/api"
	"hangar/internal/builder"
	"hangar/internal/engine"
	"hangar/internal/mcpserver"
	"hangar/internal/proxy"
	"hangar/internal/runtime"
	"hangar/internal/update"
	"hangar/internal/web"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func serverCmd() *cobra.Command {
	var addr, data, gitCreds, buildDir, acmeEmail string
	var grace, buildTimeout time.Duration
	var proxyOn, autoUpgrade bool
	var updURL, updKey string
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
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the Hangar control plane (API server + Docker runtime)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			log := slog.New(slog.NewTextHandler(os.Stderr, nil))
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
			rotator := &keyRotator{st: st, kr: kr, keyFile: keyFile, fromEnv: os.Getenv("HANGAR_SECRET_KEY") != ""}
			rt := runtime.NewDocker()
			rt.StartupGrace = grace
			bd := builder.NewGit()
			bd.CredentialsFile, bd.WorkDir, bd.Timeout = gitCreds, buildDir, buildTimeout
			var px proxy.Proxy
			if proxyOn {
				dataAbs, err := filepath.Abs(data)
				if err != nil {
					return err
				}
				px = proxy.NewCaddy(filepath.Join(filepath.Dir(dataAbs), "hangar-proxy"), httpPort, httpsPort, acmeEmail)
			}
			eng := engine.New(st, rt, bd, px)
			if snapshotDir == "" {
				dataAbs, err := filepath.Abs(data)
				if err != nil {
					return err
				}
				snapshotDir = filepath.Join(filepath.Dir(dataAbs), "hangar-snapshots")
			}
			eng.SnapshotDir, eng.SnapshotKeep = snapshotDir, snapshotKeep
			eng.Previews = engine.PreviewConfig{Domain: previewDomain, DefaultTTL: previewTTL, MaxTTL: previewMaxTTL, Max: maxPreviews}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if tok, err := eng.EnsureAdminToken(ctx); err != nil {
				return err
			} else if tok != "" {
				fmt.Fprintf(os.Stderr, "\nCreated admin token (shown once, store it safely):\n\n  %s\n\n"+
					"Give agents their own deploy-scoped token instead:\n  HANGAR_TOKEN=%s hangar token create --name my-agent --scope deploy\n\n", tok, tok)
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
			srv := &http.Server{Addr: addr, Handler: apiSrv, ReadHeaderTimeout: 10 * time.Second}

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
			if updKey != "" {
				pub, err := update.ParsePublicKey(updKey)
				if err != nil {
					return err
				}
				checker.PublicKey = pub
			}
			apiSrv.SetUpdater(checker)
			apiSrv.SetPublicURL(publicURL)
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
			log.Info("hangar listening", "addr", addr, "data", data, "version", version)
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
	cmd.Flags().StringVar(&addr, "addr", envOr("HANGAR_ADDR", "127.0.0.1:7070"), "listen address")
	cmd.Flags().StringVar(&data, "data", envOr("HANGAR_DATA", "hangar.db"), "SQLite state file")
	cmd.Flags().DurationVar(&grace, "startup-grace", 3*time.Second, "how long a new container must stay up to count as deployed")
	cmd.Flags().StringVar(&updURL, "update-url", envOr("HANGAR_UPDATE_URL", defaultUpdateURL), "signed release manifest to check for new versions")
	cmd.Flags().StringVar(&updKey, "update-public-key", envOr("HANGAR_UPDATE_PUBLIC_KEY", updatePublicKey), "Ed25519 public key (base64) that release manifests must be signed with")
	cmd.Flags().BoolVar(&autoUpgrade, "auto-upgrade", true, "install new minor and patch versions automatically when no deployment is running")
	cmd.Flags().DurationVar(&updInterval, "update-interval", 6*time.Hour, "how often to check for new versions")
	cmd.Flags().StringVar(&previewDomain, "preview-domain", envOr("HANGAR_PREVIEW_DOMAIN", ""), "give previews URLs under this domain (needs a wildcard DNS record *.<domain> pointing here)")
	cmd.Flags().DurationVar(&previewTTL, "preview-ttl", 24*time.Hour, "default lifetime of a preview")
	cmd.Flags().DurationVar(&previewMaxTTL, "preview-max-ttl", 7*24*time.Hour, "longest lifetime a preview may ask for")
	cmd.Flags().IntVar(&maxPreviews, "max-previews", 20, "most previews that may exist at once")
	cmd.Flags().StringVar(&keyFile, "secret-key-file", envOr("HANGAR_SECRET_KEY_FILE", ""), "key file that encrypts stored secrets (default: hangar.key next to --data; or set HANGAR_SECRET_KEY)")
	cmd.Flags().StringVar(&publicURL, "public-url", envOr("HANGAR_PUBLIC_URL", ""), "URL Git hosts use to reach this server, for webhook URLs (e.g. https://hangar.example.com)")
	cmd.Flags().StringVar(&snapshotDir, "snapshot-dir", envOr("HANGAR_SNAPSHOT_DIR", ""), "where database snapshots are kept (default: hangar-snapshots next to --data)")
	cmd.Flags().IntVar(&snapshotKeep, "snapshot-keep", 20, "snapshots to keep per database (older ones are deleted)")
	cmd.Flags().DurationVar(&snapshotInterval, "db-snapshot-interval", 24*time.Hour, "how often to snapshot every database (0 disables scheduled snapshots)")
	cmd.Flags().BoolVar(&proxyOn, "proxy", true, "run the Caddy proxy that serves app domains with automatic HTTPS")
	cmd.Flags().IntVar(&httpPort, "proxy-http-port", 80, "host port for the proxy's HTTP listener")
	cmd.Flags().IntVar(&httpsPort, "proxy-https-port", 443, "host port for the proxy's HTTPS listener")
	cmd.Flags().StringVar(&acmeEmail, "acme-email", envOr("HANGAR_ACME_EMAIL", ""), "email for Let's Encrypt certificate notices (optional)")
	cmd.Flags().DurationVar(&buildTimeout, "build-timeout", 20*time.Minute, "maximum time for one Git build")
	cmd.Flags().StringVar(&gitCreds, "git-credentials", envOr("HANGAR_GIT_CREDENTIALS", ""), "git-credential-store file for private https repositories")
	cmd.Flags().StringVar(&buildDir, "build-dir", envOr("HANGAR_BUILD_DIR", ""), "directory for temporary build checkouts (default: system temp dir)")
	return cmd
}

func mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP server on stdio for AI agents (uses HANGAR_URL / HANGAR_TOKEN)",
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
