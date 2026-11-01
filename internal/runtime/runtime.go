// Package runtime runs app containers on a node. Docker drives the local
// Docker daemon through the Engine API (the official Go SDK); Fake is an
// in-memory runtime for tests.
package runtime

import (
	"context"
	"io"
	"strconv"
	"strings"

	"github.com/dunkbing/coldship/internal/spec"
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

type DeployOptions struct {
	// ZeroDowntime starts the new container next to the old one, and stops
	// the old one only after the new one is healthy and traffic has been
	// switched. If anything fails before that, the old one keeps running.
	ZeroDowntime bool
	// Switch routes traffic to the new container at upstream ("host:port" on
	// the Coldship network). It runs after the health check passes. May be nil.
	Switch func(ctx context.Context, upstream string) error
	// Log receives progress and, on failure, the new container's output.
	Log io.Writer
}

// DetailPreviousRunning is set on Deploy errors when the previous container
// was left running and serving.
const DetailPreviousRunning = "previous_version_running"

type Runtime interface {
	// Deploy starts a container for the spec and returns once it is healthy
	// and receiving traffic, or with an error.
	Deploy(ctx context.Context, deploymentID string, app spec.App, opts DeployOptions) error
	// Remove deletes the app's containers and the named volumes.
	Remove(ctx context.Context, app string, volumes []string) error
	RemoveVolumes(ctx context.Context, app string, volumes []string) error
	Status(ctx context.Context, app string) (Status, error)
	Logs(ctx context.Context, app string, tail int, since string) ([]string, error)

	// EnsureDatabase creates or starts a managed database and waits until
	// it accepts connections.
	EnsureDatabase(ctx context.Context, db DB, log io.Writer) error
	DatabaseStatus(ctx context.Context, app, name string) (DBStatus, error)
	// RemoveDatabase deletes the database container and its data volume.
	RemoveDatabase(ctx context.Context, app, name string) error
	SnapshotDatabase(ctx context.Context, db DB, w io.Writer) error
	RestoreDatabase(ctx context.Context, db DB, r io.Reader, log io.Writer) error
}

// Network is the Docker network shared by apps and the proxy. Apps can reach
// each other by app name on it.
const Network = "coldship"

// Alias is a deployment's stable hostname on the Coldship network. It survives
// the container being renamed, so the proxy can keep pointing at it.
func Alias(deploymentID string) string { return strings.ReplaceAll(deploymentID, "_", "-") }

// Upstream is the address the proxy dials for a deployment.
func Upstream(deploymentID string, port int) string {
	return Alias(deploymentID) + ":" + strconv.Itoa(port)
}
