// Package spec defines the declarative app spec (coldship.yaml), the single
// source of truth an agent edits before running plan/apply.
package spec

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/dunkbing/coldship/internal/apierr"
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
	Commit     string            `json:"commit,omitempty" yaml:"commit,omitempty" jsonschema:"optional full commit SHA pin. When empty, Coldship resolves ref at plan time and records the commit in the plan"`
	Context    string            `json:"context,omitempty" yaml:"context,omitempty" jsonschema:"build context directory inside the repository (default .)"`
	Dockerfile string            `json:"dockerfile,omitempty" yaml:"dockerfile,omitempty" jsonschema:"Dockerfile path relative to the context (default Dockerfile)"`
	Args       map[string]string `json:"args,omitempty" yaml:"args,omitempty" jsonschema:"docker build args. Visible in plans and image history: never put secrets here"`
	// Builder picks how the image is built: "dockerfile" requires the
	// Dockerfile, "auto" generates one from the project, and empty uses the
	// Dockerfile if the repository has one and generates one otherwise.
	Builder string `json:"builder,omitempty" yaml:"builder,omitempty" jsonschema:"dockerfile, auto, or empty (default: the Dockerfile if present, otherwise detect the project and generate one)"`
	// Auto overrides what detection decides for generated Dockerfiles.
	Auto *AutoBuild `json:"auto,omitempty" yaml:"auto,omitempty" jsonschema:"overrides for builds without a Dockerfile: language, version, install, build, start"`
}

// Database engines Coldship manages.
const (
	EnginePostgres = "postgres"
	EngineMySQL    = "mysql"
	EngineRedis    = "redis"
	// EngineS3 is S3-compatible object storage (SeaweedFS). It is only
	// available as a standalone data service, not as an app's database.
	EngineS3 = "s3"
	// EngineRustFS is S3-compatible object storage on RustFS.
	EngineRustFS = "rustfs"
)

// IsObjectStorage reports whether engine is S3-compatible storage, which
// is a data service only and isn't snapshotted.
func IsObjectStorage(engine string) bool { return engine == EngineS3 || engine == EngineRustFS }

// Database is a managed database attached to the app. Coldship runs it, keeps
// its data in a volume, injects its connection URL into the app's env, and
// snapshots it before any destructive change.
type Database struct {
	Name    string `json:"name" yaml:"name" jsonschema:"database name, lowercase letters, digits and dashes"`
	Engine  string `json:"engine" yaml:"engine" jsonschema:"postgres, mysql or redis"`
	Version string `json:"version,omitempty" yaml:"version,omitempty" jsonschema:"major version, e.g. 17 for postgres, 8.4 for mysql or 7 for redis (default: latest supported)"`
	Env     string `json:"env,omitempty" yaml:"env,omitempty" jsonschema:"env var the connection URL is injected as (default DATABASE_URL for postgres and mysql, REDIS_URL for redis)"`
}

var supportedVersions = map[string][]string{
	EnginePostgres: {"14", "15", "16", "17"},
	EngineMySQL:    {"8.4"},
	EngineRedis:    {"6", "7"},
	EngineS3:       {"4.51"},
	EngineRustFS:   {"1.0.4"},
}

// Versions lists the supported versions of an engine, oldest first; the
// last is the default.
func Versions(engine string) []string { return supportedVersions[engine] }

// OnPush says what a Git webhook push does. It applies only to apps built
// from Git.
type OnPush struct {
	// Action for pushes to the app's own ref: "plan" leaves a plan for
	// review, "deploy" applies it (unless it needs approval).
	Action string `json:"action,omitempty" yaml:"action,omitempty" jsonschema:"what a push to the app's ref does: plan (default; leave a plan to review) or deploy (apply it)"`
	// Previews makes pushes to other branches create or update previews,
	// and deleting a branch delete its preview.
	Previews   bool   `json:"previews,omitempty" yaml:"previews,omitempty" jsonschema:"pushes to other branches create or update a preview; deleting the branch deletes it"`
	PreviewTTL string `json:"preview_ttl,omitempty" yaml:"preview_ttl,omitempty" jsonschema:"lifetime of previews created by pushes, e.g. 72h (default: the server's preview TTL)"`
}

