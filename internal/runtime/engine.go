package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/dunkbing/coldship/internal/apierr"
)

// This file is the thin layer over the Docker Engine API (the official Go
// SDK) that the runtime and the proxy share.

func isNotFound(err error) bool { return err != nil && cerrdefs.IsNotFound(err) }

// unavailable explains an error reaching the Docker daemon, with the usual
// fixes.
func unavailable(what string, err error) *apierr.Error {
	msg := err.Error()
	e := apierr.New(502, "runtime_unavailable", "%s: %s", what, msg)
	s := strings.ToLower(msg)
	switch {
	case strings.Contains(s, "permission denied"):
		e.Code = "docker_permission_denied"
		e.WithHint("this user isn't allowed to use Docker. Easiest: install Coldship as a service, which needs no Docker access for your user " +
			"(curl -fsSL https://raw.githubusercontent.com/dunkbing/coldship/main/install.sh | sh). Or add this user to the docker group " +
			"(`sudo usermod -aG docker $USER`); that takes effect only in a new login session, so log out and back in (or run `newgrp docker`) before starting coldship server again")
	case client.IsErrConnectionFailed(err) || strings.Contains(s, "no such file") || strings.Contains(s, "connection refused") ||
		strings.Contains(s, "is the docker daemon running"):
		e.Code = "docker_not_running"
		e.WithHint("no container engine is running. Install Docker (https://docs.docker.com/engine/install/) and start it " +
			"(`sudo systemctl enable --now docker`), or with Podman start its API socket (`sudo systemctl enable --now podman.socket`); then start coldship server again")
	}
	return e
}

func (d *Docker) api() (*client.Client, error) {
	if d.initErr != nil {
		return nil, unavailable("can't set up the Docker client", d.initErr)
	}
	return d.cli, nil
}

// ContainerSpec describes a container to create and start.
type ContainerSpec struct {
	Name       string
	Image      string
	Cmd        []string
	Entrypoint []string
	// Env is passed in the API request, never on a command line.
	Env     []string
	Labels  map[string]string
	Aliases []string // hostnames on the Coldship network
	// Ports maps host ports to container ports.
	Ports  map[int]int
	Binds  []string // "volume:/path" or "/host/path:/path:ro"
	Memory int64    // bytes; 0 for no limit
}

// RunContainer creates and starts a container on the Coldship network, with
// restart policy unless-stopped. It returns Docker's error text on failure.
func (d *Docker) RunContainer(ctx context.Context, s ContainerSpec) (string, error) {
	cli, err := d.api()
	if err != nil {
		return err.Error(), err
	}
	cfg := &container.Config{Image: s.Image, Cmd: s.Cmd, Entrypoint: s.Entrypoint, Env: s.Env, Labels: s.Labels}
	// Podman's boot-time restart (podman-restart.service) only starts
	// containers whose policy is "always".
	restart := container.RestartPolicyUnlessStopped
	if d.IsPodman(ctx) {
		restart = container.RestartPolicyAlways
	}
	host := &container.HostConfig{
		Binds:         s.Binds,
		RestartPolicy: container.RestartPolicy{Name: restart},
		Resources:     container.Resources{Memory: s.Memory},
		// Rotate logs so a chatty app can't fill the disk (at most 3×10 MB).
		LogConfig: container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
	}
	if len(s.Ports) > 0 {
		cfg.ExposedPorts = network.PortSet{}
		host.PortBindings = network.PortMap{}
		for hostPort, ctrPort := range s.Ports {
			p, ok := network.PortFrom(uint16(ctrPort), network.TCP)
			if !ok {
				return "invalid port", fmt.Errorf("invalid port %d", ctrPort)
			}
			cfg.ExposedPorts[p] = struct{}{}
			host.PortBindings[p] = append(host.PortBindings[p], network.PortBinding{HostPort: strconv.Itoa(hostPort)})
		}
	}
	netCfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
		Network: {Aliases: s.Aliases},
	}}
	res, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: s.Name, Config: cfg, HostConfig: host, NetworkingConfig: netCfg})
	if err != nil {
		return err.Error(), err
	}
	if _, err := cli.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
		return err.Error(), err
	}
	return "", nil
}

