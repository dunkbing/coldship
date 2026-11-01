package runtime

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/spec"
)

// Docker drives the local Docker daemon through the Engine API (the official
// Go SDK). It honours DOCKER_HOST like the docker CLI does.
type Docker struct {
	cli     *client.Client
	initErr error
	// Host is the engine socket in use, for messages.
	Host string

	engineOnce sync.Once
	podman     bool
	// StartupGrace is how long a new container must stay up before it counts
	// as started, on top of passing its health check.
	StartupGrace time.Duration
	// StopTimeout is how long an old container gets to shut down gracefully.
	StopTimeout time.Duration
}

// Engine sockets tried, in order, when DOCKER_HOST isn't set: Docker's,
// then rootful Podman's (its Docker-compatible API).
var defaultSockets = []string{"/var/run/docker.sock", "/run/podman/podman.sock"}

func NewDocker() *Docker {
	opts := []client.Opt{client.FromEnv}
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		for _, s := range defaultSockets {
			if _, err := os.Stat(s); err == nil {
				host = "unix://" + s
				opts = append(opts, client.WithHost(host))
				break
			}
		}
	}
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	cli, err := client.New(opts...)
	return &Docker{cli: cli, initErr: err, Host: host, StartupGrace: 3 * time.Second, StopTimeout: 10 * time.Second}
}

// IsPodman reports whether the engine is Podman (through its
// Docker-compatible API) rather than Docker.
func (d *Docker) IsPodman(ctx context.Context) bool {
	d.engineOnce.Do(func() {
		cli, err := d.api()
		if err != nil {
			return
		}
		v, err := cli.ServerVersion(ctx, client.ServerVersionOptions{})
		if err != nil {
			return
		}
		for _, c := range v.Components {
			if strings.Contains(strings.ToLower(c.Name), "podman") {
				d.podman = true
			}
		}
		if strings.Contains(strings.ToLower(v.Platform.Name), "podman") {
			d.podman = true
		}
	})
	return d.podman
}

func ContainerName(app string) string      { return "coldship-" + app }
func nextName(app string) string           { return "coldship-" + app + "-next" }
func VolumeName(app, volume string) string { return "coldship-" + app + "-" + volume }

// Names lists the Docker names an app's resources use: hostnames on the
// Coldship network (container names and network aliases) and volume names.
// Names are joined with single dashes, so different apps can map to the same
// name (app web-db-main vs database main of app web); callers check Names
// against other apps before creating anything.
func Names(a *spec.App) (hosts, volumes []string) {
	hosts = []string{ContainerName(a.Name), nextName(a.Name), a.Name}
	for _, v := range a.Volumes {
		volumes = append(volumes, VolumeName(a.Name, v.Name))
	}
	for _, db := range a.Databases {
		hosts = append(hosts, dbContainer(a.Name, db.Name), DBHost(a.Name, db.Name))
		volumes = append(volumes, dbVolume(a.Name, db.Name))
	}
	return hosts, volumes
}

// ReservedHosts and ReservedVolumes are Docker names Coldship itself uses.
var (
	ReservedHosts   = []string{"coldship-proxy"}
	ReservedVolumes = []string{"coldship-proxy-data", "coldship-proxy-config"}
)

// Ping checks that the Docker daemon is reachable and usable by this user,
// and says how to fix it if not.
func (d *Docker) Ping(ctx context.Context) error {
	cli, err := d.api()
	if err != nil {
		return err
	}
	if _, err := cli.ServerVersion(ctx, client.ServerVersionOptions{}); err != nil {
		return unavailable("can't use Docker", err)
	}
	return nil
}