const (
	OnPushPlan   = "plan"
	OnPushDeploy = "deploy"
)

// Health tells Coldship when a newly started container is ready for traffic.
type Health struct {
	Path    string `json:"path,omitempty" yaml:"path,omitempty" jsonschema:"HTTP path that returns 2xx or 3xx when the app is ready, e.g. /healthz. Without it, Coldship waits for the port to accept connections"`
	Timeout string `json:"timeout,omitempty" yaml:"timeout,omitempty" jsonschema:"how long a new container may take to become healthy, e.g. 60s (default 60s, max 10m)"`
}

// TimeoutOrDefault parses Timeout (already validated) or returns the default.
func (h *Health) TimeoutOrDefault() time.Duration {
	if h != nil && h.Timeout != "" {
		if d, err := time.ParseDuration(h.Timeout); err == nil {
			return d
		}
	}
	return 60 * time.Second
}

type App struct {
	Name      string            `json:"name" yaml:"name" jsonschema:"app name: lowercase letters, digits and dashes, starting with a letter"`
	Stage     Stage             `json:"stage,omitempty" yaml:"stage,omitempty" jsonschema:"production or staging (default). Destructive changes to production, and leaving production, need human approval. preview is reserved for apps made by coldship preview create"`
	Image     string            `json:"image,omitempty" yaml:"image,omitempty" jsonschema:"container image to run, e.g. nginx:1.27. Set either image or build"`
	Build     *Build            `json:"build,omitempty" yaml:"build,omitempty" jsonschema:"build the image from a Git repository instead of using image"`
	OnPush    *OnPush           `json:"on_push,omitempty" yaml:"on_push,omitempty" jsonschema:"what Git webhook pushes do (apps built from Git; enable the webhook with coldship webhook enable)"`
	Port      int               `json:"port,omitempty" yaml:"port,omitempty" jsonschema:"port the app listens on inside the container"`
	HostPort  int               `json:"host_port,omitempty" yaml:"host_port,omitempty" jsonschema:"host port to publish the container port on (requires port). Apps with a host_port cannot be deployed without downtime; prefer domains"`
	Domains   []string          `json:"domains,omitempty" yaml:"domains,omitempty" jsonschema:"hostnames routed to this app through Coldship's proxy with automatic HTTPS, e.g. app.example.com (requires port)"`
	Health    *Health           `json:"health,omitempty" yaml:"health,omitempty" jsonschema:"readiness check used before switching traffic to a new version"`
	Memory    string            `json:"memory,omitempty" yaml:"memory,omitempty" jsonschema:"memory limit such as 256m or 1g"`
	Env       map[string]string `json:"env,omitempty" yaml:"env,omitempty" jsonschema:"environment variables; values are write-only and redacted in all output"`
	Volumes   []Volume          `json:"volumes,omitempty" yaml:"volumes,omitempty" jsonschema:"persistent volumes. Removing one from the spec deletes its data"`
	Databases []Database        `json:"databases,omitempty" yaml:"databases,omitempty" jsonschema:"managed databases. Their connection URLs are injected into env. Removing one deletes its data (after a snapshot)"`
	Uses      []string          `json:"uses,omitempty" yaml:"uses,omitempty" jsonschema:"data services the app connects to (see coldship data list). Each injects its connection env: DATABASE_URL for postgres and mysql, REDIS_URL for redis, AWS_* and S3_* variables for s3 storage. Write name=ENV_VAR to inject a database's URL under another name"`
}

var (
	nameRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	memoryRe = regexp.MustCompile(`^[1-9][0-9]*[kmg]$`)
	slugRe   = regexp.MustCompile(`[^a-z0-9-]+`)
	shaRe    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	refRe    = regexp.MustCompile(`^[A-Za-z0-9._/+-]{1,200}$`)
	// https://host/..., http://host/..., ssh://[user@]host/..., or scp-like user@host:path.
	// Lowercase DNS hostname with at least two labels; no wildcards.
	domainRe = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	gitURLRe = regexp.MustCompile(`^(?:(?:https?|ssh)://[A-Za-z0-9._~%!$&'()*+,;=:@-]+(?:/\S*)?|[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:\S+)$`)
)