// ContainerInfo is what Coldship reads back about a container.
type ContainerInfo struct {
	Status
	Labels map[string]string
	IP     string // on the Coldship network
}

// InspectContainer returns the container's state; Exists is false if there
// is no such container.
func (d *Docker) InspectContainer(ctx context.Context, name string) (ContainerInfo, error) {
	cli, err := d.api()
	if err != nil {
		return ContainerInfo{}, err
	}
	res, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if isNotFound(err) {
		return ContainerInfo{}, nil
	}
	if err != nil {
		return ContainerInfo{}, unavailable("docker inspect failed", err)
	}
	c := res.Container
	info := ContainerInfo{Status: Status{Exists: true, RestartCount: c.RestartCount}}
	if st := c.State; st != nil {
		info.State, info.Running = string(st.Status), st.Running && !st.Restarting
		info.ExitCode, info.OOMKilled, info.StartedAt, info.Error = st.ExitCode, st.OOMKilled, st.StartedAt, st.Error
	}
	if c.Config != nil {
		info.Image, info.Labels = c.Config.Image, c.Config.Labels
	}
	if ns := c.NetworkSettings; ns != nil {
		if ep := ns.Networks[Network]; ep != nil && ep.IPAddress.IsValid() {
			info.IP = ep.IPAddress.String()
		}
	}
	return info, nil
}

// ForceRemove removes a container, running or not. A missing one is fine.
func (d *Docker) ForceRemove(ctx context.Context, name string) error {
	cli, err := d.api()
	if err != nil {
		return err
	}
	if _, err := cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true}); err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

func (d *Docker) stopContainer(ctx context.Context, name string, timeout time.Duration) error {
	cli, err := d.api()
	if err != nil {
		return err
	}
	secs := int(timeout.Seconds())
	if _, err := cli.ContainerStop(ctx, name, client.ContainerStopOptions{Timeout: &secs}); err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

func (d *Docker) startContainer(ctx context.Context, name string) error {
	cli, err := d.api()
	if err != nil {
		return err
	}
	_, err = cli.ContainerStart(ctx, name, client.ContainerStartOptions{})
	return err
}

func (d *Docker) removeVolume(ctx context.Context, name string) error {
	cli, err := d.api()
	if err != nil {
		return err
	}
	if _, err := cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{}); err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

// EnsureImage pulls image unless it is already present.
func (d *Docker) EnsureImage(ctx context.Context, image string, log io.Writer) error {
	cli, err := d.api()
	if err != nil {
		return err
	}
	if _, err := cli.ImageInspect(ctx, image); err == nil {
		return nil
	} else if !isNotFound(err) {
		return unavailable("docker image inspect failed", err)
	}
	if log != nil {
		fmt.Fprintf(log, "coldship: pulling %s\n", image)
	}
	resp, err := cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return err
	}
	defer resp.Close()
	return resp.Wait(ctx)
}

// ExecIO wires an exec's streams. Nil writers discard.
type ExecIO struct {
	Env    []string // passed in the API request, never on a command line
	Stdin  io.Reader
	Stdout io.Writer
}

