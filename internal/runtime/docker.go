package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"hangar/internal/apierr"
	"hangar/internal/spec"
)

// Docker drives the docker CLI. Shelling out keeps the dependency tree small
// for the starter; swap in the Docker Engine API client when needed.
type Docker struct {
	Bin string
	// StartupGrace is how long a new container must stay up before it counts
	// as started, on top of passing its health check.
	StartupGrace time.Duration
	// StopTimeout is how long an old container gets to shut down gracefully.
	StopTimeout time.Duration
}

func NewDocker() *Docker {
	return &Docker{Bin: "docker", StartupGrace: 3 * time.Second, StopTimeout: 10 * time.Second}
}

func ContainerName(app string) string      { return "hangar-" + app }
func nextName(app string) string           { return "hangar-" + app + "-next" }
func VolumeName(app, volume string) string { return "hangar-" + app + "-" + volume }

func (d *Docker) run(ctx context.Context, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, d.Bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), strings.TrimSpace(errb.String()), err
}

// EnsureNetwork creates the Hangar network if it doesn't exist.
func (d *Docker) EnsureNetwork(ctx context.Context) error {
	if _, _, err := d.run(ctx, "network", "inspect", Network); err == nil {
		return nil
	}
	if _, se, err := d.run(ctx, "network", "create", "--label", "hangar=true", Network); err != nil && !strings.Contains(se, "already exists") {
		return apierr.New(502, "runtime_unavailable", "could not create docker network %s: %s", Network, se)
	}
	return nil
}

func (d *Docker) Deploy(ctx context.Context, deploymentID string, app spec.App, opts DeployOptions) error {
	log := opts.Log
	if log == nil {
		log = io.Discard
	}
	if err := d.EnsureNetwork(ctx); err != nil {
		return err
	}
	// Pull before touching anything, so a bad image never causes downtime.
	if _, _, err := d.run(ctx, "image", "inspect", app.Image); err != nil {
		fmt.Fprintf(log, "hangar: pulling %s\n", app.Image)
		if _, se, err := d.run(ctx, "pull", app.Image); err != nil {
			return apierr.New(422, "image_pull_failed", "could not pull image %q: %s", app.Image, se).
				WithField("image").WithHint("check the image name and tag, and that the node can reach the registry")
		}
	}

	if opts.ZeroDowntime {
		return d.deployAlongside(ctx, deploymentID, app, opts, log)
	}
	return d.deployRecreate(ctx, deploymentID, app, opts, log)
}

// deployRecreate stops the old container, then starts the new one. Used when
// two containers can't run side by side (host port, volumes).
func (d *Docker) deployRecreate(ctx context.Context, depID string, app spec.App, opts DeployOptions, log io.Writer) error {
	fmt.Fprintln(log, "hangar: strategy recreate (host_port or volumes prevent running two versions side by side)")
	for _, n := range []string{nextName(app.Name), ContainerName(app.Name)} {
		if err := d.removeContainer(ctx, n); err != nil {
			return err
		}
	}
	if err := d.start(ctx, ContainerName(app.Name), depID, app, log); err != nil {
		return err
	}
	if err := d.waitHealthy(ctx, ContainerName(app.Name), app, log); err != nil {
		// Leave the failed container in place so diagnose and logs can see it.
		return err
	}
	if opts.Switch != nil && app.Port > 0 {
		if err := opts.Switch(ctx, Upstream(depID, app.Port)); err != nil {
			return err
		}
	}
	fmt.Fprintln(log, "hangar: deployed")
	return nil
}

// deployAlongside starts the new container next to the old one, waits for it
// to be healthy, switches traffic, and only then stops the old one.
func (d *Docker) deployAlongside(ctx context.Context, depID string, app spec.App, opts DeployOptions, log io.Writer) error {
	fmt.Fprintln(log, "hangar: strategy zero-downtime (new version starts next to the old one)")
	old, err := d.Status(ctx, app.Name)
	if err != nil {
		return err
	}
	prevRunning := old.Exists && old.Running
	fail := func(err error) error {
		d.removeContainer(context.WithoutCancel(ctx), nextName(app.Name))
		if ae, ok := apierr.As(err); ok && prevRunning {
			fmt.Fprintln(log, "hangar: the previous version is still running and serving traffic")
			return ae.WithDetail(DetailPreviousRunning, true)
		}
		return err
	}

	if err := d.removeContainer(ctx, nextName(app.Name)); err != nil {
		return err
	}
	if err := d.start(ctx, nextName(app.Name), depID, app, log); err != nil {
		return fail(err)
	}
	if err := d.waitHealthy(ctx, nextName(app.Name), app, log); err != nil {
		return fail(err)
	}
	if opts.Switch != nil && app.Port > 0 {
		fmt.Fprintln(log, "hangar: switching traffic to the new version")
		if err := opts.Switch(ctx, Upstream(depID, app.Port)); err != nil {
			return fail(err)
		}
	}
	// The new version is live. Problems from here on are logged, not fatal.
	if old.Exists {
		fmt.Fprintf(log, "hangar: stopping the previous version (up to %s for graceful shutdown)\n", d.StopTimeout)
		if _, se, err := d.run(ctx, "stop", "-t", strconv.Itoa(int(d.StopTimeout.Seconds())), ContainerName(app.Name)); err != nil && !strings.Contains(se, "No such container") {
			fmt.Fprintf(log, "hangar: warning: stopping the previous version failed: %s\n", se)
		}
		if err := d.removeContainer(ctx, ContainerName(app.Name)); err != nil {
			fmt.Fprintf(log, "hangar: warning: %v\n", err)
		}
	}
	if _, se, err := d.run(ctx, "rename", nextName(app.Name), ContainerName(app.Name)); err != nil {
		fmt.Fprintf(log, "hangar: warning: renaming the new container failed: %s\n", se)
	}
	fmt.Fprintln(log, "hangar: deployed")
	return nil
}

