package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/dunkbing/coldship/internal/agents"
	"github.com/dunkbing/coldship/internal/engine"
	"github.com/dunkbing/coldship/internal/hostinfo"
	"github.com/dunkbing/coldship/internal/store"
)

// The overview is the org at a glance: its apps' health, recent deploys,
// and what needs someone. "Needs attention" is worked out from how things
// are now, so it never shows an alert that has since cleared.

type Attention struct {
	Kind     string     `json:"kind"`     // app_failing | deploy_failed | job_failed | approval | agent_waiting | data_stopped | backup_failing | disk_low | update
	Severity string     `json:"severity"` // error | warning | info
	Title    string     `json:"title"`
	Detail   string     `json:"detail,omitempty"` // may quote untrusted text (errors)
	Link     string     `json:"link"`             // a dashboard path
	App      string     `json:"app,omitempty"`
	At       *time.Time `json:"at,omitempty"`
}

type OverviewView struct {
	Apps      []*engine.AppView  `json:"apps"`
	Previews  int                `json:"previews"`
	Data      OverviewCount      `json:"data"`
	Deploys   []store.Deployment `json:"deploys"`
	Attention []Attention        `json:"attention"`
	Agents    *OverviewAgents    `json:"agents,omitempty"` // when the caller can use agents
	Server    *OverviewServer    `json:"server,omitempty"` // for the default org's members
	Jobs      int                `json:"jobs"`             // scheduled jobs across the apps
}

type OverviewCount struct {
	Total   int `json:"total"`
	Running int `json:"running"`
}

type OverviewAgents struct {
	Configured int                   `json:"configured"`
	Live       int                   `json:"live"`
	Recent     []*agents.SessionView `json:"recent"`
}

type OverviewServer struct {
	Hostname string          `json:"hostname"`
	Version  string          `json:"version"`
	Load     [3]float64      `json:"load"`
	CPUs     int             `json:"cpus"`
	Memory   hostinfo.Usage  `json:"memory"`
	Disks    []hostinfo.Disk `json:"disks"`
	Uptime   int64           `json:"uptime_seconds"`
}

const (
	overviewDeploys  = 8
	overviewSessions = 5
	diskWarn         = 0.90
)

