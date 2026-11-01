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

// Build describes building the app's image from a Git repository with a
// Dockerfile. Exactly one of App.Image and App.Build is set.
type Build struct {
	Git        string            `json:"git" yaml:"git" jsonschema:"repository URL: https://, http:// or ssh (git@host:org/repo.git). No embedded passwords"`
	Ref        string            `json:"ref,omitempty" yaml:"ref,omitempty" jsonschema:"branch, tag or full commit SHA to build (default: the repository's default branch)"`
	Commit     string            `json:"commit,omitempty" yaml:"commit,omitempty" jsonschema:"optional full commit SHA pin. When empty, Hangar resolves ref at plan time and records the commit in the plan"`
	Context    string            `json:"context,omitempty" yaml:"context,omitempty" jsonschema:"build context directory inside the repository (default .)"`
	Dockerfile string            `json:"dockerfile,omitempty" yaml:"dockerfile,omitempty" jsonschema:"Dockerfile path relative to the context (default Dockerfile)"`
	Args       map[string]string `json:"args,omitempty" yaml:"args,omitempty" jsonschema:"docker build args. Visible in plans and image history: never put secrets here"`
}

type App struct {
	Name     string            `json:"name" yaml:"name" jsonschema:"app name: lowercase letters, digits and dashes, starting with a letter"`
	Stage    Stage             `json:"stage,omitempty" yaml:"stage,omitempty" jsonschema:"production, staging (default) or preview. Destructive changes to production need human approval"`
	Image    string            `json:"image,omitempty" yaml:"image,omitempty" jsonschema:"container image to run, e.g. nginx:1.27. Set either image or build"`
	Build    *Build            `json:"build,omitempty" yaml:"build,omitempty" jsonschema:"build the image from a Git repository instead of using image"`
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
	shaRe    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	refRe    = regexp.MustCompile(`^[A-Za-z0-9._/+-]{1,200}$`)
	// https://host/..., http://host/..., ssh://[user@]host/..., or scp-like user@host:path.
	gitURLRe = regexp.MustCompile(`^(?:(?:https?|ssh)://[A-Za-z0-9._~%!$&'()*+,;=:@-]+(?:/\S*)?|[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:\S+)$`)
)

// IsCommitSHA reports whether s is a full 40-character commit SHA.
func IsCommitSHA(s string) bool { return shaRe.MatchString(s) }

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
			WithHint("allowed fields: name, stage, image, build (git, ref, commit, context, dockerfile, args), port, host_port, memory, env, volumes")
	}
	a.Normalize()
	return &a, a.Validate()
}

// Normalize fills defaults in place.
func (a *App) Normalize() {
	if a.Stage == "" {
		a.Stage = StageStaging
	}
	if b := a.Build; b != nil {
		if b.Context == "" {
			b.Context = "."
		}
		if b.Dockerfile == "" {
			b.Dockerfile = "Dockerfile"
		}
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
	switch {
	case a.Image != "" && a.Build != nil:
		return apierr.Invalid("image", "set either image or build, not both")
	case a.Build != nil:
		if err := a.Build.validate(); err != nil {
			return err
		}
	case strings.TrimSpace(a.Image) == "":
		return apierr.Invalid("image", "set image (a container image to run) or build (a Git repository to build)").
			WithFix(map[string]any{"image": "nginx:1.27"})
	case strings.HasPrefix(a.Image, "-") || strings.ContainsAny(a.Image, " \t\n"):
		return apierr.Invalid("image", "image %q is not a valid image reference", a.Image)
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

func (b *Build) validate() error {
	u := strings.TrimSpace(b.Git)
	switch {
	case u == "":
		return apierr.Invalid("build.git", "build.git is required: the repository URL to build from")
	case u != b.Git || !gitURLRe.MatchString(u):
		return apierr.Invalid("build.git", "build.git %q must be an https://, http:// or ssh URL (or user@host:path)", b.Git).
			WithHint("local paths, file:// and other git transports are not allowed")
	case hasPassword(u):
		return apierr.Invalid("build.git", "build.git must not contain a password or token").
			WithHint("remove the credentials from the URL and give the server a git credentials file with `hangar server --git-credentials`")
	}
	if b.Ref != "" && (!refRe.MatchString(b.Ref) || strings.HasPrefix(b.Ref, "-") || strings.Contains(b.Ref, "..")) {
		return apierr.Invalid("build.ref", "build.ref %q is not a valid branch, tag or commit", b.Ref)
	}
	if b.Commit != "" && !IsCommitSHA(b.Commit) {
		return apierr.Invalid("build.commit", "build.commit must be a full 40-character lowercase commit SHA").
			WithHint("leave commit empty to have Hangar resolve ref when planning")
	}
	if !relPathOK(b.Context) {
		return apierr.Invalid("build.context", "build.context %q must be a relative path inside the repository", b.Context)
	}
	if !relPathOK(b.Dockerfile) {
		return apierr.Invalid("build.dockerfile", "build.dockerfile %q must be a relative path inside the build context", b.Dockerfile)
	}
	for k, v := range b.Args {
		if !envKeyRe.MatchString(k) {
			return apierr.Invalid("build.args."+k, "build arg %q must match [A-Za-z_][A-Za-z0-9_]*", k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return apierr.Invalid("build.args."+k, "build arg %q must be a single line", k)
		}
	}
	return nil
}

// hasPassword reports whether an http(s) URL carries userinfo, which would
// put credentials into specs, plans and the audit log.
func hasPassword(u string) bool {
	rest, ok := strings.CutPrefix(u, "https://")
	if !ok {
		rest, ok = strings.CutPrefix(u, "http://")
	}
	if !ok {
		return false
	}
	host, _, _ := strings.Cut(rest, "/")
	return strings.Contains(host, "@")
}

func relPathOK(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") || strings.ContainsAny(p, "\x00\\") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// Clone returns a deep copy.
func (b *Build) Clone() *Build {
	if b == nil {
		return nil
	}
	c := *b
	if b.Args != nil {
		c.Args = make(map[string]string, len(b.Args))
		for k, v := range b.Args {
			c.Args[k] = v
		}
	}
	return &c
}

// Source describes where the app's image comes from, for display.
func (a *App) Source() string {
	if a.Build == nil {
		return a.Image
	}
	s := a.Build.Git
	if a.Build.Ref != "" {
		s += "@" + a.Build.Ref
	}
	if len(a.Build.Commit) >= 12 {
		s += " (" + a.Build.Commit[:12] + ")"
	}
	return s
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
	c.Build = a.Build.Clone()
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
	c.Build = a.Build.Clone()
	return &c
}