// ParseUse splits a uses entry, "maindb" or "reports=REPORTS_URL", into the
// data service's name and the env var override (or "").
func ParseUse(u string) (name, env string) {
	name, env, _ = strings.Cut(strings.TrimSpace(u), "=")
	return name, env
}

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
			WithHint("allowed fields: name, stage, image, build (git, ref, commit, context, dockerfile, args, builder, auto), port, host_port, domains, health (path, timeout), memory, env, volumes, databases (name, engine, version, env), on_push (action, previews, preview_ttl)")
	}
	a.Normalize()
	return &a, a.Validate()
}

// Normalize fills defaults in place.
func (a *App) Normalize() {
	if a.Stage == "" {
		a.Stage = StageStaging
	}
	for i := range a.Databases {
		d := &a.Databases[i]
		if d.Version == "" {
			if vs := supportedVersions[d.Engine]; len(vs) > 0 {
				d.Version = vs[len(vs)-1]
			}
		}
		if d.Env == "" {
			switch d.Engine {
			case EnginePostgres, EngineMySQL:
				d.Env = "DATABASE_URL"
			case EngineRedis:
				d.Env = "REDIS_URL"
			}
		}
	}
	if a.OnPush != nil && a.OnPush.Action == "" {
		a.OnPush.Action = OnPushPlan
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
		return apierr.Invalid("port", "host_port is set but port is not; Coldship needs to know which container port to publish").
			WithFix(map[string]any{"port": a.HostPort})
	}
	seen := map[string]bool{}
	for i, d := range a.Domains {
		f := fmt.Sprintf("domains[%d]", i)
		low := strings.ToLower(strings.TrimSuffix(d, "."))
		switch {
		case strings.Contains(d, "*"):
			return apierr.Invalid(f, "wildcard domain %q is not supported; list each hostname", d)
		case strings.Contains(d, "://") || strings.Contains(d, "/"):
			return apierr.Invalid(f, "domain %q must be a bare hostname, without scheme or path", d).
				WithFix(map[string]any{"domains": []string{bareHost(d)}})
		case low != d:
			return apierr.Invalid(f, "domain %q must be lowercase, without a trailing dot", d).WithFix(map[string]any{"domains": []string{low}})
		case len(d) > 253 || !domainRe.MatchString(d):
			return apierr.Invalid(f, "domain %q is not a valid hostname", d)
		case seen[d]:
			return apierr.Invalid(f, "domain %q is listed twice", d)
		}
		seen[d] = true
	}
	if len(a.Domains) > 0 && a.Port == 0 {
		return apierr.Invalid("port", "domains are set but port is not; Coldship needs to know which container port to route traffic to").
			WithFix(map[string]any{"port": 8080})
	}
	if h := a.Health; h != nil {
		if h.Path != "" && (!strings.HasPrefix(h.Path, "/") || strings.ContainsAny(h.Path, " \t\r\n")) {
			return apierr.Invalid("health.path", "health.path %q must start with / and contain no spaces", h.Path).
				WithFix(map[string]any{"health": map[string]any{"path": "/" + strings.TrimLeft(strings.TrimSpace(h.Path), "/")}})
		}
		if h.Timeout != "" {
			d, err := time.ParseDuration(h.Timeout)
			if err != nil || d < time.Second || d > 10*time.Minute {
				return apierr.Invalid("health.timeout", "health.timeout %q must be a duration between 1s and 10m, e.g. 60s", h.Timeout).
					WithFix(map[string]any{"health": map[string]any{"timeout": "60s"}})
			}
		}
		if a.Port == 0 {
			return apierr.Invalid("port", "health is set but port is not; health checks connect to the app's port")
		}
	}
	if op := a.OnPush; op != nil {
		switch {
		case a.Build == nil:
			return apierr.Invalid("on_push", "on_push needs build: only apps built from Git receive pushes")
		case op.Action != OnPushPlan && op.Action != OnPushDeploy:
			return apierr.Invalid("on_push.action", "on_push.action %q must be plan or deploy", op.Action).WithFix(map[string]any{"on_push": map[string]any{"action": OnPushPlan}})
		case op.PreviewTTL != "" && !op.Previews:
			return apierr.Invalid("on_push.preview_ttl", "on_push.preview_ttl is set but previews is off").WithFix(map[string]any{"on_push": map[string]any{"previews": true}})
		}
		if op.PreviewTTL != "" {
			if d, err := time.ParseDuration(op.PreviewTTL); err != nil || d < time.Minute {
				return apierr.Invalid("on_push.preview_ttl", "on_push.preview_ttl %q must be a duration of at least 1m, e.g. 72h", op.PreviewTTL)
			}
		}
	}
	seenUses := map[string]bool{}
	for i, u := range a.Uses {
		f := fmt.Sprintf("uses[%d]", i)
		name, env := ParseUse(u)
		switch {
		case !nameRe.MatchString(name):
			return apierr.Invalid(f, "data service %q must be a name like maindb, optionally with =ENV_VAR", u)
		case env != "" && !envKeyRe.MatchString(env):
			return apierr.Invalid(f, "env %q in %q must match [A-Za-z_][A-Za-z0-9_]*", env, u)
		case seenUses[name]:
			return apierr.Invalid(f, "data service %q is listed twice", name)
		}
		seenUses[name] = true
	}
	dbNames, dbEnvs := map[string]bool{}, map[string]bool{}
	for i, d := range a.Databases {
		f := fmt.Sprintf("databases[%d]", i)
		vs, ok := supportedVersions[d.Engine]
		switch {
		case !nameRe.MatchString(d.Name):
			return apierr.Invalid(f+".name", "database name %q must be lowercase letters, digits and dashes", d.Name)
		case dbNames[d.Name]:
			return apierr.Invalid(f+".name", "database %q is listed twice", d.Name)
		case !ok || IsObjectStorage(d.Engine):
			return apierr.Invalid(f+".engine", "engine %q is not supported for an app's database; use postgres, mysql or redis (S3 storage is a data service: coldship data create s3 <name>)", d.Engine).
				WithFix(map[string]any{"engine": EnginePostgres})
		case !contains(vs, d.Version):
			return apierr.Invalid(f+".version", "%s version %q is not supported; use one of %s", d.Engine, d.Version, strings.Join(vs, ", ")).
				WithFix(map[string]any{"version": vs[len(vs)-1]})
		case !envKeyRe.MatchString(d.Env):
			return apierr.Invalid(f+".env", "env %q must match [A-Za-z_][A-Za-z0-9_]*", d.Env)
		case dbEnvs[d.Env]:
			return apierr.Invalid(f+".env", "two databases inject %s; give each its own env name", d.Env).
				WithFix(map[string]any{"env": strings.ToUpper(strings.ReplaceAll(d.Name, "-", "_")) + "_URL"})
		}
		if _, clash := a.Env[d.Env]; clash {
			return apierr.Invalid(f+".env", "env %s is set in env and also injected by database %s; remove one", d.Env, d.Name)
		}
		dbNames[d.Name], dbEnvs[d.Env] = true, true
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
			WithHint("remove the credentials from the URL and give the server a git credentials file with `coldship server --git-credentials`")
	}
	if b.Ref != "" && (!refRe.MatchString(b.Ref) || strings.HasPrefix(b.Ref, "-") || strings.Contains(b.Ref, "..")) {
		return apierr.Invalid("build.ref", "build.ref %q is not a valid branch, tag or commit", b.Ref)
	}
	if b.Commit != "" && !IsCommitSHA(b.Commit) {
		return apierr.Invalid("build.commit", "build.commit must be a full 40-character lowercase commit SHA").
			WithHint("leave commit empty to have Coldship resolve ref when planning")
	}
	if !relPathOK(b.Context) {
		return apierr.Invalid("build.context", "build.context %q must be a relative path inside the repository", b.Context)
	}
	switch b.Builder {
	case "", BuilderDockerfile, BuilderAuto:
	default:
		return apierr.Invalid("build.builder", "build.builder %q must be dockerfile, auto, or empty", b.Builder).
			WithFix(map[string]any{"build": map[string]any{"builder": BuilderAuto}})
	}
	if b.Auto != nil {
		if b.Builder == BuilderDockerfile {
			return apierr.Invalid("build.auto", "build.auto has no effect with builder: dockerfile; remove one of them")
		}
		if err := b.Auto.validate(); err != nil {
			return err
		}
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

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func bareHost(s string) string {
	if _, rest, ok := strings.Cut(s, "://"); ok {
		s = rest
	}
	s, _, _ = strings.Cut(s, "/")
	s, _, _ = strings.Cut(s, ":")
	return strings.ToLower(strings.TrimSuffix(s, "."))
}

// ZeroDowntime reports whether the app can be redeployed by starting the new
// container next to the old one: impossible with a host port (two containers
// can't bind it) or volumes (two copies of e.g. a database must never share
// a data directory).
func (a *App) ZeroDowntime() bool {
	return a.HostPort == 0 && len(a.Volumes) == 0
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

// AutoBuild overrides project detection for builds without a Dockerfile.
type AutoBuild struct {
	Language string `json:"language,omitempty" yaml:"language,omitempty" jsonschema:"node, bun, python, go or static (default: detected)"`
	Version  string `json:"version,omitempty" yaml:"version,omitempty" jsonschema:"runtime version, e.g. 22 for node or 3.12 for python (default: from the project, else a current one)"`
	Install  string `json:"install,omitempty" yaml:"install,omitempty" jsonschema:"command that installs dependencies"`
	Build    string `json:"build,omitempty" yaml:"build,omitempty" jsonschema:"command that builds the app (go: the go build target, e.g. ./cmd/server)"`
	Start    string `json:"start,omitempty" yaml:"start,omitempty" jsonschema:"command that starts the app"`
}

const (
	BuilderDockerfile = "dockerfile"
	BuilderAuto       = "auto"
)

// Languages that builds without a Dockerfile support.
var AutoLanguages = []string{"node", "bun", "python", "go", "static"}

var (
	versionRe = regexp.MustCompile(`^[0-9]{1,2}(\.[0-9]{1,2})?$`)
)

func (a *AutoBuild) validate() error {
	if a.Language != "" && !contains(AutoLanguages, a.Language) {
		return apierr.Invalid("build.auto.language", "language %q is not supported; use one of %s", a.Language, strings.Join(AutoLanguages, ", "))
	}
	if a.Version != "" && !versionRe.MatchString(a.Version) {
		return apierr.Invalid("build.auto.version", "version %q must look like 22 or 3.12", a.Version)
	}
	for field, cmd := range map[string]string{"install": a.Install, "build": a.Build, "start": a.Start} {
		if len(cmd) > 500 || strings.ContainsAny(cmd, "\r\n\x00") {
			return apierr.Invalid("build.auto."+field, "build.auto.%s must be a single line of at most 500 characters", field)
		}
	}
	return nil
}

// UsesAuto reports whether the build is configured beyond the original
// Dockerfile-only fields (used to keep image tags of existing apps stable).
func (b *Build) UsesAuto() bool { return b.Builder != "" || b.Auto != nil }

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
	if b.Auto != nil {
		a := *b.Auto
		c.Auto = &a
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
	c.Domains = append([]string(nil), a.Domains...)
	c.Databases = append([]Database(nil), a.Databases...)
	c.Uses = append([]string(nil), a.Uses...)
	if a.OnPush != nil {
		op := *a.OnPush
		c.OnPush = &op
	}
	if a.Health != nil {
		h := *a.Health
		c.Health = &h
	}
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
	c.Domains = append([]string(nil), a.Domains...)
	c.Databases = append([]Database(nil), a.Databases...)
	c.Uses = append([]string(nil), a.Uses...)
	if a.OnPush != nil {
		op := *a.OnPush
		c.OnPush = &op
	}
	if a.Health != nil {
		h := *a.Health
		c.Health = &h
	}
	c.Build = a.Build.Clone()
	return &c
}