func (d *Docker) start(ctx context.Context, name, depID string, app spec.App, log io.Writer) error {
	// Env goes through a 0600 temp file so secret values never appear in the
	// process list.
	envFile, err := os.CreateTemp("", "hangar-env-*")
	if err != nil {
		return err
	}
	defer os.Remove(envFile.Name())
	keys := make([]string, 0, len(app.Env))
	for k := range app.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(envFile, "%s=%s\n", k, app.Env[k])
	}
	if err := envFile.Close(); err != nil {
		return err
	}

	args := []string{"run", "-d", "--name", name,
		"--label", "hangar.app=" + app.Name, "--label", "hangar.deployment=" + depID,
		"--network", Network, "--network-alias", Alias(depID), "--network-alias", app.Name,
		"--restart", "unless-stopped", "--env-file", envFile.Name()}
	if app.HostPort > 0 {
		args = append(args, "-p", fmt.Sprintf("%d:%d", app.HostPort, app.Port))
	}
	if app.Memory != "" {
		args = append(args, "--memory", app.Memory)
	}
	for _, v := range app.Volumes {
		args = append(args, "-v", VolumeName(app.Name, v.Name)+":"+v.Path)
	}
	args = append(args, app.Image)
	fmt.Fprintf(log, "hangar: starting %s from %s\n", name, app.Image)
	if _, se, err := d.run(ctx, args...); err != nil {
		e := apierr.New(502, "container_start_failed", "docker could not start the container: %s", se)
		if strings.Contains(se, "port is already allocated") || strings.Contains(se, "address already in use") {
			e.Code = "host_port_in_use"
			e.Field = "host_port"
			e.Hint = fmt.Sprintf("host port %d is used by another process or app; pick a free port, or use domains instead", app.HostPort)
		}
		return e
	}
	return nil
}

