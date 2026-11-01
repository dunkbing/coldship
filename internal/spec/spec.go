// Package spec defines the declarative app spec (hangar.yaml), the single
// source of truth an agent edits before running plan/apply.
package spec

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"hangar/internal/apierr"
)

type Stage string

const (
	StageProduction Stage = "production"
	StageStaging    Stage = "staging"
	StagePreview    Stage = "preview"
)

type Volume struct {
	Name string `json:"name" yaml:"name" jsonschema:"volume name, lowercase letters, digits and dashes"`
	Path string `json:"path" yaml:"path" jsonschema:"absolute mount path inside the container"`
}

type App struct {
	Name     string            `json:"name" yaml:"name" jsonschema:"app name: lowercase letters, digits and dashes, starting with a letter"`
	Stage    Stage             `json:"stage,omitempty" yaml:"stage,omitempty" jsonschema:"production, staging (default) or preview. Destructive changes to production need human approval"`
	Image    string            `json:"image" yaml:"image" jsonschema:"container image, e.g. nginx:1.27"`
	Port     int               `json:"port,omitempty" yaml:"port,omitempty" jsonschema:"port the app listens on inside the container"`
	HostPort int               `json:"host_port,omitempty" yaml:"host_port,omitempty" jsonschema:"host port to publish the container port on (requires port)"`
	Memory   string            `json:"memory,omitempty" yaml:"memory,omitempty" jsonschema:"memory limit such as 256m or 1g"`
	Env      map[string]string `json:"env,omitempty" yaml:"env,omitempty" jsonschema:"environment variables; values are write-only and redacted in all output"`
	Volumes  []Volume          `json:"volumes,omitempty" yaml:"volumes,omitempty" jsonschema:"persistent volumes. Removing one from the spec deletes its data"`
}

var (
	nameRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	memoryRe = regexp.MustCompile(`^[1-9][0-9]*[kmg]$`)
	slugRe   = regexp.MustCompile(`[^a-z0-9-]+`)
)

// Load reads a spec file strictly: unknown fields are errors, so a typo
// never silently becomes a no-op.
func Load(path string) (*App, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, apierr.New(400, "spec_unreadable", "%v", err)
	}
	return Parse(b)
}

func Parse(b []byte) (*App, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var a App
	if err := dec.Decode(&a); err != nil {
		return nil, apierr.New(400, "invalid_spec", "cannot parse spec: %v", err).
			WithHint("allowed fields: name, stage, image, port, host_port, memory, env, volumes")
	}
	a.Normalize()
	return &a, a.Validate()
}

// Normalize fills defaults in place.
func (a *App) Normalize() {
	if a.Stage == "" {
		a.Stage = StageStaging
	}
}

func (a *App) Validate() error {
	if !nameRe.MatchString(a.Name) {
		e := apierr.Invalid("name", "name %q must be 1-40 chars of lowercase letters, digits and dashes, starting with a letter", a.Name)
		if s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(a.Name), "-"), "-"); nameRe.MatchString(s) {
			e.WithFix(map[string]any{"name": s})
		}
		return e
	}
	switch a.Stage {
	case StageProduction, StageStaging, StagePreview:
	default:
		return apierr.Invalid("stage", "stage %q is not one of production, staging, preview", a.Stage).
			WithFix(map[string]any{"stage": "staging"})
	}
	if strings.TrimSpace(a.Image) == "" {
		return apierr.Invalid("image", "image is required").WithFix(map[string]any{"image": "nginx:1.27"})
	}
	if a.Port < 0 || a.Port > 65535 {
		return apierr.Invalid("port", "port %d out of range 1-65535", a.Port)
	}
	if a.HostPort < 0 || a.HostPort > 65535 {
		return apierr.Invalid("host_port", "host_port %d out of range 1-65535", a.HostPort)
	}
	if a.HostPort > 0 && a.Port == 0 {
		return apierr.Invalid("port", "host_port is set but port is not; Hangar needs to know which container port to publish").
			WithFix(map[string]any{"port": a.HostPort})
	}
	if a.Memory != "" && !memoryRe.MatchString(a.Memory) {
		return apierr.Invalid("memory", "memory %q must look like 256m or 1g", a.Memory).WithFix(map[string]any{"memory": "512m"})
	}
	for k, v := range a.Env {
		if !envKeyRe.MatchString(k) {
			return apierr.Invalid("env."+k, "env key %q must match [A-Za-z_][A-Za-z0-9_]*", k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return apierr.Invalid("env."+k, "env value for %q must be a single line", k)
		}
	}
	names, paths := map[string]bool{}, map[string]bool{}
	for i, v := range a.Volumes {
		f := fmt.Sprintf("volumes[%d]", i)
		if !nameRe.MatchString(v.Name) {
			return apierr.Invalid(f+".name", "volume name %q must be lowercase letters, digits and dashes", v.Name)
		}
		if !strings.HasPrefix(v.Path, "/") {
			return apierr.Invalid(f+".path", "volume path %q must be absolute", v.Path).WithFix(map[string]any{"path": "/" + strings.TrimLeft(v.Path, "./")})
		}
		if names[v.Name] || paths[v.Path] {
			return apierr.Invalid(f, "duplicate volume name or path (%s, %s)", v.Name, v.Path)
		}
		names[v.Name], paths[v.Path] = true, true
	}
	return nil
}

const RedactedValue = "<redacted>"

// Redacted returns a copy safe to show to agents and logs: env values are
// replaced, so secrets never end up in tool output or model context.
func (a *App) Redacted() *App {
	if a == nil {
		return nil
	}
	c := *a
	if a.Env != nil {
		c.Env = make(map[string]string, len(a.Env))
		for k := range a.Env {
			c.Env[k] = RedactedValue
		}
	}
	c.Volumes = append([]Volume(nil), a.Volumes...)
	return &c
}

// Clone returns a deep copy.
func (a *App) Clone() *App {
	if a == nil {
		return nil
	}
	c := *a
	if a.Env != nil {
		c.Env = make(map[string]string, len(a.Env))
		for k, v := range a.Env {
			c.Env[k] = v
		}
	}
	c.Volumes = append([]Volume(nil), a.Volumes...)
	return &c
}
