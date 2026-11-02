package runtime

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/spec"
)

// Fake is an in-memory Runtime for tests.
type Fake struct {
	mu         sync.Mutex
	Containers map[string]spec.App
	Volumes    map[string]bool // key: app/volume
	LogLines   map[string][]string
	Statuses   map[string]Status // overrides Status for an app
	// FailNext, if set, is returned by the next Deploy (and then cleared).
	// It fails like a health check: with ZeroDowntime the old container is
	// kept, otherwise it is gone.
	FailNext error
	// FailOutput is written to the deploy log when FailNext fires, like a
	// failed container's last output.
	FailOutput []string
	Deploys    int
	LastOpts   DeployOptions
	// DBs holds fake database contents by "app/name".
	DBs          map[string]string
	FailSnapshot error
	FailDatabase error
	// Ports holds images' declared ports (EXPOSE), by image.
	Ports map[string][]int
	// Stats holds containers' counters, by container name.
	Stats map[string]RawStats
	// Job runs a job (default: exit 0); Jobs records the specs run.
	Job         func(ctx context.Context, s JobSpec, log io.Writer) (int, error)
	Jobs        []JobSpec
	RemovedJobs []string
}

func (f *Fake) RunJob(ctx context.Context, s JobSpec, log io.Writer) (int, error) {
	f.mu.Lock()
	f.Jobs = append(f.Jobs, s)
	run := f.Job
	f.mu.Unlock()
	if run == nil {
		fmt.Fprintln(log, "fake: ran", s.Command)
		return 0, nil
	}
	return run(ctx, s, log)
}

func (f *Fake) RemoveJobs(_ context.Context, app string) error {
	f.mu.Lock()
	f.RemovedJobs = append(f.RemovedJobs, app)
	f.mu.Unlock()
	return nil
}

// JobRuns returns the jobs run so far.
func (f *Fake) JobRuns() []JobSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]JobSpec(nil), f.Jobs...)
}

func NewFake() *Fake {
	return &Fake{Containers: map[string]spec.App{}, Volumes: map[string]bool{},
		LogLines: map[string][]string{}, Statuses: map[string]Status{}, DBs: map[string]string{}, Ports: map[string][]int{}, Stats: map[string]RawStats{}}
}

func (f *Fake) Deploy(ctx context.Context, depID string, app spec.App, opts DeployOptions) error {
	f.mu.Lock()
	f.Deploys++
	f.LastOpts = opts
	_, hadOld := f.Containers[app.Name]
	if err := f.FailNext; err != nil {
		f.FailNext = nil
		if opts.Log != nil {
			fmt.Fprintln(opts.Log, "fake: new container failed its health check")
			for _, l := range f.FailOutput {
				fmt.Fprintln(opts.Log, l)
			}
		}
		if opts.ZeroDowntime && hadOld {
			f.mu.Unlock()
			if ae, ok := apierr.As(err); ok {
				return ae.WithDetail(DetailPreviousRunning, true)
			}
			return err
		}
		delete(f.Containers, app.Name)
		f.mu.Unlock()
		return err
	}
	f.mu.Unlock()
	if opts.Switch != nil && app.Port > 0 {
		if err := opts.Switch(ctx, Upstream(depID, app.Port)); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Containers[app.Name] = app
	for _, v := range app.Volumes {
		f.Volumes[app.Name+"/"+v.Name] = true
	}
	return nil
}

func (f *Fake) Remove(ctx context.Context, app string, volumes []string) error {
	f.mu.Lock()
	delete(f.Containers, app)
	f.mu.Unlock()
	return f.RemoveVolumes(ctx, app, volumes)
}

func (f *Fake) RemoveVolumes(_ context.Context, app string, volumes []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range volumes {
		delete(f.Volumes, app+"/"+v)
	}
	return nil
}

func (f *Fake) Status(_ context.Context, app string) (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.Statuses[app]; ok {
		return s, nil
	}
	c, ok := f.Containers[app]
	if !ok {
		return Status{}, nil
	}
	return Status{Exists: true, State: "running", Running: true, Image: c.Image}, nil
}

func (f *Fake) Logs(_ context.Context, app string, tail int, _ string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.LogLines[app]
	if len(l) > tail {
		l = l[len(l)-tail:]
	}
	return append([]string{}, l...), nil
}

func (f *Fake) EnsureDatabase(_ context.Context, db DB, _ io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.FailDatabase; err != nil {
		f.FailDatabase = nil
		return err
	}
	if _, ok := f.DBs[db.App+"/"+db.Name]; !ok {
		f.DBs[db.App+"/"+db.Name] = ""
	}
	return nil
}

func (f *Fake) DatabaseStatus(_ context.Context, app, name string) (DBStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.DBs[app+"/"+name]; ok {
		return DBStatus{Exists: true, Running: true, State: "running"}, nil
	}
	return DBStatus{}, nil
}

func (f *Fake) RemoveDatabase(_ context.Context, app, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.DBs, app+"/"+name)
	return nil
}

func (f *Fake) SnapshotDatabase(_ context.Context, db DB, w io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.FailSnapshot; err != nil {
		f.FailSnapshot = nil
		return err
	}
	_, err := io.WriteString(w, f.DBs[db.App+"/"+db.Name])
	return err
}

func (f *Fake) RestoreDatabase(_ context.Context, db DB, r io.Reader, _ io.Writer) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.DBs[db.App+"/"+db.Name] = string(b)
	return nil
}

// ImagePorts returns Ports[image], standing in for an image's EXPOSE.
func (f *Fake) ImagePorts(_ context.Context, image string) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Ports[image], nil
}

// ContainerStats returns Stats[name], standing in for the engine's counters.
func (f *Fake) ContainerStats(_ context.Context, name string) (RawStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Stats[name], nil
}
