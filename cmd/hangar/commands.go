package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"hangar/internal/apierr"
	"hangar/internal/engine"
	"hangar/internal/planner"
	"hangar/internal/spec"
	"hangar/internal/store"
)

func planRequestFromFlags(file, del string, redeploy bool) (engine.PlanRequest, error) {
	switch {
	case file != "" && del != "":
		return engine.PlanRequest{}, fmt.Errorf("use either -f or --delete, not both")
	case del != "":
		return engine.PlanRequest{Delete: del}, nil
	default:
		if file == "" {
			file = "hangar.yaml"
		}
		a, err := spec.Load(file)
		if err != nil {
			return engine.PlanRequest{}, err
		}
		return engine.PlanRequest{Spec: a, Redeploy: redeploy}, nil
	}
}

func planCmd() *cobra.Command {
	var file, del string
	var redeploy bool
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Dry run: show what applying a spec (or deleting an app) would change",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req, err := planRequestFromFlags(file, del, redeploy)
			if err != nil {
				return err
			}
			p, err := newClient().Plan(cmd.Context(), req)
			if err != nil {
				return err
			}
			return emit(p, func() { printPlan(p) })
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "spec file (default hangar.yaml)")
	cmd.Flags().StringVar(&del, "delete", "", "plan deleting this app instead")
	cmd.Flags().BoolVar(&redeploy, "redeploy", false, "recreate the container even if nothing changed")
	return cmd
}

func applyCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "apply <plan-id>",
		Short: "Apply a reviewed plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := newClient().Apply(cmd.Context(), args[0], reason)
			if err != nil {
				return err
			}
			return emit(res, func() { printApply(res) })
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why (recorded in the audit log)")
	cmd.MarkFlagRequired("reason")
	return cmd
}

func deployCmd() *cobra.Command {
	var file, reason string
	var redeploy bool
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Plan and apply a spec in one step (stops if human approval is needed)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req, err := planRequestFromFlags(file, "", redeploy)
			if err != nil {
				return err
			}
			c := newClient()
			p, err := c.Plan(cmd.Context(), req)
			if err != nil {
				return err
			}
			if p.RequiresApproval {
				if !flagJSON {
					printPlan(p)
				}
				return apierr.New(403, "approval_required", "plan %s needs human approval before it can be applied", p.ID).
					WithHint("review it, run `hangar approve " + p.ID + "`, then `hangar apply " + p.ID + " --reason ...`")
			}
			if p.Action == "noop" {
				return emit(p, func() { fmt.Println("Nothing to do: app already matches the spec.") })
			}
			if !flagJSON {
				printPlan(p)
				fmt.Println()
			}
			res, err := c.Apply(cmd.Context(), p.ID, reason)
			if err != nil {
				return err
			}
			return emit(res, func() { printApply(res) })
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "spec file (default hangar.yaml)")
	cmd.Flags().StringVar(&reason, "reason", "", "why (recorded in the audit log)")
	cmd.Flags().BoolVar(&redeploy, "redeploy", false, "recreate the container even if nothing changed")
	cmd.MarkFlagRequired("reason")
	return cmd
}

func approveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "approve <plan-id>",
		Short: "Approve a destructive production plan (admin token; meant for humans)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := newClient().Approve(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(p, func() { printPlan(p) })
		},
	}
}

func rollbackCmd() *cobra.Command {
	var reason, to string
	cmd := &cobra.Command{
		Use:   "rollback <app>",
		Short: "Redeploy the last good (or a specific) deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := newClient().Rollback(cmd.Context(), args[0], to, reason)
			if err != nil {
				return err
			}
			return emit(res, func() { printApply(res) })
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why (recorded in the audit log)")
	cmd.Flags().StringVar(&to, "to", "", "deployment id to restore (default: last good)")
	cmd.MarkFlagRequired("reason")
	return cmd
}

func appsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "apps",
		Short: "List apps",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			apps, err := newClient().ListApps(cmd.Context())
			if err != nil {
				return err
			}
			return emit(apps, func() {
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tSTAGE\tIMAGE\tDEPLOYMENT")
				for _, a := range apps {
					stage, image := "-", "-"
					if a.Spec != nil {
						stage, image = string(a.Spec.Stage), a.Spec.Image
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.Name, stage, image, orDash(a.CurrentDeployment))
				}
				w.Flush()
			})
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <app>",
		Short: "Show an app's spec, container status and latest deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := newClient().GetApp(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(v, nil)
		},
	}
}