// EnsureNetwork creates the Coldship network if it doesn't exist.
func (d *Docker) EnsureNetwork(ctx context.Context) error {
	cli, err := d.api()
	if err != nil {
		return err
	}
	if _, err := cli.NetworkInspect(ctx, Network, client.NetworkInspectOptions{}); err == nil {
		return nil
	} else if !isNotFound(err) {
		return unavailable("could not inspect docker network "+Network, err)
	}
	_, err = cli.NetworkCreate(ctx, Network, client.NetworkCreateOptions{Driver: "bridge", Labels: map[string]string{"coldship": "true"}})
	if err != nil && !cerrdefs.IsConflict(err) {
		return unavailable("could not create docker network "+Network, err)
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
	if err := d.EnsureImage(ctx, app.Image, log); err != nil {
		if ae, ok := apierr.As(err); ok {
			return ae
		}
		return apierr.New(422, "image_pull_failed", "could not pull image %q: %v", app.Image, err).
			WithField("image").WithHint("check the image name and tag, and that the node can reach the registry")
	}

	if opts.ZeroDowntime {
		return d.deployAlongside(ctx, deploymentID, app, opts, log)
	}
	return d.deployRecreate(ctx, deploymentID, app, opts, log)
}

// deployRecreate stops the old container, then starts the new one. Used when
// two containers can't run side by side (host port, volumes).
func (d *Docker) deployRecreate(ctx context.Context, depID string, app spec.App, opts DeployOptions, log io.Writer) error {
	fmt.Fprintln(log, "coldship: strategy recreate (host_port or volumes prevent running two versions side by side)")
	for _, n := range []string{nextName(app.Name), ContainerName(app.Name)} {
		if err := d.removeContainer(ctx, n, app.Name); err != nil {
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
	fmt.Fprintln(log, "coldship: deployed")
	return nil
}

// deployAlongside starts the new container next to the old one, waits for it
// to be healthy, switches traffic, and only then stops the old one.
func (d *Docker) deployAlongside(ctx context.Context, depID string, app spec.App, opts DeployOptions, log io.Writer) error {
	fmt.Fprintln(log, "coldship: strategy zero-downtime (new version starts next to the old one)")
	old, err := d.Status(ctx, app.Name)
	if err != nil {
		return err
	}
	prevRunning := old.Exists && old.Running
	fail := func(err error) error {
		d.removeContainer(context.WithoutCancel(ctx), nextName(app.Name), app.Name)
		if ae, ok := apierr.As(err); ok && prevRunning {
			fmt.Fprintln(log, "coldship: the previous version is still running and serving traffic")
			return ae.WithDetail(DetailPreviousRunning, true)
		}
		return err
	}

	if err := d.removeContainer(ctx, nextName(app.Name), app.Name); err != nil {
		return err
	}
	if err := d.start(ctx, nextName(app.Name), depID, app, log); err != nil {
		return fail(err)
	}
	if err := d.waitHealthy(ctx, nextName(app.Name), app, log); err != nil {
		return fail(err)
	}
	if opts.Switch != nil && app.Port > 0 {
		fmt.Fprintln(log, "coldship: switching traffic to the new version")
		if err := opts.Switch(ctx, Upstream(depID, app.Port)); err != nil {
			return fail(err)
		}
	}
	// The new version is live. Problems from here on are logged, not fatal.
	if old.Exists {
		if err := d.checkOwner(ctx, ContainerName(app.Name), app.Name, ""); err != nil {
			return fail(err)
		}
		fmt.Fprintf(log, "coldship: stopping the previous version (up to %s for graceful shutdown)\n", d.StopTimeout)
		if err := d.stopContainer(ctx, ContainerName(app.Name), d.StopTimeout); err != nil {
			fmt.Fprintf(log, "coldship: warning: stopping the previous version failed: %v\n", err)
		}
		if err := d.removeContainer(ctx, ContainerName(app.Name), app.Name); err != nil {
			fmt.Fprintf(log, "coldship: warning: %v\n", err)
		}
	}
	if cli, err := d.api(); err == nil {
		if _, err := cli.ContainerRename(ctx, nextName(app.Name), client.ContainerRenameOptions{NewName: ContainerName(app.Name)}); err != nil {
			fmt.Fprintf(log, "coldship: warning: renaming the new container failed: %v\n", err)
		}
	}
	fmt.Fprintln(log, "coldship: deployed")
	return nil
}

func (d *Docker) start(ctx context.Context, name, depID string, app spec.App, log io.Writer) error {
	keys := make([]string, 0, len(app.Env))
	for k := range app.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+app.Env[k])
	}
	cs := ContainerSpec{Name: name, Image: app.Image, Env: env,
		Labels:  map[string]string{"coldship.app": app.Name, "coldship.deployment": depID},
		Aliases: []string{Alias(depID), app.Name}, Memory: parseMemory(app.Memory)}
	if app.HostPort > 0 {
		cs.Ports = map[int]int{app.HostPort: app.Port}
	}
	for _, v := range app.Volumes {
		cs.Binds = append(cs.Binds, VolumeName(app.Name, v.Name)+":"+v.Path)
	}
	fmt.Fprintf(log, "coldship: starting %s from %s\n", name, app.Image)
	if se, err := d.RunContainer(ctx, cs); err != nil {
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
		fmt.Fprintf(log, "coldship: waiting up to %s for %s\n", timeout, check)
	}
	deadline := started.Add(timeout)
	last := "not checked yet"
	for {
		st, err := d.inspect(ctx, name)
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
				fmt.Fprintf(log, "coldship: healthy after %s (%s)\n", time.Since(started).Round(100*time.Millisecond), last)
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
	info, err := d.InspectContainer(ctx, name)
	ip := info.IP
	if err != nil || ip == "" {
		return false, "container has no address on the coldship network yet"
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
	req.Header.Set("User-Agent", "coldship-health-check")
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
	out, err := d.tailLogs(context.WithoutCancel(ctx), name, 50, "")
	if err != nil || strings.TrimSpace(out) == "" {
		fmt.Fprintln(log, "coldship: the container produced no output")
		return
	}
	fmt.Fprintln(log, "coldship: last output of the new container:")
	fmt.Fprint(log, out)
	if !strings.HasSuffix(out, "\n") {
		fmt.Fprintln(log)
	}
}

// removeContainer removes a container of app, refusing one that belongs to
// something else (another app, a database, or nothing Coldship made).
func (d *Docker) removeContainer(ctx context.Context, name, app string) error {
	if err := d.checkOwner(ctx, name, app, ""); err != nil {
		return err
	}
	if err := d.ForceRemove(ctx, name); err != nil {
		return apierr.New(502, "container_remove_failed", "could not remove container %s: %v", name, err)
	}
	return nil
}

// checkOwner returns an error if container name exists and isn't app's (or,
// with db set, app's database db).
func (d *Docker) checkOwner(ctx context.Context, name, app, db string) error {
	info, err := d.InspectContainer(ctx, name)
	if err != nil {
		return err
	}
	if !info.Exists {
		return nil
	}
	if got := info.Labels["coldship.app"] + "|" + info.Labels["coldship.db"]; got != app+"|"+db {
		return apierr.New(409, "name_in_use", "container %s doesn't belong to %s (labels %q); Coldship won't touch it", name, app, got).
			WithHint("rename or remove that container yourself, or rename the app")
	}
	return nil
}

func (d *Docker) Remove(ctx context.Context, app string, volumes []string) error {
	for _, n := range []string{nextName(app), ContainerName(app)} {
		if err := d.removeContainer(ctx, n, app); err != nil {
			return err
		}
	}
	return d.RemoveVolumes(ctx, app, volumes)
}

func (d *Docker) RemoveVolumes(ctx context.Context, app string, volumes []string) error {
	for _, v := range volumes {
		if err := d.removeVolume(ctx, VolumeName(app, v)); err != nil {
			return apierr.New(502, "volume_remove_failed", "could not remove volume %s: %v", v, err)
		}
	}
	return nil
}

func (d *Docker) Status(ctx context.Context, app string) (Status, error) {
	return d.inspect(ctx, ContainerName(app))
}

func (d *Docker) inspect(ctx context.Context, name string) (Status, error) {
	info, err := d.InspectContainer(ctx, name)
	return info.Status, err
}

func (d *Docker) Logs(ctx context.Context, app string, tail int, since string) ([]string, error) {
	out, err := d.tailLogs(ctx, ContainerName(app), tail, since)
	if isNotFound(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, apierr.New(502, "logs_unavailable", "docker logs failed: %v", err)
	}
	return splitLines(out), nil
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return []string{}
	}
	return strings.Split(s, "\n")
}