// waitHealthy waits until the container is running and, if the app has a
// port, accepts connections (or answers its health path with 2xx/3xx). On
// failure it writes the container's last output to log.
func (d *Docker) waitHealthy(ctx context.Context, name string, app spec.App, log io.Writer) error {
	started := time.Now()
	timeout := d.StartupGrace
	check := ""
	if app.Port > 0 {
		timeout = app.Health.TimeoutOrDefault()
		check = fmt.Sprintf("port %d accepting connections", app.Port)
		if app.Health != nil && app.Health.Path != "" {
			check = fmt.Sprintf("GET %s on port %d returning 2xx/3xx", app.Health.Path, app.Port)
		}
		fmt.Fprintf(log, "hangar: waiting up to %s for %s\n", timeout, check)
	}
	deadline := started.Add(timeout)
	last := "not checked yet"
	for {
		st, err := d.Status(ctx, name)
		if err != nil {
			return err
		}
		if !st.Exists || st.State != "running" || st.RestartCount > 0 {
			d.dumpLogs(ctx, name, log)
			return apierr.New(502, "container_exited", "container did not stay up after start (state=%s, exit_code=%d, restarts=%d, oom_killed=%t)",
				st.State, st.ExitCode, st.RestartCount, st.OOMKilled).
				WithHint("read the deploy log for the container's output, or call diagnose")
		}
		healthy := app.Port == 0
		if app.Port > 0 {
			healthy, last = d.probe(ctx, name, app)
		}
		if healthy && time.Since(started) >= d.StartupGrace {
			if check != "" {
				fmt.Fprintf(log, "hangar: healthy after %s (%s)\n", time.Since(started).Round(100*time.Millisecond), last)
			}
			return nil
		}
		if time.Now().After(deadline) && app.Port > 0 {
			d.dumpLogs(ctx, name, log)
			return apierr.New(502, "health_check_failed", "the new container did not become healthy within %s: expected %s, last result: %s", timeout, check, last).
				WithField("health").WithDetail("last_result", last).
				WithHint("check that the app listens on the spec's port (on 0.0.0.0, not 127.0.0.1) and that health.path is right; raise health.timeout for slow starts")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (d *Docker) probe(ctx context.Context, name string, app spec.App) (bool, string) {
	out, _, err := d.run(ctx, "inspect", "-f", `{{with index .NetworkSettings.Networks "`+Network+`"}}{{.IPAddress}}{{end}}`, name)
	ip := strings.TrimSpace(out)
	if err != nil || ip == "" {
		return false, "container has no address on the hangar network yet"
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(app.Port))
	if app.Health == nil || app.Health.Path == "" {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			return false, shortNetErr(err)
		}
		c.Close()
		return true, "port open"
	}
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, "GET", "http://"+addr+app.Health.Path, nil)
	req.Header.Set("User-Agent", "hangar-health-check")
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return false, shortNetErr(err)
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400, "HTTP " + strconv.Itoa(resp.StatusCode)
}

func shortNetErr(err error) string {
	s := err.Error()
	for _, k := range []string{"connection refused", "i/o timeout", "no route to host", "connection reset", "EOF"} {
		if strings.Contains(s, k) {
			return k
		}
	}
	return s
}

func (d *Docker) dumpLogs(ctx context.Context, name string, log io.Writer) {
	out, err := exec.CommandContext(context.WithoutCancel(ctx), d.Bin, "logs", "--tail", "50", name).CombinedOutput()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		fmt.Fprintln(log, "hangar: the container produced no output")
		return
	}
	fmt.Fprintln(log, "hangar: last output of the new container:")
	log.Write(out)
	if !bytes.HasSuffix(out, []byte("\n")) {
		fmt.Fprintln(log)
	}
}

func (d *Docker) removeContainer(ctx context.Context, name string) error {
	if _, se, err := d.run(ctx, "rm", "-f", name); err != nil && !strings.Contains(se, "No such container") {
		return apierr.New(502, "container_remove_failed", "could not remove container %s: %s", name, se)
	}
	return nil
}

func (d *Docker) Remove(ctx context.Context, app string, volumes []string) error {
	for _, n := range []string{nextName(app), ContainerName(app)} {
		if err := d.removeContainer(ctx, n); err != nil {
			return err
		}
	}
	return d.RemoveVolumes(ctx, app, volumes)
}

func (d *Docker) RemoveVolumes(ctx context.Context, app string, volumes []string) error {
	for _, v := range volumes {
		if _, se, err := d.run(ctx, "volume", "rm", VolumeName(app, v)); err != nil && !strings.Contains(se, "no such volume") {
			return apierr.New(502, "volume_remove_failed", "could not remove volume %s: %s", v, se)
		}
	}
	return nil
}

func (d *Docker) Status(ctx context.Context, app string) (Status, error) {
	name := app
	if !strings.HasPrefix(app, "hangar-") {
		name = ContainerName(app)
	}
	out, se, err := d.run(ctx, "inspect", "--type", "container", name)
	if err != nil {
		if strings.Contains(se, "No such") {
			return Status{}, nil
		}
		return Status{}, apierr.New(502, "runtime_unavailable", "docker inspect failed: %s", se)
	}
	var res []struct {
		State struct {
			Status     string
			Running    bool
			Restarting bool
			OOMKilled  bool
			ExitCode   int
			StartedAt  string
			Error      string
		}
		RestartCount int
		Config       struct{ Image string }
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res) == 0 {
		return Status{}, fmt.Errorf("parse docker inspect: %v", err)
	}
	r := res[0]
	return Status{
		Exists: true, State: r.State.Status, Running: r.State.Running && !r.State.Restarting,
		ExitCode: r.State.ExitCode, OOMKilled: r.State.OOMKilled, RestartCount: r.RestartCount,
		StartedAt: r.State.StartedAt, Image: r.Config.Image, Error: r.State.Error,
	}, nil
}

func (d *Docker) Logs(ctx context.Context, app string, tail int, since string) ([]string, error) {
	args := []string{"logs", "--tail", fmt.Sprint(tail)}
	if since != "" {
		args = append(args, "--since", since)
	}
	args = append(args, ContainerName(app))
	cmd := exec.CommandContext(ctx, d.Bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := strings.TrimSpace(string(out))
		if strings.Contains(s, "No such container") {
			return []string{}, nil
		}
		return nil, apierr.New(502, "logs_unavailable", "docker logs failed: %s", s)
	}
	return splitLines(string(out)), nil
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return []string{}
	}
	return strings.Split(s, "\n")
}