// Exec runs cmd in a running container and returns its exit code and
// stderr. err is set only when the exec couldn't run at all.
func (d *Docker) Exec(ctx context.Context, name string, cmd []string, o ExecIO) (int, string, error) {
	cli, err := d.api()
	if err != nil {
		return -1, "", err
	}
	cr, err := cli.ExecCreate(ctx, name, client.ExecCreateOptions{Cmd: cmd, Env: o.Env,
		AttachStdin: o.Stdin != nil, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return -1, err.Error(), err
	}
	att, err := cli.ExecAttach(ctx, cr.ID, client.ExecAttachOptions{})
	if err != nil {
		return -1, err.Error(), err
	}
	defer att.Close()
	if o.Stdin != nil {
		go func() {
			io.Copy(att.Conn, o.Stdin)
			att.CloseWrite()
		}()
	}
	stdout := o.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	var stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(stdout, &stderr, att.Reader); err != nil {
		return -1, stderr.String(), err
	}
	// The stream ends when the process does; its exit code may take a moment
	// to be recorded.
	for i := 0; i < 50; i++ {
		ins, err := cli.ExecInspect(ctx, cr.ID, client.ExecInspectOptions{})
		if err != nil {
			return -1, stderr.String(), err
		}
		if !ins.Running {
			return ins.ExitCode, stderr.String(), nil
		}
		select {
		case <-ctx.Done():
			return -1, stderr.String(), ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return -1, stderr.String(), fmt.Errorf("exec in %s didn't report an exit code", name)
}

// tailLogs returns the container's last n lines of output (stdout and
// stderr together), optionally only those newer than since.
func (d *Docker) tailLogs(ctx context.Context, name string, n int, since string) (string, error) {
	cli, err := d.api()
	if err != nil {
		return "", err
	}
	rc, err := cli.ContainerLogs(ctx, name, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true,
		Tail: strconv.Itoa(n), Since: since})
	if err != nil {
		return "", err
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := stdcopy.StdCopy(&buf, &buf, rc); err != nil {
		return buf.String(), err
	}
	return buf.String(), nil
}

// parseMemory turns a spec memory limit (256m, 1g, 512k) into bytes.
func parseMemory(s string) int64 {
	if len(s) < 2 {
		return 0
	}
	n, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
	if err != nil {
		return 0
	}
	switch s[len(s)-1] {
	case 'k':
		return n << 10
	case 'm':
		return n << 20
	case 'g':
		return n << 30
	}
	return 0
}

// ImagePorts lists the TCP ports an image declares (EXPOSE), lowest first,
// pulling the image if it isn't here yet.
func (d *Docker) ImagePorts(ctx context.Context, image string) ([]int, error) {
	if err := d.EnsureImage(ctx, image, nil); err != nil {
		return nil, err
	}
	cli, err := d.api()
	if err != nil {
		return nil, err
	}
	res, err := cli.ImageInspect(ctx, image)
	if err != nil {
		return nil, err
	}
	var ports []int
	if res.Config != nil {
		for p := range res.Config.ExposedPorts {
			num, proto, _ := strings.Cut(p, "/")
			if proto != "" && proto != "tcp" {
				continue
			}
			if n, err := strconv.Atoi(num); err == nil && n > 0 {
				ports = append(ports, n)
			}
		}
	}
	sort.Ints(ports)
	return ports, nil
}

// RawStats are a container's cumulative counters at one moment.
type RawStats struct {
	At                time.Time
	CPUNanos, SysNano uint64 // container and whole-host CPU time
	CPUs              uint32
	Mem, MemLimit     uint64
	NetRx, NetTx      uint64
	DiskRead, DiskOut uint64
}

// ContainerStats reads a container's counters once.
func (d *Docker) ContainerStats(ctx context.Context, name string) (RawStats, error) {
	cli, err := d.api()
	if err != nil {
		return RawStats{}, err
	}
	res, err := cli.ContainerStats(ctx, name, client.ContainerStatsOptions{})
	if err != nil {
		return RawStats{}, err
	}
	defer res.Body.Close()
	var s container.StatsResponse
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
		return RawStats{}, err
	}
	r := RawStats{At: s.Read, CPUNanos: s.CPUStats.CPUUsage.TotalUsage, SysNano: s.CPUStats.SystemUsage,
		CPUs: s.CPUStats.OnlineCPUs, MemLimit: s.MemoryStats.Limit}
	if r.At.IsZero() {
		r.At = time.Now()
	}
	// Like docker stats: page cache the kernel can drop doesn't count.
	cache := s.MemoryStats.Stats["inactive_file"]
	if v, ok := s.MemoryStats.Stats["total_inactive_file"]; ok {
		cache = v
	}
	if s.MemoryStats.Usage > cache {
		r.Mem = s.MemoryStats.Usage - cache
	}
	for _, n := range s.Networks {
		r.NetRx += n.RxBytes
		r.NetTx += n.TxBytes
	}
	for _, e := range s.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			r.DiskRead += e.Value
		case "write":
			r.DiskOut += e.Value
		}
	}
	return r, nil
}
