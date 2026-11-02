package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/engine"
	"github.com/dunkbing/coldship/internal/planner"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

func planRequestFromFlags(file, del string, redeploy bool) (engine.PlanRequest, error) {
	switch {
	case file != "" && del != "":
		return engine.PlanRequest{}, fmt.Errorf("use either -f or --delete, not both")
	case del != "":
		return engine.PlanRequest{Delete: del}, nil
	default:
		if file == "" {
			file = "coldship.yaml"
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
	cmd.Flags().StringVarP(&file, "file", "f", "", "spec file (default coldship.yaml)")
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
			res, err := applyFollowing(cmd, newClient(), args[0], reason)
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
				fmt.Fprintln(w, "NAME\tSTAGE\tSOURCE\tDEPLOYMENT")
				for _, a := range apps {
					stage, image := "-", "-"
					if a.Spec != nil {
						stage, image = string(a.Spec.Stage), a.Spec.Source()
					}
					dep := a.CurrentDeployment
					if dep == "" {
						dep = "not deployed yet (coldship status " + a.Name + ")"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.Name, stage, image, dep)
				}
				w.Flush()
			})
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [app]",
		Short: "Is Coldship running, and how are the apps? With an app name: that app's details",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return serverStatus(cmd)
			}
			v, err := newClient().GetApp(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(v, func() { printStatus(v) })
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
				if len(d.DeployLog) > 0 {
					n := min(len(d.DeployLog), 20)
					fmt.Printf("\nLast %d deploy log lines:\n", n)
					for _, l := range d.DeployLog[len(d.DeployLog)-n:] {
						fmt.Println("  " + l)
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
				fmt.Fprintln(w, "ID\tSTATUS\tIMAGE\tCOMMIT\tACTOR\tCREATED\tREASON")
				for _, d := range deps {
					image := d.Image
					if image == "" && d.Spec != nil {
						image = d.Spec.Source()
					}
					commit := "-"
					if len(d.Commit) >= 12 {
						commit = d.Commit[:12]
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.Status, orDash(image), commit, d.Actor, d.CreatedAt.Format("2006-01-02 15:04:05"), d.Reason)
				}
				w.Flush()
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "max deployments")
	return cmd
}

func deployLogCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "deploy-log <deployment-id>",
		Aliases: []string{"build-log"},
		Short:   "Show what happened during a deployment: build, start, health checks",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := newClient().DeployLog(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(l, func() {
				if len(l.Lines) == 0 {
					fmt.Println(l.Note)
					return
				}
				fmt.Println(strings.Join(l.Lines, "\n"))
			})
		},
	}
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
	var apps []string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a token (admin). Scopes: read, deploy, admin",
		Long: `Create an API token (admin). Scopes: read, deploy, admin.

Limit a token to some apps with --app (repeatable). Patterns work, which
also lets the token create new apps, but only with matching names:

  coldship token create --name claude --app web --app api
  coldship token create --name sandbox --app 'agent-*'

A limited token sees and changes only those apps and their previews.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := newClient().CreateToken(cmd.Context(), name, scope, apps...)
			if err != nil {
				return err
			}
			return emit(t, func() {
				limit := "every app"
				if len(t.Apps) > 0 {
					limit = "apps " + strings.Join(t.Apps, ", ")
				}
				fmt.Printf("Token %q (scope %s, %s), shown once:\n\n  %s\n", t.Name, t.Scope, limit, t.Token)
			})
		},
	}
	create.Flags().StringVar(&name, "name", "", "token name, e.g. claude-agent")
	create.Flags().StringVar(&scope, "scope", string(store.ScopeDeploy), "read | deploy | admin")
	create.Flags().StringArrayVar(&apps, "app", nil, "limit the token to this app or pattern, like shop-* (repeatable; default: every app)")
	create.MarkFlagRequired("name")

	list := &cobra.Command{
		Use:   "list",
		Short: "List tokens, never their values (admin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			toks, err := newClient().ListTokens(cmd.Context())
			if err != nil {
				return err
			}
			return emit(toks, func() {
				w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tSCOPE\tAPPS\tCREATED")
				for _, t := range toks {
					limit := "all"
					if len(t.Apps) > 0 {
						limit = strings.Join(t.Apps, ",")
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", t.Name, t.Scope, limit, t.CreatedAt.Local().Format("2006-01-02 15:04"))
				}
				w.Flush()
			})
		},
	}

	var reason string
	revoke := &cobra.Command{
		Use:   "revoke <name>",
		Short: "Revoke a token and sign out its dashboard sessions (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := newClient().RevokeToken(cmd.Context(), args[0], reason); err != nil {
				return err
			}
			return emit(map[string]any{"name": args[0], "revoked": true}, func() {
				fmt.Printf("Token %q revoked. Its dashboard sessions are signed out.\n", args[0])
			})
		},
	}
	revoke.Flags().StringVar(&reason, "reason", "", "why, for the audit log")
	cmd.AddCommand(create, list, revoke)
	return cmd
}

// --- human output ---

func printPlan(p *store.Plan) {
	printChanges(p, true)
	fmt.Printf("Status: %s  Expires: %s\nNext: %s\n", p.Status, p.ExpiresAt.Local().Format("15:04:05"), p.NextStep)
}

// printChanges prints what a plan changes. header adds the plan id line,
// for commands where the user handles plans themselves.
func printChanges(p *store.Plan, header bool) {
	if header {
		fmt.Printf("Plan %s for app %q (%s)\n", p.ID, p.App, p.Action)
	}
	for _, c := range p.Changes {
		sym := map[string]string{planner.OpCreate: "+", planner.OpUpdate: "~", planner.OpDelete: "-", planner.OpRedeploy: "↻", "restore": "⟲"}[c.Op]
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
	fmt.Println()
}

func printApply(r *engine.ApplyResult) {
	fmt.Println(r.Message)
	if d := r.Deployment; d != nil {
		fmt.Printf("Deployment %s: %s\n", d.ID, d.Status)
		if d.Static {
			fmt.Println("Served as static files by the proxy, with no container.")
		}
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

func printStatus(v *engine.AppView) {
	fmt.Printf("App %s", v.Name)
	if v.PreviewOf != "" {
		fmt.Printf(" (preview of %s)", v.PreviewOf)
	}
	fmt.Println()
	if s := v.Spec; s != nil {
		fmt.Printf("  Stage:    %s\n  Source:   %s\n", s.Stage, s.Source())
		static := v.Runtime != nil && v.Runtime.State == "static"
		if s.Port > 0 && !static {
			fmt.Printf("  Port:     %d", s.Port)
			if s.HostPort > 0 {
				fmt.Printf(" (published on the server's port %d)", s.HostPort)
			}
			fmt.Println()
		}
		if len(s.Env) > 0 {
			keys := make([]string, 0, len(s.Env))
			for k := range s.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			fmt.Printf("  Env:      %s\n", strings.Join(keys, ", "))
		}
		for _, db := range s.Databases {
			fmt.Printf("  Database: %s (%s %s, in %s)\n", db.Name, db.Engine, db.Version, db.Env)
		}
		for _, vol := range s.Volumes {
			fmt.Printf("  Volume:   %s at %s\n", vol.Name, vol.Path)
		}
	}
	for _, u := range v.URLs {
		fmt.Printf("  URL:      %s\n", u)
	}
	if rt := v.Runtime; rt != nil && rt.State == "static" {
		fmt.Println("  Served:   as static files by the proxy (no container)")
	} else if rt != nil {
		state := "not running"
		switch {
		case rt.Running:
			state = "running"
		case rt.Exists:
			state = rt.State
			if rt.Error != "" {
				state += ": " + rt.Error
			}
		}
		fmt.Printf("  Container: %s\n", state)
	}
	if d := v.LatestDeployment; d != nil {
		fmt.Printf("  Latest deployment: %s, %s by %s, %s", d.ID, d.Status, d.Actor, d.CreatedAt.Local().Format("2006-01-02 15:04"))
		if d.Error != "" {
			fmt.Printf("\n    %s", d.Error)
		}
		fmt.Println()
		if d.Status == store.DeployFailed {
			fmt.Printf("  Next: coldship deploy-log %s, or coldship diagnose %s\n", d.ID, v.Name)
		}
	} else {
		fmt.Println("  Not deployed yet.")
	}
}

// appHealth sums up an app the way the dashboard does.
func appHealth(v *engine.AppView) string {
	if d := v.LatestDeployment; d != nil && (d.Status == store.DeployBuilding || d.Status == store.DeployInProgress) {
		return "deploying"
	}
	rt := v.Runtime
	switch {
	case rt == nil:
		return "unknown"
	case !rt.Exists:
		return "stopped"
	}
	failed := v.LatestDeployment != nil && v.LatestDeployment.Status == store.DeployFailed && !v.LatestDeployment.KeptPrevious
	if rt.Running && rt.RestartCount == 0 && !failed {
		return "healthy"
	}
	return "failing"
}

func isLocalURL(u string) bool {
	return strings.Contains(u, "://127.0.0.1") || strings.Contains(u, "://localhost") || strings.Contains(u, "://[::1]")
}

// serverStatus answers "is Coldship running, and is everything all right?".
func serverStatus(cmd *cobra.Command) error {
	ctx := cmd.Context()
	c := newClient()
	u := serverURL()
	_, unitErr := os.Stat(unitPath)
	service := unitErr == nil && isLocalURL(u)

	sys, err := c.SystemView(ctx)
	if ae, ok := apierr.As(err); ok {
		switch ae.Code {
		case "server_unreachable":
			e := apierr.New(503, "server_unreachable", "Coldship isn't reachable at %s", u)
			if isLocalURL(u) {
				e.Message = "Coldship isn't running at " + u
			}
			switch {
			case service:
				e.WithHint("the coldship service is installed but not answering: see `sudo systemctl status coldship` and `sudo journalctl -u coldship -n 50`; start it with `sudo systemctl start coldship`")
			case isLocalURL(u):
				e.WithHint("install it as a service, which starts on boot (curl -fsSL https://raw.githubusercontent.com/dunkbing/coldship/main/install.sh | sh), or run `coldship server` in a terminal")
			default:
				e.WithHint("check that the server is up and reachable from here, and the URL you're logged in to (coldship login <url>)")
			}
			return e
		case "unauthenticated":
			return apierr.New(401, "unauthenticated", "Coldship is running at %s, but this CLI isn't signed in", u).
				WithHint("coldship login " + u + " (the admin token was printed when Coldship was installed)")
		}
	}
	if err != nil {
		return err
	}
	me, _ := c.WhoAmI(ctx)
	apps, err := c.ListApps(ctx)
	if err != nil {
		return err
	}
	var healthy, deploying int
	var failing []string
	for _, a := range apps {
		v, err := c.GetApp(ctx, a.Name)
		if err != nil {
			continue
		}
		switch appHealth(v) {
		case "healthy":
			healthy++
		case "deploying":
			deploying++
		default:
			failing = append(failing, a.Name)
		}
	}
	var waiting []string
	if plans, err := c.ListPlans(ctx, "pending", 200); err == nil {
		for _, p := range plans {
			if p.RequiresApproval && p.ApprovedBy == "" {
				waiting = append(waiting, p.ID)
			}
		}
	}

	out := map[string]any{"url": u, "running": true, "service": service, "version": sys.Version, "host": sys.Host,
		"apps":                 map[string]any{"total": len(apps), "healthy": healthy, "deploying": deploying, "failing": failing},
		"waiting_for_approval": waiting, "update_available": sys.UpdateAvailable, "latest": sys.Latest}
	if me != nil {
		out["signed_in_as"] = me
	}
	return emit(out, func() {
		where := ""
		if service {
			where = " (the coldship service)"
		}
		fmt.Printf("Coldship %s is running at %s%s\n", sys.Version, u, where)
		h := sys.Host
		switch {
		case h.EngineError != "":
			fmt.Printf("  Engine:    unreachable: %s\n", h.EngineError)
		case h.Engine != "":
			name := map[string]string{"docker": "Docker", "podman": "Podman"}[h.Engine]
			fmt.Printf("  Engine:    %s %s\n", name, h.EngineVersion)
		}
		// Servers before 0.1.6 don't report their engine or proxy.
		hostKnown := h.Engine != "" || h.EngineError != ""
		switch p := h.Proxy; {
		case !hostKnown:
		case p == nil:
			fmt.Println("  Proxy:     off (no domains; started with --proxy=false)")
		case p.Running:
			fmt.Printf("  Proxy:     running on ports %d and %d\n", p.HTTP, p.HTTPS)
		default:
			fmt.Printf("  Proxy:     not running: %s\n", orDash(p.Error))
		}
		switch {
		case len(apps) == 0:
			fmt.Println("  Apps:      none yet (coldship deploy --name hello --image nginx:1.27-alpine --port 80 --host-port 8090)")
		default:
			line := fmt.Sprintf("%d: %d healthy", len(apps), healthy)
			if deploying > 0 {
				line += fmt.Sprintf(", %d deploying", deploying)
			}
			if len(failing) > 0 {
				verb := "needs"
				if len(failing) > 1 {
					verb = "need"
				}
				line += fmt.Sprintf(", %d %s attention (%s; see coldship diagnose <app>)", len(failing), verb, strings.Join(failing, ", "))
			}
			fmt.Printf("  Apps:      %s\n", line)
		}
		if len(waiting) > 0 {
			fmt.Printf("  Approvals: %d plan(s) wait for a human: coldship approve %s\n", len(waiting), waiting[0])
		}
		if sys.UpdateAvailable {
			fmt.Printf("  Update:    %s is available (coldship upgrade)\n", sys.Latest)
		}
		if me != nil {
			who := fmt.Sprintf("%s (%s)", me.Name, me.Scope)
			if len(me.Apps) > 0 {
				who += ", limited to " + strings.Join(me.Apps, ", ")
			}
			fmt.Printf("  Signed in: %s\n", who)
		}
	})
}

func inviteCmd() *cobra.Command {
	var role string
	cmd := &cobra.Command{
		Use:   "invite",
		Short: "Make a link that adds a person to the organization (admin)",
		Long: `Make a single-use link that lets a person create an account (or join with
the one they have) in this token's organization. It works for 7 days.

Right after installing, run it to create your own account for the
dashboard:

  coldship invite --role admin`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := newClient().CreateInvite(cmd.Context(), role)
			if err != nil {
				return err
			}
			return emit(l, func() {
				fmt.Printf("Open this link in a browser to join as %s (works once, for 7 days):\n\n  %s\n\n", l.Role, l.URL)
				fmt.Println("If you reach the dashboard at another address, keep everything from /invite on.")
			})
		},
	}
	cmd.Flags().StringVar(&role, "role", string(store.ScopeAdmin), "admin | deploy | read")
	return cmd
}
