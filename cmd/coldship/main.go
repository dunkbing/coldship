// Command coldship is a single binary: the control-plane server, the CLI, and
// the MCP server for AI agents.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/client"
)

// Set at release time with -ldflags "-X main.version=... -X main.defaultUpdateURL=...".
// Builds from source have no update URL, so they never replace themselves.
var (
	version          = "0.1.0-dev"
	defaultUpdateURL = ""
)

var (
	flagURL   string
	flagToken string
	flagJSON  bool
)

// Exit codes are part of the CLI contract so agents can branch on them.
const (
	exitError            = 1
	exitApprovalRequired = 3
	exitStale            = 4
)

func main() {
	root := &cobra.Command{
		Use:           "coldship",
		Short:         "Agent-native, self-hosted app platform",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&flagURL, "url", "", "Coldship server URL (env COLDSHIP_URL, then `coldship login`, default http://127.0.0.1:7070)")
	root.PersistentFlags().StringVar(&flagToken, "token", "", "API token (env COLDSHIP_TOKEN, then `coldship login`)")
	root.PersistentFlags().BoolVar(&flagJSON, "json", false, "machine-readable JSON output")

	root.AddCommand(serverCmd(), mcpCmd())
	root.AddCommand(loginCmd(), logoutCmd(), deployCmd(), deleteCmd(), exportCmd(), planCmd(), applyCmd(), approveCmd(), rollbackCmd())
	root.AddCommand(appsCmd(), statusCmd(), logsCmd(), diagnoseCmd(), deploymentsCmd(), deployLogCmd(), auditCmd(), tokenCmd(), inviteCmd())
	root.AddCommand(dataCmd(), backupsCmd(), secretsCmd(), buildCmd(), webhookCmd(), dbCmd(), previewCmd(), versionCmd(), upgradeCmd(), selfUpdateCmd(), uninstallCmd(), releaseCmd())
	root.PersistentPostRun = func(cmd *cobra.Command, _ []string) { maybeNotice(cmd) }

	if err := root.Execute(); err != nil {
		os.Exit(reportError(err))
	}
}

func newClient() *client.Client {
	return client.New(serverURL(), token())
}

// emit prints v as JSON (--json, or when no human format exists) or via human.
func emit(v any, human func()) error {
	if flagJSON || human == nil {
		enc := json.NewEncoder(os.Stdout)
		if !flagJSON {
			enc.SetIndent("", "  ")
		}
		return enc.Encode(v)
	}
	human()
	return nil
}

func reportError(err error) int {
	ae, ok := apierr.As(err)
	if !ok {
		if flagJSON {
			json.NewEncoder(os.Stderr).Encode(map[string]any{"error": map[string]string{"code": "cli_error", "message": err.Error()}})
		} else {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		return exitError
	}
	if flagJSON {
		json.NewEncoder(os.Stderr).Encode(map[string]any{"error": ae})
	} else {
		fmt.Fprintf(os.Stderr, "error [%s]: %s\n", ae.Code, ae.Message)
		if ae.Field != "" {
			fmt.Fprintf(os.Stderr, "  field: %s\n", ae.Field)
		}
		if ae.Hint != "" {
			fmt.Fprintf(os.Stderr, "  hint: %s\n", ae.Hint)
		}
		if ae.SuggestedFix != nil {
			b, _ := json.Marshal(ae.SuggestedFix)
			fmt.Fprintf(os.Stderr, "  suggested fix: %s\n", b)
		}
		for k, v := range ae.Details {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", k, v)
		}
	}
	switch ae.Code {
	case "approval_required":
		return exitApprovalRequired
	case "plan_stale", "plan_expired", "plan_superseded":
		return exitStale
	}
	return exitError
}
