package builder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"hangar/internal/apierr"
	"hangar/internal/spec"
)

// AutogenVersion is bumped whenever the generated Dockerfiles change, so
// images built from older templates aren't reused.
const AutogenVersion = "autogen-v1"

// Detection explains how Hangar builds a project without a Dockerfile.
type Detection struct {
	Mode           string   `json:"mode"` // auto | dockerfile
	Language       string   `json:"language,omitempty"`
	Version        string   `json:"version,omitempty"`
	PackageManager string   `json:"package_manager,omitempty"`
	Install        string   `json:"install,omitempty"`
	Build          string   `json:"build,omitempty"`
	Start          string   `json:"start,omitempty"`
	DefaultPort    int      `json:"default_port,omitempty"`
	Notes          []string `json:"notes"`
	Dockerfile     string   `json:"dockerfile,omitempty"`
	ignore         string
}

// Summary is a one-line description, e.g. "node 22 · pnpm · pnpm start".
func (d *Detection) Summary() string {
	if d.Mode != "auto" {
		return "the repository's Dockerfile"
	}
	parts := []string{d.Language + " " + d.Version}
	if d.PackageManager != "" {
		parts = append(parts, d.PackageManager)
	}
	if d.Start != "" {
		parts = append(parts, d.Start)
	}
	return strings.Join(parts, " · ")
}

// project gives safe read access to the build context: no symlinks, no
// paths outside it, small files only.
type project struct {
	root  string
	notes *[]string
}

func (p project) has(name string) bool {
	fi, err := os.Lstat(filepath.Join(p.root, name))
	return err == nil && fi.Mode()&os.ModeSymlink == 0
}

func (p project) read(name string) (string, bool) {
	path := filepath.Join(p.root, name)
	fi, err := os.Lstat(path)
	if err != nil {
		return "", false
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		*p.notes = append(*p.notes, fmt.Sprintf("ignored %s because it is a symlink", name))
		return "", false
	}
	if !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
		return "", false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(b), true
}

var (
	majorRe     = regexp.MustCompile(`([0-9]{1,2})(\.[0-9xX*]+)*`)
	pyVersionRe = regexp.MustCompile(`3\.([0-9]{1,2})`)
	goVersionRe = regexp.MustCompile(`(?m)^go\s+(1\.[0-9]{1,2})`)
	relFileRe   = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	goPkgRe     = regexp.MustCompile(`^\.(/[A-Za-z0-9._-]+)*$`)
	pkgMainRe   = regexp.MustCompile(`(?m)^package\s+main\b`)
)

var defaultVersions = map[string]string{"node": "22", "bun": "1", "python": "3.12", "go": "1.23"}
var defaultPorts = map[string]int{"node": 3000, "bun": 3000, "python": 8000, "go": 8080, "static": 80}

// Detect works out how to build the project at root (the build context)
// and generates a Dockerfile for it. Values read from the repository are
// untrusted: they only reach the Dockerfile if they match strict patterns.
func Detect(root string, o *spec.AutoBuild) (*Detection, error) {
	if o == nil {
		o = &spec.AutoBuild{}
	}
	d := &Detection{Mode: "auto", Notes: []string{}}
	p := project{root: root, notes: &d.Notes}

	lang := o.Language
	if lang != "" {
		d.note("language %s, from build.auto.language", lang)
	} else {
		switch {
		case p.has("bun.lock") || p.has("bun.lockb"):
			lang = "bun"
			d.note("language bun: found a bun lockfile")
		case p.has("package.json"):
			lang = "node"
			if why := bunHint(p); why != "" {
				lang = "bun"
				d.note("language bun: %s", why)
			} else {
				d.note("language node: found package.json")
			}
		case p.has("go.mod"):
			lang = "go"
			d.note("language go: found go.mod")
		case p.has("requirements.txt") || p.has("pyproject.toml") || p.has("uv.lock") || p.has("setup.py"):
			lang = "python"
			d.note("language python: found Python project files")
		case p.has("index.html"):
			lang = "static"
			d.note("static site: found index.html and no app code, so it is served by nginx")
		default:
			return nil, undetected(root)
		}
	}
	d.Language, d.DefaultPort = lang, defaultPorts[lang]

	var err error
	switch lang {
	case "node", "bun":
		err = d.node(p, o)
	case "python":
		err = d.python(p, o)
	case "go":
		err = d.golang(p, o)
	case "static":
		d.static()
	}
	if err != nil {
		return nil, err
	}
	d.ignore = ".git\nnode_modules\n.venv\n__pycache__\n*.pyc\n.env\n.env.*\n"
	if extra, ok := p.read(".dockerignore"); ok {
		d.ignore += extra
		d.note("also applied the repository's .dockerignore")
	}
	return d, nil
}

