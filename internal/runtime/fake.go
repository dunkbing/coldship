package runtime

import (
	"context"
	"sync"

	"hangar/internal/spec"
)

// Fake is an in-memory Runtime for tests.
type Fake struct {
	mu         sync.Mutex
	Containers map[string]spec.App
	Volumes    map[string]bool // key: app/volume
	LogLines   map[string][]string
	Statuses   map[string]Status // overrides Status for an app
	// FailNext, if set, is returned by the next Deploy (and then cleared).
	FailNext error
	Deploys  int
}

func NewFake() *Fake {
	return &Fake{Containers: map[string]spec.App{}, Volumes: map[string]bool{},
		LogLines: map[string][]string{}, Statuses: map[string]Status{}}
}

func (f *Fake) Deploy(_ context.Context, _ string, app spec.App) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Deploys++
	if err := f.FailNext; err != nil {
		f.FailNext = nil
		delete(f.Containers, app.Name)
		return err
	}
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
