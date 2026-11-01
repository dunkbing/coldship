package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"hangar/internal/api"
	"hangar/internal/builder"
	"hangar/internal/engine"
	"hangar/internal/mcpserver"
	"hangar/internal/runtime"
	"hangar/internal/store"
	"hangar/internal/web"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func serverCmd() *cobra.Command {
	var addr, data, gitCreds, buildDir string
	var grace, buildTimeout time.Duration
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
			eng := engine.New(st, rt, bd)

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if tok, err := eng.EnsureAdminToken(ctx); err != nil {
				return err
			} else if tok != "" {
				fmt.Fprintf(os.Stderr, "\nCreated admin token (shown once, store it safely):\n\n  %s\n\n"+
					"Give agents their own deploy-scoped token instead:\n  HANGAR_TOKEN=%s hangar token create --name my-agent --scope deploy\n\n", tok, tok)
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