func logsCmd() *cobra.Command {
	var tail int
	var since string
	cmd := &cobra.Command{
		Use:   "logs <app>",
		Short: "Show recent container logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := newClient().Logs(cmd.Context(), args[0], tail, since)
			if err != nil {
				return err
			}
			return emit(res, func() { fmt.Println(strings.Join(res.Lines, "\n")) })
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 100, "lines from the end")
	cmd.Flags().StringVar(&since, "since", "", "only lines newer than this (e.g. 10m)")
	return cmd
}

func diagnoseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diagnose <app>",
		Short: "Explain what is wrong with an app and suggest fixes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := newClient().Diagnose(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(d, func() {
				fmt.Printf("App %s: healthy=%t state=%s restarts=%d\n", d.App, d.Healthy, orDash(d.Runtime.State), d.Runtime.RestartCount)
				for _, f := range d.Findings {
					fmt.Printf("  [%s] %s: %s\n", f.Severity, f.Code, f.Message)
					if f.SuggestedFix != nil {
						b, _ := json.Marshal(f.SuggestedFix)
						fmt.Printf("      suggested fix: %s\n", b)
					}
				}
				if len(d.RecentLogs) > 0 {
					n := min(len(d.RecentLogs), 15)
					fmt.Printf("\nLast %d log lines:\n", n)
					for _, l := range d.RecentLogs[len(d.RecentLogs)-n:] {
						fmt.Println("  " + l)
					}
				}
			})
		},
	}
}

func deploymentsCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "deployments <app>",
		Short: "List an app's deployments, newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := newClient().Deployments(cmd.Context(), args[0], limit)
			if err != nil {
				return err
			}
			return emit(deps, func() {
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "ID\tSTATUS\tIMAGE\tACTOR\tCREATED\tREASON")
				for _, d := range deps {
					image := "-"
					if d.Spec != nil {
						image = d.Spec.Image
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.Status, image, d.Actor, d.CreatedAt.Format("2006-01-02 15:04:05"), d.Reason)
				}
				w.Flush()
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "max deployments")
	return cmd
}

func auditCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Show the audit log: who did what, and why",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			entries, err := newClient().Audit(cmd.Context(), limit)
			if err != nil {
				return err
			}
			return emit(entries, func() {
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "TIME\tACTOR\tACTION\tTARGET\tREASON")
				for _, e := range entries {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Time.Format("2006-01-02 15:04:05"), e.Actor, e.Action, e.Target, e.Reason)
				}
				w.Flush()
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "max entries")
	return cmd
}

func tokenCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "token", Short: "Manage API tokens"}
	var name, scope string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a token (admin). Scopes: read, deploy, admin",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := newClient().CreateToken(cmd.Context(), name, scope)
			if err != nil {
				return err
			}
			return emit(t, func() {
				fmt.Printf("Token %q (scope %s), shown once:\n\n  %s\n", t.Name, t.Scope, t.Token)
			})
		},
	}
	create.Flags().StringVar(&name, "name", "", "token name, e.g. claude-agent")
	create.Flags().StringVar(&scope, "scope", string(store.ScopeDeploy), "read | deploy | admin")
	create.MarkFlagRequired("name")
	cmd.AddCommand(create)
	return cmd
}

// --- human output ---

func printPlan(p *store.Plan) {
	fmt.Printf("Plan %s for app %q (%s)\n", p.ID, p.App, p.Action)
	for _, c := range p.Changes {
		sym := map[string]string{planner.OpCreate: "+", planner.OpUpdate: "~", planner.OpDelete: "-", planner.OpRedeploy: "↻"}[c.Op]
		line := fmt.Sprintf("  %s %s", sym, c.Path)
		switch {
		case c.From != nil && c.To != nil:
			line += fmt.Sprintf(": %v -> %v", c.From, c.To)
		case c.To != nil:
			line += fmt.Sprintf(": %v", c.To)
		case c.From != nil:
			line += fmt.Sprintf(" (was %v)", c.From)
		}
		if c.Destructive {
			line += "  [DESTRUCTIVE: " + c.Reason + "]"
		} else if c.Reason != "" {
			line += "  (" + c.Reason + ")"
		}
		fmt.Println(line)
	}
	fmt.Printf("Destructive: %s  Downtime: %s  Requires approval: %s", yn(p.Destructive), yn(p.Downtime), yn(p.RequiresApproval))
	if p.ApprovedBy != "" {
		fmt.Printf(" (approved by %s)", p.ApprovedBy)
	}
	fmt.Printf("\nStatus: %s  Expires: %s\nNext: %s\n", p.Status, p.ExpiresAt.Local().Format("15:04:05"), p.NextStep)
}

func printApply(r *engine.ApplyResult) {
	fmt.Println(r.Message)
	if d := r.Deployment; d != nil {
		fmt.Printf("Deployment %s: %s\n", d.ID, d.Status)
	}
}

func yn(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
