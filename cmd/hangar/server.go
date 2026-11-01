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
	"hangar/internal/store"
	"hangar/internal/web"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func serverCmd() *cobra.Command {
	var addr, data, gitCreds, buildDir, acmeEmail string
	var grace, buildTimeout time.Duration
	var proxyOn bool
	var httpPort, httpsPort int
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the Hangar control plane (API server + Docker runtime)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			log := slog.New(slog.NewTextHandler(os.Stderr, nil))
			st, err := store.Open(data)
			if err != nil {
				return err
			}
			defer st.Close()
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

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if tok, err := eng.EnsureAdminToken(ctx); err != nil {
				return err
			} else if tok != "" {
				fmt.Fprintf(os.Stderr, "\nCreated admin token (shown once, store it safely):\n\n  %s\n\n"+
					"Give agents their own deploy-scoped token instead:\n  HANGAR_TOKEN=%s hangar token create --name my-agent --scope deploy\n\n", tok, tok)
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

			srv := &http.Server{Addr: addr, Handler: api.New(eng, st, log, web.Handler()), ReadHeaderTimeout: 10 * time.Second}
			go func() {
				<-ctx.Done()
				shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				srv.Shutdown(shCtx)
			}()
			log.Info("hangar listening", "addr", addr, "data", data)
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", envOr("HANGAR_ADDR", "127.0.0.1:7070"), "listen address")
	cmd.Flags().StringVar(&data, "data", envOr("HANGAR_DATA", "hangar.db"), "SQLite state file")
	cmd.Flags().DurationVar(&grace, "startup-grace", 3*time.Second, "how long a new container must stay up to count as deployed")
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