func (s *Server) overview(r *http.Request, p principal) (any, error) {
	ctx := r.Context()
	all, err := s.eng.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	v := &OverviewView{Apps: []*engine.AppView{}, Deploys: []store.Deployment{}, Attention: []Attention{}}
	var urgent, later []Attention // errors first, then the rest
	add := func(a Attention) {
		if a.Severity == "error" {
			urgent = append(urgent, a)
		} else {
			later = append(later, a)
		}
	}

	for _, a := range visibleApps(p, all) {
		if a.PreviewOf != "" {
			v.Previews++
			continue
		}
		av, err := s.eng.GetApp(ctx, a.Name)
		if err != nil {
			continue // deleted meanwhile
		}
		v.Apps = append(v.Apps, av)
		link := "/apps/" + a.Name
		ld := av.LatestDeployment
		switch h := appHealth(av); {
		case h == "stopped":
			add(Attention{Kind: "app_failing", Severity: "error", App: a.Name, Link: link, Title: a.Name + " isn't running", Detail: runtimeDetail(av)})
		case h == "failing" && (ld == nil || ld.Status != store.DeployFailed):
			add(Attention{Kind: "app_failing", Severity: "error", App: a.Name, Link: link, Title: a.Name + " is failing", Detail: runtimeDetail(av)})
		}
		if ld != nil && ld.Status == store.DeployFailed {
			at := ld.CreatedAt
			att := Attention{Kind: "deploy_failed", Severity: "error", App: a.Name, Link: link + "?tab=deployments", At: &at,
				Title: a.Name + ": the last deploy failed", Detail: clip(ld.Error, 200)}
			if ld.KeptPrevious {
				att.Severity, att.Title = "warning", a.Name+": the last deploy failed; the previous version still runs"
			}
			add(att)
		}
		if a.Spec != nil && len(a.Spec.Jobs) > 0 {
			v.Jobs += len(a.Spec.Jobs)
			if jobs, err := s.eng.Jobs(ctx, a.Name); err == nil {
				for _, j := range jobs {
					if l := j.Last; l != nil && (l.Status == store.JobFailed || l.Status == store.JobTimedOut) {
						at := l.StartedAt
						add(Attention{Kind: "job_failed", Severity: "warning", App: a.Name, Link: link + "?tab=jobs", At: &at,
							Title:  fmt.Sprintf("%s: job %s %s", a.Name, j.Name, strings.ReplaceAll(l.Status, "_", " ")),
							Detail: clip(l.Error, 200)})
					}
				}
			}
		}
		if deps, err := s.eng.Deployments(ctx, a.Name, overviewDeploys); err == nil {
			for _, d := range deps {
				d.Spec = nil
				v.Deploys = append(v.Deploys, d)
			}
		}
	}
	slices.SortFunc(v.Deploys, func(a, b store.Deployment) int { return b.CreatedAt.Compare(a.CreatedAt) })
	if len(v.Deploys) > overviewDeploys {
		v.Deploys = v.Deploys[:overviewDeploys]
	}

	// Approvals, as the plans list shows them.
	if plans, err := s.eng.ListPlans(ctx, store.PlanPending, 100); err == nil {
		for _, pl := range plans {
			if store.OrgOr(pl.Org) != p.org || !pl.RequiresApproval || pl.ApprovedBy != "" {
				continue
			}
			if p.limited() && s.allowApp(ctx, p, pl.App) != nil {
				continue
			}
			at := pl.CreatedAt
			add(Attention{Kind: "approval", Severity: "warning", App: pl.App, Link: "/approvals", At: &at,
				Title: fmt.Sprintf("%s: a change by %s waits for approval", pl.App, pl.CreatedBy)})
		}
	}

	// Data services, as the data list shows them.
	if list, err := s.eng.ListData(ctx); err == nil {
		for _, d := range list {
			if d.Org != p.org || d.App != "" {
				continue
			}
			if p.limited() && !store.MatchApps(p.apps, d.Name) {
				continue
			}
			v.Data.Total++
			if d.Status.Running {
				v.Data.Running++
			} else {
				add(Attention{Kind: "data_stopped", Severity: "error", Link: "/data/" + d.Name, Title: d.Name + " (" + d.Engine + ") isn't running"})
			}
		}
	}

	if s.agentAccess(p) == nil {
		ag := &OverviewAgents{Recent: []*agents.SessionView{}}
		if list, err := s.agents.List(ctx, p.org); err == nil {
			ag.Configured = len(list)
		}
		for _, ss := range s.agents.Sessions(p.org) {
			live := ss.Status != agents.StatusEnded && ss.Status != agents.StatusFailed
			if live {
				ag.Live++
			}
			if ss.Waiting {
				at := ss.UpdatedAt
				title := "An agent waits for an answer"
				if ss.Title != "" {
					title = "An agent waits for an answer: " + clip(ss.Title, 80)
				}
				add(Attention{Kind: "agent_waiting", Severity: "warning", App: ss.AlertApp, Link: "/agents/sessions/" + ss.ID, At: &at, Title: title})
			}
			if len(ag.Recent) < overviewSessions {
				ag.Recent = append(ag.Recent, ss)
			}
		}
		v.Agents = ag
	}

	if !p.limited() {
		if b, err := s.eng.BackupStatus(ctx); err == nil && b.Configured && b.LastError != "" {
			add(Attention{Kind: "backup_failing", Severity: "error", Link: "/backups", Title: "Off-site backups are failing", Detail: clip(b.LastError, 200), At: b.LastSync})
		}
	}

	// The host is the default org's business.
	if !p.limited() && p.org == store.DefaultOrg {
		in := s.eng.Glance()
		v.Server = &OverviewServer{Hostname: in.Hostname, Version: s.version(), Load: in.Load, CPUs: in.CPUs, Memory: in.Memory, Disks: in.Disks, Uptime: in.UptimeS}
		for _, d := range in.Disks {
			if d.Total > 0 && float64(d.Used)/float64(d.Total) >= diskWarn {
				add(Attention{Kind: "disk_low", Severity: "error", Link: "/server",
					Title: fmt.Sprintf("The disk with %s is %d%% full", d.For, d.Used*100/d.Total)})
			}
		}
		if s.upd != nil && p.scope == store.ScopeAdmin {
			if st := s.upd.Status(); st.UpdateAvailable {
				add(Attention{Kind: "update", Severity: "info", Link: "/server", Title: "Coldship " + st.Latest + " is available (this server runs " + st.Version + ")"})
			}
		}
	}

	v.Attention = append(append(v.Attention, urgent...), later...)
	return v, nil
}

// appHealth matches the dashboard's healthOf.
func appHealth(v *engine.AppView) string {
	ld := v.LatestDeployment
	if ld != nil && (ld.Status == store.DeployInProgress || ld.Status == store.DeployBuilding) {
		return "deploying"
	}
	rt := v.Runtime
	switch {
	case rt == nil:
		return "unknown"
	case !rt.Exists:
		return "stopped"
	case rt.Running && rt.RestartCount == 0 && !(ld != nil && ld.Status == store.DeployFailed && !ld.KeptPrevious):
		return "healthy"
	}
	return "failing"
}

func runtimeDetail(v *engine.AppView) string {
	rt := v.Runtime
	switch {
	case rt == nil:
		return ""
	case rt.OOMKilled:
		return "It ran out of memory."
	case !rt.Exists:
		return "Its container is gone."
	case !rt.Running:
		return fmt.Sprintf("It exited with code %d.", rt.ExitCode)
	case rt.RestartCount > 0:
		return fmt.Sprintf("It restarted %d times.", rt.RestartCount)
	}
	return ""
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
