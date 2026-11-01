package engine

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"hangar/internal/apierr"
	"hangar/internal/runtime"
	"hangar/internal/spec"
	"hangar/internal/store"
)

type Finding struct {
	Severity     string `json:"severity"` // info|warning|error
	Code         string `json:"code"`
	Message      string `json:"message"`
	SuggestedFix any    `json:"suggested_fix,omitempty"`
}

// Diagnosis is one context-sized bundle: everything an agent needs to work
// out why an app is unhealthy without making five separate calls.
type Diagnosis struct {
	App              string            `json:"app"`
	Healthy          bool              `json:"healthy"`
	Findings         []Finding         `json:"findings"`
	Runtime          runtime.Status    `json:"runtime"`
	Spec             *spec.App         `json:"spec,omitempty"`
	LatestDeployment *store.Deployment `json:"latest_deployment,omitempty"`
	RecentLogs       []string          `json:"recent_logs"`
	LogsNote         string            `json:"logs_note"`
}

var (
	listenRe     = regexp.MustCompile(`(?i)\b(?:listening|running|started|serving|bound)\b.*?(?:port\s*|:)(\d{2,5})\b`)
	addrInUseRe  = regexp.MustCompile(`(?i)address already in use|EADDRINUSE`)
	permissionRe = regexp.MustCompile(`(?i)permission denied|EACCES`)
	missingEnvRe = regexp.MustCompile(`(?i)(?:missing|required|not set|undefined).{0,40}?\b([A-Z][A-Z0-9_]{2,})\b`)
)

func (e *Engine) Diagnose(ctx context.Context, name string) (*Diagnosis, error) {
	app, err := e.st.GetApp(ctx, name)
	if err != nil {
		return nil, err
	}
	if app == nil {
		return nil, apierr.NotFound("app", name)
	}
	st, err := e.rt.Status(ctx, name)
	if err != nil {
		return nil, err
	}
	logs, err := e.rt.Logs(ctx, name, 50, "")
	if err != nil {
		logs = []string{}
	}
	d := &Diagnosis{App: name, Runtime: st, Spec: app.Spec.Redacted(), RecentLogs: logs, LogsNote: untrustedNote, Findings: []Finding{}}
	if deps, err := e.st.ListDeployments(ctx, name, 1); err == nil && len(deps) > 0 {
		d.LatestDeployment = publicDeployment(&deps[0])
	}

	add := func(sev, code, msg string, fix any) {
		d.Findings = append(d.Findings, Finding{Severity: sev, Code: code, Message: msg, SuggestedFix: fix})
	}
	rollbackFix := map[string]any{"tool": "rollback", "app": name}

	if ld := d.LatestDeployment; ld != nil && ld.Status == store.DeployFailed {
		add("error", "last_deploy_failed", fmt.Sprintf("latest deployment %s failed: %s", ld.ID, ld.Error), rollbackFix)
	}
	switch {
	case !st.Exists:
		add("error", "not_running", "no container exists for this app", map[string]any{"tool": "plan", "redeploy": true})
	case st.OOMKilled:
		fix := map[string]any{"memory": "512m"}
		if app.Spec != nil && app.Spec.Memory != "" {
			fix["memory"] = doubleMemory(app.Spec.Memory)
		}
		add("error", "oom_killed", "the container was killed for running out of memory", fix)
	case st.RestartCount > 2:
		add("error", "crash_loop", fmt.Sprintf("the container has restarted %d times (last exit code %d)", st.RestartCount, st.ExitCode), nil)
	case !st.Running:
		add("error", "exited", fmt.Sprintf("the container is %s with exit code %d", st.State, st.ExitCode), nil)
	}

	if app.Spec != nil && app.Spec.Port > 0 {
		for i := len(logs) - 1; i >= 0; i-- {
			if m := listenRe.FindStringSubmatch(logs[i]); m != nil {
				if p, _ := strconv.Atoi(m[1]); p > 0 && p != app.Spec.Port {
					add("warning", "port_mismatch", fmt.Sprintf("the app seems to listen on port %d but the spec says port %d", p, app.Spec.Port),
						map[string]any{"port": p})
				}
				break
			}
		}
	}
	joined := strings.Join(logs, "\n")
	if addrInUseRe.MatchString(joined) {
		add("error", "address_in_use", "the app reports its address is already in use inside the container", nil)
	}
	if permissionRe.MatchString(joined) {
		add("warning", "permission_denied", "the logs mention permission errors; check volume paths and the image's user", nil)
	}
	if m := missingEnvRe.FindStringSubmatch(joined); m != nil && app.Spec != nil {
		if _, ok := app.Spec.Env[m[1]]; !ok {
			add("warning", "missing_env", fmt.Sprintf("the logs mention %s, which is not set in the spec's env", m[1]),
				map[string]any{"env": map[string]string{m[1]: "<value>"}})
		}
	}

	d.Healthy = st.Running && st.RestartCount == 0
	for _, f := range d.Findings {
		if f.Severity == "error" {
			d.Healthy = false
		}
	}
	if d.Healthy && len(d.Findings) == 0 {
		add("info", "healthy", "the container is running and no problems were found", nil)
	}
	return d, nil
}

func doubleMemory(m string) string {
	n, err := strconv.Atoi(m[:len(m)-1])
	if err != nil {
		return m
	}
	return strconv.Itoa(n*2) + m[len(m)-1:]
}
