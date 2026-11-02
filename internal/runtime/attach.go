package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// AgentLabel marks containers running AI agents. They are never apps:
// Coldship removes leftovers at start.
const AgentLabel = "coldship.agent"

// AgentVolumePrefix names agents' home volumes.
const AgentVolumePrefix = "coldship-agent-"

// CopyIn is a host file copied into a container before it starts.
type CopyIn struct {
	From string // host path
	Dir  string // directory in the container
	Name string
	Mode int64
}

// AttachSpec is a container whose stdin and stdout a program talks to.
type AttachSpec struct {
	Name       string
	Image      string
	Cmd        []string
	Env        []string // passed in the API request, never on a command line
	Labels     map[string]string
	Binds      []string
	WorkingDir string
	Memory     int64
	Files      []CopyIn
}

// Attached is a running attached container.
type Attached struct {
	Stdin  io.WriteCloser
	Stdout io.Reader
	Stderr io.Reader
	// Wait blocks until the container exits.
	Wait func() error
	// Kill stops and removes the container.
	Kill func()
}

type hijackStdin struct{ h *client.HijackedResponse }

func (s hijackStdin) Write(b []byte) (int, error) { return s.h.Conn.Write(b) }
func (s hijackStdin) Close() error                { return s.h.CloseWrite() }

// RunAttached creates a container on the default network (not Coldship's,
// so it can't reach apps or databases), copies files in, attaches to its
// stdio and starts it.
func (d *Docker) RunAttached(ctx context.Context, s AttachSpec, log io.Writer) (*Attached, error) {
	cli, err := d.api()
	if err != nil {
		return nil, err
	}
	if err := d.EnsureImage(ctx, s.Image, log); err != nil {
		return nil, err
	}
	cfg := &container.Config{Image: s.Image, Cmd: s.Cmd, Env: s.Env, Labels: s.Labels, WorkingDir: s.WorkingDir,
		OpenStdin: true, StdinOnce: true, AttachStdin: true, AttachStdout: true, AttachStderr: true}
	host := &container.HostConfig{Binds: s.Binds, Resources: container.Resources{Memory: s.Memory},
		LogConfig: container.LogConfig{Type: "none"}}
	res, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: s.Name, Config: cfg, HostConfig: host})
	if err != nil {
		return nil, unavailable("creating the agent container failed", err)
	}
	id := res.ID
	remove := func() {
		cli.ContainerRemove(context.Background(), id, client.ContainerRemoveOptions{Force: true})
	}
	for _, f := range s.Files {
		src, err := os.Open(f.From)
		if err != nil {
			remove()
			return nil, err
		}
		err = d.copyFileMode(ctx, id, f.Dir, f.Name, f.Mode, src)
		src.Close()
		if err != nil {
			remove()
			return nil, fmt.Errorf("copying %s into the agent container: %w", f.Name, err)
		}
	}
	att, err := cli.ContainerAttach(ctx, id, client.ContainerAttachOptions{Stream: true, Stdin: true, Stdout: true, Stderr: true})
	if err != nil {
		remove()
		return nil, unavailable("attaching to the agent container failed", err)
	}
	h := &att.HijackedResponse
	if _, err := cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		h.Close()
		remove()
		return nil, unavailable("starting the agent container failed", err)
	}
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	go func() {
		var err error
		if mt, ok := h.MediaType(); ok && mt == "application/vnd.docker.raw-stream" {
			_, err = io.Copy(outW, h.Reader)
		} else {
			_, err = stdcopy.StdCopy(outW, errW, h.Reader)
		}
		outW.CloseWithError(err)
		errW.CloseWithError(err)
	}()
	var once sync.Once
	kill := func() {
		once.Do(func() {
			h.Close()
			remove()
		})
	}
	wait := func() error {
		w := cli.ContainerWait(context.Background(), id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
		select {
		case r := <-w.Result:
			if r.StatusCode != 0 {
				return fmt.Errorf("the agent exited with status %d", r.StatusCode)
			}
			return nil
		case err := <-w.Error:
			return err
		}
	}
	return &Attached{Stdin: hijackStdin{h}, Stdout: outR, Stderr: errR, Wait: wait, Kill: kill}, nil
}

// RemoveLabeled force-removes every container with label (e.g. agents left
// from before a restart).
func (d *Docker) RemoveLabeled(ctx context.Context, label string) error {
	cli, err := d.api()
	if err != nil {
		return err
	}
	list, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", label)})
	if err != nil {
		return unavailable("listing containers failed", err)
	}
	for _, c := range list.Items {
		cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true})
	}
	return nil
}
