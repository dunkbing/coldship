package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func webhookCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "webhook", Short: "Git webhooks: deploy or preview on push"}

	var rotate bool
	enable := &cobra.Command{
		Use:   "enable <app>",
		Short: "Enable an app's Git webhook and show its URL and secret (admin)",
		Long: "Create the app's webhook secret and print what to configure on the Git host. What a push\n" +
			"does is set by on_push in the app's spec. The secret is shown only once.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := newClient().EnableWebhook(cmd.Context(), args[0], rotate)
			if err != nil {
				return err
			}
			return emit(info, func() {
				fmt.Printf("Webhook for %s enabled. The secret is shown once:\n\n", info.App)
				fmt.Printf("  URL:     %s\n  Secret:  %s\n\n", info.URL, info.Secret)
				fmt.Println("Set it up on your Git host:")
				fmt.Println("  GitHub:        Settings → Webhooks → Add webhook. Content type application/json, the secret")
				fmt.Println("                 above, events: \"Just the push event\".")
				fmt.Println("  GitLab:        Settings → Webhooks. Secret token: the secret above. Trigger: Push events.")
				fmt.Println("  Gitea/Forgejo: Settings → Webhooks → Add webhook → Gitea. Content type application/json,")
				fmt.Println("                 the secret above, trigger on push events.")
				if strings.HasPrefix(info.URL, "http://127.") || strings.HasPrefix(info.URL, "http://localhost") {
					fmt.Println("\nThe URL points at localhost: start the server with --public-url so Git hosts can reach it.")
				}
				if info.OnPush == nil {
					fmt.Println("\nThe app's spec has no on_push section yet, so pushes are recorded but do nothing.")
					fmt.Println("Add for example:  on_push: {action: plan, previews: true}")
				} else {
					fmt.Printf("\nPushes to %s will %s.", orDefault(info.Ref, "the default branch"), map[string]string{"plan": "create a plan for review", "deploy": "deploy automatically"}[info.OnPush.Action])
					if info.OnPush.Previews {
						fmt.Print(" Pushes to other branches create previews.")
					}
					fmt.Println()
				}
			})
		},
	}
	enable.Flags().BoolVar(&rotate, "rotate", false, "replace the secret of an enabled webhook (the old one stops working)")

	status := &cobra.Command{
		Use:   "status <app>",
		Short: "Show an app's webhook and its recent deliveries",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := newClient().WebhookStatus(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(info, func() {
				if !info.Enabled {
					fmt.Printf("The webhook for %s is not enabled (coldship webhook enable %s).\n", info.App, info.App)
					return
				}
				fmt.Printf("Webhook for %s: %s\n", info.App, info.URL)
				if info.OnPush != nil {
					fmt.Printf("On push to %s: %s; previews: %t\n", orDefault(info.Ref, "default branch"), info.OnPush.Action, info.OnPush.Previews)
				} else {
					fmt.Println("No on_push in the spec: pushes do nothing.")
				}
				if len(info.Deliveries) == 0 {
					fmt.Println("\nNo deliveries yet.")
					return
				}
				fmt.Println()
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "WHEN\tREF\tCOMMIT\tBY\tRESULT\tDETAIL")
				for _, d := range info.Deliveries {
					commit := d.Commit
					if len(commit) > 7 {
						commit = commit[:7]
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", d.CreatedAt.Local().Format("01-02 15:04:05"), orDefault(d.Ref, "-"), orDefault(commit, "-"),
						orDefault(d.Pusher, "-"), d.Result, d.Detail)
				}
				w.Flush()
			})
		},
	}

	disable := &cobra.Command{
		Use:   "disable <app>",
		Short: "Disable an app's webhook (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := newClient().DisableWebhook(cmd.Context(), args[0]); err != nil {
				return err
			}
			return emit(map[string]bool{"disabled": true}, func() { fmt.Printf("Webhook for %s disabled.\n", args[0]) })
		},
	}

	cmd.AddCommand(enable, status, disable)
	return cmd
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
