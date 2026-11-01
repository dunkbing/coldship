// Package proxy routes domains to app containers with automatic HTTPS.
//
// Coldship runs Caddy as a managed container ("coldship-proxy") on the Coldship
// Docker network. Coldship renders Caddy's whole JSON config from its route
// table, writes it to a directory mounted into the container, and asks Caddy
// to reload it gracefully (in-flight requests finish). Caddy's admin API stays
// inside the container.
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/runtime"
)

// Route sends every listed domain to one upstream ("host:port").
type Route struct {
	App      string
	Domains  []string
	Upstream string
}

type Proxy interface {
	// Ensure starts the proxy container (and network) if needed.
	Ensure(ctx context.Context) error
	// Apply replaces the whole route table.
	Apply(ctx context.Context, routes []Route) error
	Status(ctx context.Context) Status
}

type Status struct {
	Enabled bool   `json:"enabled"`
	Running bool   `json:"running"`
	HTTP    int    `json:"http_port,omitempty"`
	HTTPS   int    `json:"https_port,omitempty"`
	Error   string `json:"error,omitempty"`
}

const containerName = "coldship-proxy"

type Caddy struct {
	Docker *runtime.Docker
	Image  string
	// ConfigDir is a host directory mounted into the container at
	// /etc/coldship; Coldship writes caddy.json there.
	ConfigDir string
	HTTPPort  int
	HTTPSPort int
	// Email is given to the ACME CA for certificate expiry notices. Optional.
	Email string

	mu sync.Mutex
}

func NewCaddy(configDir string, httpPort, httpsPort int, email string) *Caddy {
	return &Caddy{Docker: runtime.NewDocker(), Image: "caddy:2-alpine", ConfigDir: configDir,
		HTTPPort: httpPort, HTTPSPort: httpsPort, Email: email}
}

func (c *Caddy) portsLabel() string { return fmt.Sprintf("%d,%d", c.HTTPPort, c.HTTPSPort) }

func (c *Caddy) Ensure(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.Docker.EnsureNetwork(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(c.ConfigDir, 0o755); err != nil {
		return err
	}
	cfgPath := filepath.Join(c.ConfigDir, "caddy.json")
	if _, err := os.Stat(cfgPath); err != nil {
		if err := c.write(nil); err != nil {
			return err
		}
	}

	info, err := c.Docker.InspectContainer(ctx, containerName)
	if err != nil {
		return err
	}
	if info.Exists {
		if info.Running && info.Labels["coldship.proxy.ports"] == c.portsLabel() {
			return nil
		}
		// Wrong ports or stopped: recreate. Certificates live in a volume.
		if err := c.Docker.ForceRemove(ctx, containerName); err != nil {
			return apierr.New(502, "proxy_unavailable", "could not replace the proxy container: %v", err)
		}
	}
	abs, err := filepath.Abs(c.ConfigDir)
	if err != nil {
		return err
	}
	if err := c.Docker.EnsureImage(ctx, c.Image, nil); err != nil {
		return apierr.New(502, "proxy_unavailable", "could not pull %s: %v", c.Image, err)
	}
	se, err := c.Docker.RunContainer(ctx, runtime.ContainerSpec{Name: containerName, Image: c.Image,
		Cmd:    []string{"caddy", "run", "--config", "/etc/coldship/caddy.json"},
		Labels: map[string]string{"coldship.proxy.ports": c.portsLabel()},
		Ports:  map[int]int{c.HTTPPort: 80, c.HTTPSPort: 443},
		Binds:  []string{"coldship-proxy-data:/data", "coldship-proxy-config:/config", abs + ":/etc/coldship:ro"}})
	if err != nil {
		e := apierr.New(502, "proxy_unavailable", "could not start the proxy container: %s", se)
		if strings.Contains(se, "port is already allocated") || strings.Contains(se, "address already in use") {
			e.Hint = fmt.Sprintf("ports %d/%d are taken by another web server; stop it, or start coldship with --proxy-http-port/--proxy-https-port", c.HTTPPort, c.HTTPSPort)
		}
		return e
	}
	return nil
}

func (c *Caddy) Apply(ctx context.Context, routes []Route) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.write(routes); err != nil {
		return err
	}
	code, se, err := c.Docker.Exec(ctx, containerName, []string{"caddy", "reload", "--config", "/etc/coldship/caddy.json", "--force"}, runtime.ExecIO{})
	if err != nil || code != 0 {
		if se == "" && err != nil {
			se = err.Error()
		}
		return apierr.New(502, "proxy_reload_failed", "the proxy rejected its new configuration: %s", lastLine(se)).
			WithHint("check that the coldship-proxy container is running (coldship server starts it)")
	}
	return nil
}

func (c *Caddy) Status(ctx context.Context) Status {
	st := Status{Enabled: true, HTTP: c.HTTPPort, HTTPS: c.HTTPSPort}
	info, err := c.Docker.InspectContainer(ctx, containerName)
	switch {
	case err != nil:
		st.Error = err.Error()
	case !info.Exists:
		st.Error = "no such container: " + containerName
	default:
		st.Running = info.Running
	}
	return st
}

// write renders the config and replaces caddy.json atomically.
func (c *Caddy) write(routes []Route) error {
	b, err := json.MarshalIndent(Config(routes, c.Email), "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.ConfigDir, ".caddy-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	os.Chmod(tmp.Name(), 0o644)
	return os.Rename(tmp.Name(), filepath.Join(c.ConfigDir, "caddy.json"))
}

// Config renders Caddy's JSON config. Routes are sorted so the output is
// stable. Unknown hosts get a plain 404.
func Config(routes []Route, email string) map[string]any {
	sorted := append([]Route(nil), routes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].App < sorted[j].App })
	var rs []any
	for _, r := range sorted {
		if len(r.Domains) == 0 || r.Upstream == "" {
			continue
		}
		rs = append(rs, map[string]any{
			"@id":   "app-" + r.App,
			"match": []any{map[string]any{"host": r.Domains}},
			"handle": []any{map[string]any{
				"handler":   "reverse_proxy",
				"upstreams": []any{map[string]any{"dial": r.Upstream}},
			}},
			"terminal": true,
		})
	}
	rs = append(rs, map[string]any{
		"handle": []any{map[string]any{"handler": "static_response", "status_code": 404, "body": "No app is configured for this host.\n"}},
	})
	cfg := map[string]any{
		"admin": map[string]any{"listen": "localhost:2019"},
		"apps": map[string]any{
			"http": map[string]any{"servers": map[string]any{
				"coldship": map[string]any{"listen": []string{":80", ":443"}, "routes": rs},
			}},
		},
	}
	if email != "" {
		cfg["apps"].(map[string]any)["tls"] = map[string]any{"automation": map[string]any{"policies": []any{
			map[string]any{"issuers": []any{map[string]any{"module": "acme", "email": email}}},
		}}}
	}
	return cfg
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Fake records route tables for tests.
type Fake struct {
	mu       sync.Mutex
	Routes   []Route
	Applies  int
	FailNext error
}

func (f *Fake) Ensure(context.Context) error { return nil }

func (f *Fake) Apply(_ context.Context, routes []Route) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.FailNext; err != nil {
		f.FailNext = nil
		return err
	}
	f.Applies++
	f.Routes = append([]Route(nil), routes...)
	return nil
}

func (f *Fake) Status(context.Context) Status { return Status{Enabled: true, Running: true} }

// Upstream returns the upstream currently routed for app, or "".
func (f *Fake) Upstream(app string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.Routes {
		if r.App == app {
			return r.Upstream
		}
	}
	return ""
}
