package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"hangar/internal/apierr"
	"hangar/internal/spec"
)

// Docker drives the docker CLI. Shelling out keeps the dependency tree small
// for the starter; swap in the Docker Engine API client when needed.
type Docker struct {
	Bin          string
	StartupGrace time.Duration
}

func NewDocker() *Docker { return &Docker{Bin: "docker", StartupGrace: 3 * time.Second} }

func ContainerName(app string) string      { return "hangar-" + app }
func VolumeName(app, volume string) string { return "hangar-" + app + "-" + volume }

func (d *Docker) run(ctx context.Context, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, d.Bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), strings.TrimSpace(errb.String()), err
}

func (d *Docker) Deploy(ctx context.Context, deploymentID string, app spec.App) error {
	// Pull before touching the running container, so a bad image never
	// causes downtime.
	if _, _, err := d.run(ctx, "image", "inspect", app.Image); err != nil {
		if _, se, err := d.run(ctx, "pull", app.Image); err != nil {
			return apierr.New(422, "image_pull_failed", "could not pull image %q: %s", app.Image, se).
				WithField("image").WithHint("check the image name and tag, and that the node can reach the registry")
		}
	}

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

	name := ContainerName(app.Name)
	if _, se, err := d.run(ctx, "rm", "-f", name); err != nil && !strings.Contains(se, "No such container") {
		return apierr.New(502, "container_remove_failed", "could not remove old container: %s", se)
	}

	args := []string{"run", "-d", "--name", name,
		"--label", "hangar.app=" + app.Name, "--label", "hangar.deployment=" + deploymentID,
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
	if _, se, err := d.run(ctx, args...); err != nil {
		e := apierr.New(502, "container_start_failed", "docker could not start the container: %s", se)
		if strings.Contains(se, "port is already allocated") || strings.Contains(se, "address already in use") {
			e.Code = "host_port_in_use"
			e.Field = "host_port"
			e.Hint = fmt.Sprintf("host port %d is used by another process or app; pick a free port", app.HostPort)
		}
		return e
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d.StartupGrace):
	}
	st, err := d.Status(ctx, app.Name)
	if err != nil {
		return err
	}
	if st.State != "running" || st.RestartCount > 0 {
		return apierr.New(502, "container_exited", "container did not stay up after start (state=%s, exit_code=%d, restarts=%d, oom_killed=%t)",
			st.State, st.ExitCode, st.RestartCount, st.OOMKilled).
			WithHint("call diagnose for this app to see logs and likely causes")
	}
	return nil
}

func (d *Docker) Remove(ctx context.Context, app string, volumes []string) error {
	if _, se, err := d.run(ctx, "rm", "-f", ContainerName(app)); err != nil && !strings.Contains(se, "No such container") {
		return apierr.New(502, "container_remove_failed", "could not remove container: %s", se)
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
	out, se, err := d.run(ctx, "inspect", "--type", "container", ContainerName(app))
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