func (d *Detection) note(format string, args ...any) {
	d.Notes = append(d.Notes, fmt.Sprintf(format, args...))
}

// version picks the override, else a value found in the project if it
// matches pattern, else the default.
func (d *Detection) version(o *spec.AutoBuild, found, source string) {
	switch {
	case o.Version != "":
		d.Version = o.Version
		d.note("version %s, from build.auto.version", d.Version)
	case found != "":
		d.Version = found
		d.note("version %s, from %s", d.Version, source)
	default:
		d.Version = defaultVersions[d.Language]
		d.note("version %s (default; set build.auto.version to change it)", d.Version)
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func (d *Detection) node(p project, o *spec.AutoBuild) error {
	var pkg struct {
		Main    string            `json:"main"`
		Scripts map[string]string `json:"scripts"`
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if raw, ok := p.read("package.json"); ok {
		if err := json.Unmarshal([]byte(raw), &pkg); err != nil {
			return apierr.New(422, "build_detect_failed", "package.json is not valid JSON: %v", err).WithField("build")
		}
	}

	found, source := "", ""
	if d.Language == "node" {
		for _, f := range []string{".nvmrc", ".node-version"} {
			if v, ok := p.read(f); ok {
				if m := majorRe.FindStringSubmatch(strings.TrimPrefix(firstLine(v), "v")); m != nil && validNodeMajor(m[1]) {
					found, source = m[1], f
					break
				}
			}
		}
		if found == "" && pkg.Engines.Node != "" {
			if m := majorRe.FindStringSubmatch(pkg.Engines.Node); m != nil && validNodeMajor(m[1]) {
				found, source = m[1], "engines.node in package.json"
			}
		}
	}
	d.version(o, found, source)

	user, run := "node", "npm run"
	image := "node:" + d.Version + "-alpine"
	var lockfiles []string
	install := ""
	switch {
	case d.Language == "bun":
		d.PackageManager, user, run, image = "bun", "bun", "bun run", "oven/bun:"+d.Version+"-alpine"
		install = "bun install"
		for _, f := range []string{"bun.lock", "bun.lockb"} {
			if p.has(f) {
				lockfiles = append(lockfiles, f)
				install = "bun install --frozen-lockfile"
			}
		}
	case p.has("pnpm-lock.yaml"):
		d.PackageManager, run, install, lockfiles = "pnpm", "pnpm run", "pnpm install --frozen-lockfile", []string{"pnpm-lock.yaml"}
	case p.has("yarn.lock"):
		d.PackageManager, run, lockfiles = "yarn", "yarn run", []string{"yarn.lock"}
		install = "yarn install --frozen-lockfile"
		if p.has(".yarnrc.yml") {
			install = "yarn install --immutable"
			lockfiles = append(lockfiles, ".yarnrc.yml")
		}
	case p.has("package-lock.json"):
		d.PackageManager, install, lockfiles = "npm", "npm ci", []string{"package-lock.json"}
	default:
		d.PackageManager, install = "npm", "npm install"
		d.note("no lockfile found, so dependencies are not pinned; commit one for reproducible builds")
	}
	if d.PackageManager != "bun" {
		d.note("package manager %s, chosen from the lockfile", d.PackageManager)
	}
	for _, f := range []string{".npmrc"} {
		if p.has(f) {
			lockfiles = append(lockfiles, f)
		}
	}
	d.Install = pick(o.Install, install, &d.Notes, "install")

	build := ""
	if _, ok := pkg.Scripts["build"]; ok {
		build = run + " build"
	}
	d.Build = pick(o.Build, build, &d.Notes, "build")

	start := ""
	switch {
	case pkg.Scripts["start"] != "":
		start = strings.Replace(run, " run", "", 1) + " start"
		if d.PackageManager == "bun" {
			start = "bun run start"
		}
	case pkg.Main != "" && relFileRe.MatchString(pkg.Main) && !strings.Contains(pkg.Main, "..") && p.has(pkg.Main):
		start = runtimeCmd(d.Language) + " " + pkg.Main
	default:
		for _, f := range []string{"server.js", "index.js", "app.js", "server.ts", "index.ts", "src/index.ts"} {
			if p.has(f) && (strings.HasSuffix(f, ".js") || d.Language == "bun") {
				start = runtimeCmd(d.Language) + " " + f
				break
			}
		}
	}
	if s, ok := d.procfile(p); ok && o.Start == "" {
		start = s
	}
	d.Start = pick(o.Start, start, &d.Notes, "start")
	if d.Start == "" {
		return noStart(d.Language, "add a \"start\" script to package.json, or set build.auto.start", "node server.js")
	}

	var b strings.Builder
	d.header(&b)
	fmt.Fprintf(&b, "FROM %s\n", image)
	if d.PackageManager == "pnpm" || d.PackageManager == "yarn" {
		b.WriteString("ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0\nRUN corepack enable\n")
	}
	fmt.Fprintf(&b, "WORKDIR /app\nRUN chown %s:%s /app\nUSER %s\n", user, user, user)
	fmt.Fprintf(&b, "COPY --chown=%s:%s %s ./\n", user, user, strings.Join(append([]string{"package.json"}, lockfiles...), " "))
	fmt.Fprintf(&b, "RUN %s\n", d.Install)
	fmt.Fprintf(&b, "COPY --chown=%s:%s . .\n", user, user)
	if d.Build != "" {
		fmt.Fprintf(&b, "RUN %s\n", d.Build)
	}
	// NODE_ENV only now: set before the install, npm and yarn would skip the
	// devDependencies that the build step needs.
	fmt.Fprintf(&b, "ENV NODE_ENV=production PORT=%d\nEXPOSE %d\n", d.DefaultPort, d.DefaultPort)
	b.WriteString(execCmd(d.Start))
	d.Dockerfile = b.String()
	return nil
}

// bunHint finds signs of a Bun project that has no lockfile yet (Bun
// writes none until there are dependencies).
func bunHint(p project) string {
	if p.has("bunfig.toml") {
		return "found bunfig.toml"
	}
	raw, ok := p.read("package.json")
	if !ok {
		return ""
	}
	var pkg struct {
		PackageManager string            `json:"packageManager"`
		Scripts        map[string]string `json:"scripts"`
	}
	if json.Unmarshal([]byte(raw), &pkg) != nil {
		return ""
	}
	switch {
	case strings.HasPrefix(pkg.PackageManager, "bun@"):
		return "packageManager in package.json is bun"
	case strings.HasPrefix(pkg.Scripts["start"], "bun "):
		return "the start script runs bun"
	}
	return ""
}

func validNodeMajor(s string) bool {
	return len(s) == 2 && s >= "16" && s <= "30"
}

func runtimeCmd(lang string) string {
	if lang == "bun" {
		return "bun"
	}
	return "node"
}

func (d *Detection) python(p project, o *spec.AutoBuild) error {
	found, source := "", ""
	if v, ok := p.read(".python-version"); ok {
		if m := pyVersionRe.FindStringSubmatch(firstLine(v)); m != nil {
			found, source = "3."+m[1], ".python-version"
		}
	}
	if v, ok := p.read("runtime.txt"); found == "" && ok {
		if m := pyVersionRe.FindStringSubmatch(firstLine(v)); m != nil {
			found, source = "3."+m[1], "runtime.txt"
		}
	}
	d.version(o, found, source)

	var setup, install string
	switch {
	case p.has("uv.lock") && p.has("pyproject.toml"):
		d.PackageManager = "uv"
		setup = "COPY --chown=app:app pyproject.toml uv.lock ./\n"
		install = "pip install uv && uv sync --frozen --no-dev --no-install-project"
	case p.has("requirements.txt"):
		d.PackageManager = "pip"
		setup = "COPY --chown=app:app requirements.txt ./\n"
		install = "pip install -r requirements.txt"
	case p.has("pyproject.toml") || p.has("setup.py"):
		d.PackageManager = "pip"
		install = "pip install ."
	}
	d.note("package manager %s", d.PackageManager)
	d.Install = pick(o.Install, install, &d.Notes, "install")
	d.Build = pick(o.Build, "", &d.Notes, "build")

	start := ""
	if s, ok := d.procfile(p); ok {
		start = s
	} else {
		for _, f := range []string{"main.py", "app.py", "server.py"} {
			if p.has(f) {
				start = "python " + f
				d.note("start: found %s", f)
				break
			}
		}
	}
	d.Start = pick(o.Start, start, &d.Notes, "start")
	if d.Start == "" {
		hint := "add a Procfile with a web: line, or set build.auto.start"
		if p.has("manage.py") {
			hint = "this looks like Django: set build.auto.start, e.g. gunicorn mysite.wsgi --bind 0.0.0.0:$PORT (add gunicorn to your requirements)"
		}
		return noStart("python", hint, "python main.py")
	}

	var b strings.Builder
	d.header(&b)
	fmt.Fprintf(&b, "FROM python:%s-slim\n", d.Version)
	b.WriteString("ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1\n")
	b.WriteString("WORKDIR /app\nRUN useradd --create-home --uid 10001 app && chown app:app /app\n")
	if setup != "" {
		b.WriteString(setup)
		fmt.Fprintf(&b, "RUN %s\n", d.Install)
		b.WriteString("COPY --chown=app:app . .\n")
	} else {
		b.WriteString("COPY --chown=app:app . .\n")
		fmt.Fprintf(&b, "RUN %s\n", d.Install)
	}
	if d.Build != "" {
		fmt.Fprintf(&b, "RUN %s\n", d.Build)
	}
	if d.PackageManager == "uv" {
		b.WriteString("ENV PATH=\"/app/.venv/bin:$PATH\"\n")
	}
	fmt.Fprintf(&b, "USER app\nENV PORT=%d\nEXPOSE %d\n", d.DefaultPort, d.DefaultPort)
	b.WriteString(execCmd(d.Start))
	d.Dockerfile = b.String()
	return nil
}

func (d *Detection) golang(p project, o *spec.AutoBuild) error {
	found := ""
	if mod, ok := p.read("go.mod"); ok {
		if m := goVersionRe.FindStringSubmatch(mod); m != nil {
			found = m[1]
		}
	}
	d.version(o, found, "go.mod")

	target := ""
	switch {
	case o.Build != "":
		if !goPkgRe.MatchString(o.Build) || strings.Contains(o.Build, "..") {
			return apierr.Invalid("build.auto.build", "for go, build.auto.build is the package to build, like ./cmd/server").
				WithFix(map[string]any{"build": map[string]any{"auto": map[string]any{"build": "./cmd/server"}}})
		}
		target = o.Build
		d.note("building %s, from build.auto.build", target)
	case hasMainPackage(p.root, "."):
		target = "."
		d.note("building the main package at the repository root")
	default:
		var mains []string
		if entries, err := os.ReadDir(filepath.Join(p.root, "cmd")); err == nil {
			for _, e := range entries {
				if e.IsDir() && hasMainPackage(p.root, filepath.Join("cmd", e.Name())) {
					mains = append(mains, "./cmd/"+e.Name())
				}
			}
		}
		sort.Strings(mains)
		switch len(mains) {
		case 0:
			return apierr.New(422, "build_detect_failed", "found go.mod but no main package (at the root or under cmd/)").
				WithField("build.auto.build").WithHint("set build.auto.build to the package to build")
		case 1:
			target = mains[0]
			d.note("building %s, the only main package under cmd/", target)
		default:
			return apierr.New(422, "build_detect_failed", "found several main packages: %s", strings.Join(mains, ", ")).
				WithField("build.auto.build").WithDetail("candidates", mains).
				WithFix(map[string]any{"build": map[string]any{"auto": map[string]any{"build": mains[0]}}}).
				WithHint("set build.auto.build to the one to deploy")
		}
	}
	d.Build = "go build " + target
	d.Install = pick(o.Install, "go mod download", &d.Notes, "install")
	start := "app"
	if s, ok := d.procfile(p); ok {
		start = s
	}
	d.Start = pick(o.Start, start, &d.Notes, "start")

	copyMod := "go.mod"
	if p.has("go.sum") {
		copyMod += " go.sum"
	}
	var b strings.Builder
	d.header(&b)
	fmt.Fprintf(&b, "FROM golang:%s-alpine AS build\nWORKDIR /src\n", d.Version)
	fmt.Fprintf(&b, "COPY %s ./\nRUN %s\nCOPY . .\n", copyMod, d.Install)
	fmt.Fprintf(&b, "RUN CGO_ENABLED=0 go build -trimpath -ldflags=\"-s -w\" -o /out/app %s\n\n", target)
	b.WriteString("FROM alpine:3.20\nRUN apk add --no-cache ca-certificates && adduser -D -u 10001 app\n")
	b.WriteString("COPY --from=build /out/app /usr/local/bin/app\nWORKDIR /app\nUSER app\n")
	fmt.Fprintf(&b, "ENV PORT=%d\nEXPOSE %d\n", d.DefaultPort, d.DefaultPort)
	if d.Start == "app" {
		b.WriteString("CMD [\"app\"]\n")
	} else {
		b.WriteString(execCmd(d.Start))
	}
	d.Dockerfile = b.String()
	return nil
}

func hasMainPackage(root, dir string) bool {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			if b, err := os.ReadFile(filepath.Join(root, dir, e.Name())); err == nil && len(b) < 1<<20 && pkgMainRe.Match(b) {
				return true
			}
		}
	}
	return false
}

func (d *Detection) static() {
	d.Version = "1.27"
	d.Start = "nginx"
	var b strings.Builder
	d.header(&b)
	b.WriteString("FROM nginx:1.27-alpine\n")
	// The official image fills in ${PORT} from env when it starts, so the
	// port Hangar injects at deploy time takes effect.
	b.WriteString("RUN mkdir -p /etc/nginx/templates && printf 'server {\\n  listen ${PORT};\\n  root /usr/share/nginx/html;\\n  location / { try_files $uri $uri/ /index.html; }\\n}\\n' > /etc/nginx/templates/default.conf.template \\\n")
	b.WriteString("    && rm -f /etc/nginx/conf.d/default.conf\n")
	b.WriteString("COPY . /usr/share/nginx/html\n")
	fmt.Fprintf(&b, "ENV PORT=%d NGINX_ENVSUBST_FILTER=^PORT$\nEXPOSE %d\n", d.DefaultPort, d.DefaultPort)
	d.Dockerfile = b.String()
}

// procfile reads a Heroku-style Procfile's web: command.
func (d *Detection) procfile(p project) (string, bool) {
	raw, ok := p.read("Procfile")
	if !ok {
		return "", false
	}
	for _, line := range strings.Split(raw, "\n") {
		if cmd, ok := strings.CutPrefix(strings.TrimSpace(line), "web:"); ok {
			cmd = strings.TrimSpace(cmd)
			if cmd == "" || len(cmd) > 500 || strings.ContainsAny(cmd, "\r\x00") {
				d.note("ignored the Procfile's web: line (empty, too long, or with control characters)")
				return "", false
			}
			d.note("start: the Procfile's web: line")
			return cmd, true
		}
	}
	return "", false
}

func pick(override, detected string, notes *[]string, what string) string {
	if override != "" {
		*notes = append(*notes, fmt.Sprintf("%s: %s, from build.auto.%s", what, override, what))
		return override
	}
	if detected != "" {
		*notes = append(*notes, fmt.Sprintf("%s: %s", what, detected))
	}
	return detected
}

// execCmd renders CMD in exec form through sh, so $PORT expands. JSON
// encoding escapes quotes; commands never contain newlines (validated).
func execCmd(cmd string) string {
	b, _ := json.Marshal([]string{"sh", "-c", cmd})
	return "CMD " + string(b) + "\n"
}

func (d *Detection) header(b *strings.Builder) {
	fmt.Fprintf(b, "# Generated by Hangar (%s) because the repository has no Dockerfile.\n", AutogenVersion)
	b.WriteString("# Customize it with build.auto in the app's spec, or commit your own Dockerfile.\n")
}

func noStart(lang, hint, example string) error {
	return apierr.New(422, "build_detect_failed", "found a %s project but not how to start it", lang).
		WithField("build.auto.start").WithHint(hint).
		WithFix(map[string]any{"build": map[string]any{"auto": map[string]any{"start": example}}})
}

func undetected(root string) error {
	var seen []string
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if len(seen) < 15 && !strings.HasPrefix(e.Name(), ".") {
			seen = append(seen, e.Name())
		}
	}
	return apierr.New(422, "build_detect_failed", "the repository has no Dockerfile, and Hangar couldn't tell what kind of project it is").
		WithField("build").WithDetail("files", seen).
		WithHint("add a Dockerfile, or set build.auto.language (one of " + strings.Join(spec.AutoLanguages, ", ") + ") and build.auto.start").
		WithFix(map[string]any{"build": map[string]any{"auto": map[string]any{"language": "node", "start": "node server.js"}}})
}
