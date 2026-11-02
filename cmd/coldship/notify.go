package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/engine"
)

func notifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Notifications to Slack, Discord, Telegram or a webhook when deploys fail, apps go down, and more",
		Long: `Channels get a message when something needs attention: a deploy or build
failed, an app went down, a change waits for approval, backups fail, the
disk is almost full. Webhook URLs and bot tokens are read from the
environment or asked for, never taken as flags.

  COLDSHIP_NOTIFY_URL=https://hooks.slack.com/services/... coldship notify add slack
  COLDSHIP_TELEGRAM_TOKEN=123456:ABC... coldship notify add telegram --chat-id -1001234567890
  coldship notify test <id>`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			chans, err := newClient().Channels(cmd.Context())
			if err != nil {
				return err
			}
			return emit(chans, func() {
				if len(chans) == 0 {
					fmt.Println("No channels yet. Add one: coldship notify add slack")
					return
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "ID\tNAME\tKIND\tTO\tEVENTS\tLAST")
				for _, c := range chans {
					last := "-"
					if c.LastAt != nil {
						last = "ok " + c.LastAt.Local().Format("01-02 15:04")
						if c.LastError != "" {
							last = "failed: " + c.LastError
						}
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", c.ID, c.Name, c.Kind, c.Target, strings.Join(c.Events, ","), last)
				}
				w.Flush()
			})
		},
	}

	var req engine.ChannelRequest
	var events string
	add := &cobra.Command{
		Use:   "add <slack|discord|telegram|webhook>",
		Short: "Add a channel (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Kind = args[0]
			if events != "" {
				req.Events = strings.Split(events, ",")
			}
			var err error
			if req.Kind == "telegram" {
				req.BotToken, err = secretInput("COLDSHIP_TELEGRAM_TOKEN", "Bot token from @BotFather: ")
			} else {
				req.URL, err = secretInput("COLDSHIP_NOTIFY_URL", "Webhook URL: ")
			}
			if err != nil {
				return err
			}
			c, err := newClient().CreateChannel(cmd.Context(), req)
			if err != nil {
				return err
			}
			return emit(c, func() {
				fmt.Printf("Added %s (%s) for: %s\n", c.ID, c.Target, strings.Join(c.Events, ", "))
				if c.Secret != "" {
					fmt.Printf("Signing secret (shown once): %s\n", c.Secret)
					fmt.Println("Check X-Coldship-Signature: sha256=HMAC-SHA256(secret, X-Coldship-Timestamp + \".\" + body)")
				}
				fmt.Printf("Send a test message: coldship notify test %s\n", c.ID)
			})
		},
	}
	add.Flags().StringVar(&req.Name, "name", "", "a name for the channel")
	add.Flags().StringVar(&req.ChatID, "chat-id", "", "Telegram chat id (groups start with -) or @channel")
	add.Flags().StringVar(&events, "events", "", "comma-separated events (default: every failure; see coldship notify events)")

	test := &cobra.Command{
		Use:   "test <id>",
		Short: "Send a test message (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient().TestChannel(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(c, func() { fmt.Printf("Delivered to %s.\n", c.Target) })
		},
	}

	rm := &cobra.Command{
		Use:   "rm <id>",
		Short: "Remove a channel (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := newClient().DeleteChannel(cmd.Context(), args[0]); err != nil {
				return err
			}
			return emit(map[string]any{"deleted": true}, func() { fmt.Println("Removed.") })
		},
	}

	evs := &cobra.Command{
		Use:   "events",
		Short: "The events channels can get",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			kinds, err := newClient().ChannelEvents(cmd.Context())
			if err != nil {
				return err
			}
			return emit(kinds, func() {
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				for _, k := range kinds {
					def := ""
					if k.Default {
						def = "(default)"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\n", k.Kind, k.Label, def)
				}
				w.Flush()
			})
		},
	}
	cmd.AddCommand(add, test, rm, evs)
	return cmd
}

// secretInput reads a secret from env, or asks for it without echo.
func secretInput(env, prompt string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return v, nil
	}
	if !isTerminal(os.Stdin) {
		return "", apierr.New(400, "invalid_request", "set %s", env)
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return strings.TrimSpace(string(b)), err
}
