// Package runtime runs app containers on a node. Docker drives the local
// Docker daemon via its CLI; Fake is an in-memory runtime for tests.
package runtime

import (
	"context"

	"hangar/internal/spec"
)

type Status struct {
	Exists       bool   `json:"exists"`
	State        string `json:"state,omitempty"` // running, exited, restarting, ...
	Running      bool   `json:"running"`
	ExitCode     int    `json:"exit_code"`
	OOMKilled    bool   `json:"oom_killed"`
	RestartCount int    `json:"restart_count"`
	StartedAt    string `json:"started_at,omitempty"`
	Image        string `json:"image,omitempty"`
	Error        string `json:"error,omitempty"`
}

type Runtime interface {
	// Deploy replaces the app's container with one built from the spec and
	// returns an error if it does not stay up through the startup grace period.
	Deploy(ctx context.Context, deploymentID string, app spec.App) error
	// Remove deletes the app's container and the named volumes.
	Remove(ctx context.Context, app string, volumes []string) error
	RemoveVolumes(ctx context.Context, app string, volumes []string) error
	Status(ctx context.Context, app string) (Status, error)
	Logs(ctx context.Context, app string, tail int, since string) ([]string, error)
}
