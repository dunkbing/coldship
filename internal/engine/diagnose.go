package engine

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
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
	// DeployLog is the end of the latest deployment's log when it failed:
	// build output, health checks and the failed container's output.
	DeployLog []string `json:"deploy_log,omitempty"`
}

var (
	listenRe     = regexp.MustCompile(`(?i)\b(?:listening|running|started|serving|bound)\b.*?(?:port\s*|:)(\d{2,5})\b`)
	addrInUseRe  = regexp.MustCompile(`(?i)address already in use|EADDRINUSE`)
	permissionRe = regexp.MustCompile(`(?i)permission denied|EACCES`)
	missingKwRe  = regexp.MustCompile(`(?i)\b(?:missing|required|not set|undefined|unset)\b`)
	envNameRe    = regexp.MustCompile(`\b[A-Z][A-Z0-9]*_[A-Z0-9_]+\b|\b[A-Z]{3,}\b`) // case-sensitive on purpose
	logWords     = map[string]bool{"ERROR": true, "FATAL": true, "WARN": true, "WARNING": true, "INFO": true, "DEBUG": true, "PANIC": true}
)

func (e *Engine) Diagnose(ctx context.Context, name string) (*Diagnosis, error) {
	app, err := e.st.GetApp(ctx, name)
	if err != nil {
		return nil, err
	}
	if app == nil {
		return nil, apierr.NotFound("app", name)
	}
	static := e.isStatic(ctx, app)
	var st runtime.Status
	var logs []string
	if static {
		// Served by the proxy: there is no container, and no app logs.
		st, logs = staticStatus, []string{}
	} else {
		var err error
		if st, err = e.rt.Status(ctx, name); err != nil {
			return nil, err
		}
		if logs, err = e.rt.Logs(ctx, name, 50, ""); err != nil {
			logs = []string{}
		}
	}
	d := &Diagnosis{App: name, Runtime: st, Spec: app.Spec.Redacted(), RecentLogs: logs, LogsNote: untrustedNote, Findings: []Finding{}}
	if deps, err := e.st.ListDeployments(ctx, name, 1); err == nil && len(deps) > 0 {
		d.LatestDeployment = publicDeployment(&deps[0])
	}

	// Work out which spec and which output explain the current state:
	//  - healthy or failed build: the running container, current spec
	//  - failed deploy that replaced the container: its logs, the failed spec
	//  - failed deploy that kept the previous version: the new container's
	//    output (captured in the deploy log) and the failed spec
	running, evidence := app.Spec, logs
	var failed *store.Deployment
	if ld := d.LatestDeployment; ld != nil && ld.Status == store.DeployFailed {
		if deps, err := e.st.ListDeployments(ctx, name, 1); err == nil && len(deps) > 0 {
			failed = &deps[0]
		}
		if lines, err := e.st.GetDeployLog(ctx, ld.ID); err == nil && len(lines) > 0 {
			if len(lines) > 40 {
				lines = lines[len(lines)-40:]
			}
			d.DeployLog = lines
			d.LogsNote += " " + deployLogNote
		}
	}
	if failed != nil && failed.FailedIn == store.PhaseDeploy && failed.Spec != nil {
		running = failed.Spec
		if failed.KeptPrevious {
			evidence = d.DeployLog
		}
	}

	add := func(sev, code, msg string, fix any) {
		d.Findings = append(d.Findings, Finding{Severity: sev, Code: code, Message: msg, SuggestedFix: fix})
	}
	rollbackFix := map[string]any{"tool": "rollback", "app": name}

	if ld := d.LatestDeployment; ld != nil && ld.Status == store.DeployFailed {
		logFix := map[string]any{"tool": "deploy_log", "deployment": ld.ID}
		switch {
		case ld.KeptPrevious && ld.FailedIn == store.PhaseBuild:
			// Not an outage: the previous version keeps serving.
			add("warning", "build_failed", fmt.Sprintf("latest deployment %s failed to build, so the previous version is still running: %s",
				ld.ID, ld.Error), logFix)
		case ld.KeptPrevious:
			add("warning", "new_version_unhealthy", fmt.Sprintf("latest deployment %s never became healthy (%s), so traffic stayed on the previous version: %s",
				ld.ID, ld.ErrorCode, ld.Error), logFix)
		default:
			add("error", "last_deploy_failed", fmt.Sprintf("latest deployment %s failed (%s): %s", ld.ID, ld.ErrorCode, ld.Error), rollbackFix)
		}
	}
	if app.Spec != nil {
		for _, db := range app.Spec.Databases {
			if st, err := e.rt.DatabaseStatus(ctx, name, db.Name); err == nil && !st.Running {
				state := st.State
				if !st.Exists {
					state = "missing"
				}
				add("error", "database_down", fmt.Sprintf("database %s (%s %s) is %s, so the app can't reach it", db.Name, db.Engine, db.Version, state),
					map[string]any{"tool": "plan", "redeploy": true})
			}
		}
	}
	if app.Spec != nil && len(app.Spec.Domains) > 0 && e.px != nil {
		if ps := e.px.Status(ctx); !ps.Running {
			add("error", "proxy_down", "the proxy container (coldship-proxy) is not running, so the app's domains are unreachable",
				map[string]any{"action": "restart coldship server, which starts the proxy"})
		}
	}
	switch {
	case !st.Exists:
		add("error", "not_running", "no container exists for this app", map[string]any{"tool": "plan", "redeploy": true})
	case st.OOMKilled:
		fix := map[string]any{"memory": "512m"}
		if running != nil && running.Memory != "" {
			fix["memory"] = doubleMemory(running.Memory)
		}
		add("error", "oom_killed", "the container was killed for running out of memory", fix)
	case st.RestartCount > 2:
		add("error", "crash_loop", fmt.Sprintf("the container has restarted %d times (last exit code %d)", st.RestartCount, st.ExitCode), nil)
	case !st.Running:
		add("error", "exited", fmt.Sprintf("the container is %s with exit code %d", st.State, st.ExitCode), nil)
	}

	if running != nil && running.Port > 0 {
		for i := len(evidence) - 1; i >= 0; i-- {
			if m := listenRe.FindStringSubmatch(evidence[i]); m != nil {
				if p, _ := strconv.Atoi(m[1]); p > 0 && p != running.Port {
					add("warning", "port_mismatch", fmt.Sprintf("the app seems to listen on port %d but the spec says port %d", p, running.Port),
						map[string]any{"port": p})
				}
				break
			}
		}
	}
	joined := strings.Join(evidence, "\n")
	if addrInUseRe.MatchString(joined) {
		add("error", "address_in_use", "the app reports its address is already in use inside the container", nil)
	}
	if permissionRe.MatchString(joined) {
		add("warning", "permission_denied", "the logs mention permission errors; check volume paths and the image's user", nil)
	}
	if name := missingEnvVar(evidence, running); name != "" {
		add("warning", "missing_env", fmt.Sprintf("the logs say %s is missing, and it is not set in the spec's env", name),
			map[string]any{"env": map[string]string{name: "<value>"}})
	}

	d.Healthy = st.Running && st.RestartCount == 0
	for _, f := range d.Findings {
		if f.Severity == "error" {
			d.Healthy = false
		}
	}
	if d.Healthy && len(d.Findings) == 0 {
		if static {
			add("info", "healthy", "served as static files by the proxy (no container) and no problems were found", nil)
		} else {
			add("info", "healthy", "the container is running and no problems were found", nil)
		}
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

// missingEnvVar finds an UPPER_CASE name on a log line that complains about
// something missing, which is not set in the spec's env.
func missingEnvVar(logs []string, running *spec.App) string {
	if running == nil {
		return ""
	}
	for _, line := range logs {
		if !missingKwRe.MatchString(line) {
			continue
		}
		for _, name := range envNameRe.FindAllString(line, -1) {
			if _, set := running.Env[name]; !set && !logWords[name] {
				return name
			}
		}
	}
	return ""
}
