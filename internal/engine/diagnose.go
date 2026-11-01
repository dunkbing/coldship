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
	// BuildLog is the tail of the latest deployment's build log when that
	// build failed.
	BuildLog []string `json:"build_log,omitempty"`
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

	// The container that produced the logs runs the latest deployment's spec,
	// which differs from the last good spec when the latest deploy failed.
	running := app.Spec
	if ld := d.LatestDeployment; ld != nil && ld.Status == store.DeployFailed && ld.FailedIn != store.PhaseBuild {
		if deps, err := e.st.ListDeployments(ctx, name, 1); err == nil && len(deps) > 0 && deps[0].Spec != nil {
			running = deps[0].Spec
		}
	}

	add := func(sev, code, msg string, fix any) {
		d.Findings = append(d.Findings, Finding{Severity: sev, Code: code, Message: msg, SuggestedFix: fix})
	}
	rollbackFix := map[string]any{"tool": "rollback", "app": name}

	if ld := d.LatestDeployment; ld != nil && ld.Status == store.DeployFailed {
		if ld.FailedIn == store.PhaseBuild {
			// A failed build never touched the container: the previous
			// version keeps running, so this is not an outage.
			add("warning", "build_failed", fmt.Sprintf("latest deployment %s failed to build, so the previous version is still running: %s",
				ld.ID, ld.Error), map[string]any{"tool": "build_log", "deployment": ld.ID})
			if lines, err := e.st.GetBuildLog(ctx, ld.ID); err == nil && len(lines) > 0 {
				if len(lines) > 40 {
					lines = lines[len(lines)-40:]
				}
				d.BuildLog = lines
				d.LogsNote += " " + buildLogNote
			}
		} else {
			add("error", "last_deploy_failed", fmt.Sprintf("latest deployment %s failed (%s): %s", ld.ID, ld.ErrorCode, ld.Error), rollbackFix)
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
		for i := len(logs) - 1; i >= 0; i-- {
			if m := listenRe.FindStringSubmatch(logs[i]); m != nil {
				if p, _ := strconv.Atoi(m[1]); p > 0 && p != running.Port {
					add("warning", "port_mismatch", fmt.Sprintf("the app seems to listen on port %d but the spec says port %d", p, running.Port),
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
	if name := missingEnvVar(logs, running); name != "" {
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
