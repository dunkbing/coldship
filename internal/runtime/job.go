package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Jobs run in one-off containers: no restart policy, no network aliases
// (so they never receive the app's traffic) and their own label.
const (
	JobLabel    = "coldship.job"
	JobAppLabel = "coldship.job.app"
)

// ErrJobTimeout is returned when a job ran past its timeout and was killed.
var ErrJobTimeout = errors.New("the job ran past its timeout and was stopped")

// JobSpec is one run of an app's job.
type JobSpec struct {
	App, Job, RunID string
	Image           string
	Command         string
	Env             []string
	Binds           []string
	Memory          string // like 512m
	Timeout         time.Duration
}

// JobContainer names a run's container. App names can't hold "_", so it
// never collides with an app's container.
func JobContainer(runID string) string { return "coldship-" + runID }

// RunJob runs the job, copying its output to log, and returns its exit code.
func (d *Docker) RunJob(ctx context.Context, s JobSpec, log io.Writer) (int, error) {
	cli, err := d.api()
	if err != nil {
		return -1, err
	}
	if err := d.EnsureImage(ctx, s.Image, log); err != nil {
		return -1, err
	}
	name := JobContainer(s.RunID)
	if se, err := d.RunContainer(ctx, ContainerSpec{Name: name, Image: s.Image, Entrypoint: []string{"/bin/sh", "-c"}, Cmd: []string{s.Command},
		Env: s.Env, Binds: s.Binds, Memory: parseMemory(s.Memory), NoRestart: true,
		Labels: map[string]string{JobLabel: s.App + "/" + s.Job, JobAppLabel: s.App}}); err != nil {
		d.ForceRemove(context.Background(), name)
		return -1, fmt.Errorf("starting the job's container failed: %s", se)
	}
	defer d.ForceRemove(context.Background(), name)

	copied := make(chan struct{})
	go func() {
		defer close(copied)
		rc, err := cli.ContainerLogs(context.Background(), name, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: true})
		if err != nil {
			return
		}
		defer rc.Close()
		stdcopy.StdCopy(log, log, rc)
	}()
	tctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	w := cli.ContainerWait(tctx, name, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case r := <-w.Result:
		select {
		case <-copied:
		case <-time.After(5 * time.Second):
		}
		return int(r.StatusCode), nil
	case err := <-w.Error:
		if tctx.Err() != nil {
			d.ForceRemove(context.Background(), name)
			if ctx.Err() != nil {
				return -1, ctx.Err()
			}
			return -1, ErrJobTimeout
		}
		return -1, err
	}
}

// RemoveJobs stops and removes an app's running jobs (or, with app "",
// every job: leftovers from before a restart).
func (d *Docker) RemoveJobs(ctx context.Context, app string) error {
	if app == "" {
		return d.RemoveLabeled(ctx, JobLabel)
	}
	return d.RemoveLabeled(ctx, JobAppLabel+"="+app)
}
