// Package builder turns a spec.Build (a Git repository with a Dockerfile)
// into a local container image.
//
// Builds are pinned: the engine resolves a ref to a commit at plan time, and
// Build always builds exactly that commit. Images are tagged by commit and
// build configuration, so redeploys and rollbacks reuse them.
package builder

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/spec"
)

type Builder interface {
	// Resolve turns b.Ref (or the default branch) into a full commit SHA
	// without cloning the repository.
	Resolve(ctx context.Context, b spec.Build) (string, error)
	// Build produces a local image for b, whose Commit must be set.
	// Progress and errors are written to log. With opts.StaticDir set, a
	// plain static site is copied there instead, and no image is built.
	Build(ctx context.Context, app string, b spec.Build, log io.Writer, opts BuildOptions) (*Result, error)
	// Detect checks out b (Commit must be set) and reports how it would be
	// built, without building anything.
	Detect(ctx context.Context, b spec.Build, log io.Writer) (*Detection, error)
}

// BuildOptions tune a build.
type BuildOptions struct {
	// StaticDir, if set, receives the files of a plain static site (no
	// Dockerfile, no app code), which is then served without a container.
	StaticDir string
}

// Result describes a built image, or a static site's files.
type Result struct {
	Image string
	// Static is true when the site's files were copied to
	// BuildOptions.StaticDir and no image was built.
	Static bool
	// Auto is true when Coldship generated the Dockerfile. Such images read
	// their port from $PORT, which the engine sets to the spec's port.
	Auto    bool
	Summary string
}

// ImageTag is the deterministic local tag for a build: the same commit and
// build configuration always map to the same tag.
func ImageTag(app string, b spec.Build) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00", b.Git, b.Context, b.Dockerfile)
	keys := make([]string, 0, len(b.Args))
	for k := range b.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\x00", k, b.Args[k])
	}
	if b.UsesAuto() {
		// Only when set, so existing apps keep their image tags.
		fmt.Fprintf(h, "builder=%s\x00%s\x00", b.Builder, AutogenVersion)
		if a := b.Auto; a != nil {
			fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00", a.Language, a.Version, a.Install, a.Build, a.Start)
		}
	}
	commit := b.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	return "coldship-build/" + app + ":" + commit + "-" + hex.EncodeToString(h.Sum(nil))[:8]
}

// Git builds with the git and docker CLIs.
type Git struct {
	GitBin    string
	DockerBin string
	podman    bool
	// CredentialsFile is a git-credential-store file used for private
	// https repositories. SSH uses the server user's keys.
	CredentialsFile string
	// AllowedProtocols is passed as GIT_ALLOW_PROTOCOL. The default blocks
	// file:// and ext:: transports.
	AllowedProtocols string
	// WorkDir holds temporary checkouts (default: the OS temp dir).
	WorkDir string
	// Timeout bounds one build, including the fetch.
	Timeout time.Duration
}

func NewGit() *Git {
	return &Git{GitBin: "git", DockerBin: "docker", AllowedProtocols: "http:https:ssh", Timeout: 20 * time.Minute}
}

func (g *Git) env() []string {
	return append(os.Environ(),
		"GIT_ALLOW_PROTOCOL="+g.AllowedProtocols,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new",
		"DOCKER_BUILDKIT=1",
	)
}

// UsePodman builds with the podman command (which has no --progress flag).
func (g *Git) UsePodman() { g.DockerBin, g.podman = "podman", true }

// gitArgs prefixes args with config that disables interactive credential
// helpers and, if configured, uses the server's credential store.
func (g *Git) gitArgs(args ...string) []string {
	pre := []string{"-c", "credential.helper=", "-c", "core.hooksPath=/dev/null", "-c", "protocol.version=2"}
	if g.CredentialsFile != "" {
		pre = append(pre, "-c", "credential.helper=store --file="+g.CredentialsFile)
	}
	return append(pre, args...)
}

func (g *Git) run(ctx context.Context, dir string, log io.Writer, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = g.env()
	var out bytes.Buffer
	if log != nil {
		cmd.Stdout = io.MultiWriter(&out, log)
		cmd.Stderr = io.MultiWriter(&out, log)
	} else {
		cmd.Stdout, cmd.Stderr = &out, &out
	}
	err := cmd.Run()
	return out.String(), err
}

func (g *Git) Resolve(ctx context.Context, b spec.Build) (string, error) {
	if spec.IsCommitSHA(b.Commit) {
		return b.Commit, nil
	}
	if spec.IsCommitSHA(b.Ref) {
		return b.Ref, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	want := b.Ref
	if want == "" {
		want = "HEAD"
	}
	// Ask for the peeled form too: for an annotated tag, ls-remote filtered by
	// "v1" alone returns the tag object, not the commit it points to.
	out, err := g.run(ctx, "", nil, g.GitBin, g.gitArgs("ls-remote", "--", b.Git, want, want+"^{}")...)
	if err != nil {
		return "", unreachable(b.Git, out)
	}
	refs := parseLsRemote(out)
	candidates := []string{want}
	if b.Ref != "" && !strings.HasPrefix(b.Ref, "refs/") {
		candidates = []string{"refs/heads/" + b.Ref, "refs/tags/" + b.Ref + "^{}", "refs/tags/" + b.Ref}
	}
	for _, c := range candidates {
		if sha, ok := refs[c]; ok {
			return sha, nil
		}
	}
	e := apierr.New(422, "git_ref_not_found", "ref %q was not found in %s", b.Ref, b.Git).WithField("build.ref")
	if out, err := g.run(ctx, "", nil, g.GitBin, g.gitArgs("ls-remote", "--heads", "--tags", "--", b.Git)...); err == nil {
		var names []string
		for name := range parseLsRemote(out) {
			name = strings.TrimSuffix(name, "^{}")
			name = strings.TrimPrefix(strings.TrimPrefix(name, "refs/heads/"), "refs/tags/")
			if !contains(names, name) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		if len(names) > 25 {
			names = names[:25]
		}
		e.WithDetail("available_refs", names)
	}
	return "", e.WithHint("use an existing branch or tag, or a full 40-character commit SHA")
}

func unreachable(url, out string) error {
	msg := lastLine(out)
	e := apierr.New(422, "git_unreachable", "cannot read repository %s: %s", url, msg).WithField("build.git")
	switch {
	case strings.Contains(out, "Authentication failed"), strings.Contains(out, "could not read Username"),
		strings.Contains(out, "Permission denied"), strings.Contains(out, "terminal prompts disabled"):
		e.Code = "git_auth_failed"
		e.Hint = "the repository needs credentials: start the server with --git-credentials for https, or give the server user an SSH key"
	case strings.Contains(out, "not found"), strings.Contains(out, "does not appear to be a git repository"):
		e.Hint = "check the repository URL"
	default:
		e.Hint = "check the URL and that the server can reach the host"
	}
	return e
}

func (g *Git) Build(ctx context.Context, app string, b spec.Build, log io.Writer, opts BuildOptions) (*Result, error) {
	if !spec.IsCommitSHA(b.Commit) {
		return nil, apierr.New(400, "invalid_spec", "build.commit must be resolved before building").WithField("build.commit")
	}
	tag := ImageTag(app, b)
	if out, err := g.run(ctx, "", nil, g.DockerBin, "image", "inspect", "-f",
		`{{index .Config.Labels "coldship.builder"}}|{{index .Config.Labels "coldship.detected"}}`, tag); err == nil {
		mode, summary, _ := strings.Cut(strings.TrimSpace(out), "|")
		// A static site built into an image earlier is served as files now.
		if opts.StaticDir == "" || !strings.HasPrefix(summary, "static") {
			fmt.Fprintf(log, "coldship: image %s for commit %s already exists, reusing it\n", tag, b.Commit)
			return &Result{Image: tag, Auto: mode == "auto", Summary: summary}, nil
		}
	}

	ctx, cancel := context.WithTimeout(ctx, g.Timeout)
	defer cancel()
	co, err := g.checkout(ctx, b, log)
	if co != nil {
		defer os.RemoveAll(co.work)
	}
	if err != nil {
		return nil, g.timeout(ctx, err)
	}
	dockerfile, det, err := chooseDockerfile(co, b)
	if err != nil {
		return nil, err
	}
	if opts.StaticDir != "" && det.Mode == "auto" && det.Language == "static" {
		n, err := copySite(co.ctxDir, opts.StaticDir, det.ignore)
		if err != nil {
			return nil, apierr.New(500, "build_failed", "copying the site's files failed: %v", err)
		}
		fmt.Fprintf(log, "coldship: static site: copied %d files; the proxy serves them directly, with no container\n", n)
		return &Result{Static: true, Auto: true, Summary: det.Summary()}, nil
	}

	args := []string{"build", "-t", tag, "-f", dockerfile,
		"--label", "coldship.app=" + app, "--label", "coldship.commit=" + b.Commit, "--label", "coldship.git=" + b.Git}
	if !g.podman {
		args = append(args[:1], append([]string{"--progress=plain"}, args[1:]...)...)
	}
	if det.Mode == "auto" {
		args = append(args, "--label", "coldship.builder=auto", "--label", "coldship.detected="+det.Summary())
		fmt.Fprintf(log, "coldship: no Dockerfile used; detected %s\n", det.Summary())
		for _, n := range det.Notes {
			fmt.Fprintf(log, "coldship:   - %s\n", n)
		}
		fmt.Fprintf(log, "coldship: generated Dockerfile:\n%s", indent(det.Dockerfile))
	}
	keys := make([]string, 0, len(b.Args))
	for k := range b.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--build-arg", k+"="+b.Args[k])
	}
	args = append(args, co.ctxDir)
	if det.Mode == "auto" {
		fmt.Fprintf(log, "coldship: docker build (generated Dockerfile) %s\n", b.Context)
	} else {
		fmt.Fprintf(log, "coldship: docker build -f %s %s\n", b.Dockerfile, b.Context)
	}
	if _, err := g.run(ctx, "", log, g.DockerBin, args...); err != nil {
		if err := g.timeout(ctx, nil); err != nil {
			return nil, err
		}
		e := apierr.New(422, "build_failed", "docker build failed for commit %s", b.Commit[:12]).
			WithHint("read the build log for the failing step, fix the repository, then plan again")
		if det.Mode == "auto" {
			e.Message += " (with a Dockerfile Coldship generated: " + det.Summary() + ")"
			e.Hint = "read the build log; if the generated Dockerfile guessed wrong, set build.auto (language, version, install, build, start) or commit your own Dockerfile"
			e.WithDetail("detected", det.Summary())
		}
		return nil, e
	}
	fmt.Fprintf(log, "coldship: built %s\n", tag)
	return &Result{Image: tag, Auto: det.Mode == "auto", Summary: det.Summary()}, nil
}

func (g *Git) Detect(ctx context.Context, b spec.Build, log io.Writer) (*Detection, error) {
	if !spec.IsCommitSHA(b.Commit) {
		return nil, apierr.New(400, "invalid_spec", "build.commit must be resolved before detecting").WithField("build.commit")
	}
	ctx, cancel := context.WithTimeout(ctx, g.Timeout)
	defer cancel()
	co, err := g.checkout(ctx, b, log)
	if co != nil {
		defer os.RemoveAll(co.work)
	}
	if err != nil {
		return nil, g.timeout(ctx, err)
	}
	_, det, err := chooseDockerfile(co, b)
	return det, err
}

func (g *Git) timeout(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return apierr.New(504, "build_timeout", "build did not finish within %s", g.Timeout).
			WithHint("speed up the build (smaller context, cached layers) or raise the server's --build-timeout")
	}
	return err
}

type checkout struct {
	work   string // temporary directory; remove when done
	repo   string // the checkout
	ctxDir string // build context inside it
}

// checkout fetches exactly b.Commit into a temporary directory.
func (g *Git) checkout(ctx context.Context, b spec.Build, log io.Writer) (*checkout, error) {
	work, err := os.MkdirTemp(g.WorkDir, "coldship-build-")
	if err != nil {
		return nil, err
	}
	co := &checkout{work: work, repo: filepath.Join(work, "src")}
	repo := co.repo

	fmt.Fprintf(log, "coldship: fetching %s at %s\n", b.Git, b.Commit)
	if out, err := g.run(ctx, "", log, g.GitBin, "init", "-q", repo); err != nil {
		return co, apierr.New(500, "git_clone_failed", "git init failed: %s", lastLine(out))
	}
	if out, err := g.run(ctx, repo, log, g.GitBin, g.gitArgs("remote", "add", "origin", b.Git)...); err != nil {
		return co, apierr.New(500, "git_clone_failed", "git remote add failed: %s", lastLine(out))
	}
	// Fast path: shallow fetch of exactly the commit. Not every server allows
	// fetching by SHA (and dumb HTTP cannot do shallow fetches), so fall back
	// to a full fetch.
	if _, err := g.run(ctx, repo, log, g.GitBin, g.gitArgs("fetch", "-q", "--depth", "1", "origin", b.Commit)...); err != nil {
		fmt.Fprintln(log, "coldship: shallow fetch by commit not supported, fetching all branches")
		if out, err := g.run(ctx, repo, log, g.GitBin, g.gitArgs("fetch", "-q", "--tags", "origin", "+refs/heads/*:refs/remotes/origin/*")...); err != nil {
			return co, unreachable(b.Git, out)
		}
	}
	if out, err := g.run(ctx, repo, log, g.GitBin, "-c", "advice.detachedHead=false", "checkout", "-q", "--detach", b.Commit, "--"); err != nil {
		return co, apierr.New(422, "git_commit_not_found", "commit %s is not in %s: %s", b.Commit, b.Git, lastLine(out)).
			WithField("build.commit").WithHint("the commit may have been force-pushed away; plan again to resolve the ref to a current commit")
	}
	if co.ctxDir, err = inside(repo, b.Context); err != nil {
		return co, apierr.Invalid("build.context", "build.context %q is not a directory inside the repository", b.Context)
	}
	return co, nil
}

// chooseDockerfile returns the Dockerfile to build with: the repository's,
// or one generated from the project (written outside the checkout, with its
// own .dockerignore next to it, so nothing is written into the repository).
func chooseDockerfile(co *checkout, b spec.Build) (string, *Detection, error) {
	explicit := b.Dockerfile != "" && b.Dockerfile != "Dockerfile"
	path, err := inside(co.ctxDir, b.Dockerfile)
	exists := false
	if err == nil {
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
			exists = true
		}
	}
	useRepo := b.Builder == spec.BuilderDockerfile || (b.Builder == "" && (exists || explicit))
	if useRepo {
		if !exists {
			return "", nil, missingDockerfile(co.repo, b)
		}
		return path, &Detection{Mode: "dockerfile", Notes: []string{"uses " + b.Dockerfile + " from the repository"}}, nil
	}
	det, err := Detect(co.ctxDir, b.Auto)
	found := findDockerfiles(co.repo)
	if err == nil && b.Builder == "" && len(found) > 0 {
		// e.g. a monorepo with services/api/Dockerfile: say so, since the
		// user may have meant to build with one of them.
		det.note("found Dockerfiles at %s but none at %s; to use one, set build.context and build.dockerfile",
			strings.Join(found, ", "), filepath.ToSlash(filepath.Join(b.Context, b.Dockerfile)))
	}
	if err != nil {
		if ae, ok := apierr.As(err); ok {
			if len(found) > 0 {
				ae.WithDetail("dockerfiles_found", found)
			}
		}
		return "", nil, err
	}
	if exists && b.Builder == spec.BuilderAuto {
		det.note("ignored the repository's Dockerfile because builder is auto")
	}
	gen := filepath.Join(co.work, "Dockerfile.coldship")
	if err := os.WriteFile(gen, []byte(det.Dockerfile), 0o600); err != nil {
		return "", nil, err
	}
	// BuildKit uses <dockerfile>.dockerignore next to the Dockerfile.
	if err := os.WriteFile(gen+".dockerignore", []byte(det.ignore), 0o600); err != nil {
		return "", nil, err
	}
	return gen, det, nil
}

func indent(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("    " + l + "\n")
	}
	return b.String()
}

// inside joins base and rel and checks that the result, after resolving
// symlinks, is still inside base. This stops a repository from pointing the
// build context or Dockerfile at files elsewhere on the host.
func inside(base, rel string) (string, error) {
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	p, err := filepath.EvalSymlinks(filepath.Join(base, rel))
	if err != nil {
		return "", err
	}
	if p != realBase && !strings.HasPrefix(p, realBase+string(filepath.Separator)) {
		return "", fmt.Errorf("%s escapes %s", rel, base)
	}
	return p, nil
}

var projectMarkers = []struct{ file, kind string }{
	{"package.json", "Node.js"}, {"go.mod", "Go"}, {"requirements.txt", "Python"}, {"pyproject.toml", "Python"},
	{"Gemfile", "Ruby"}, {"Cargo.toml", "Rust"}, {"pom.xml", "Java"}, {"build.gradle", "Java"}, {"composer.json", "PHP"},
}

func missingDockerfile(repo string, b spec.Build) error {
	e := apierr.New(422, "dockerfile_not_found", "no Dockerfile at %s in the repository",
		filepath.ToSlash(filepath.Join(b.Context, b.Dockerfile))).WithField("build.dockerfile")
	ctxDir := filepath.Join(repo, b.Context)
	for _, m := range projectMarkers {
		if _, err := os.Stat(filepath.Join(ctxDir, m.file)); err == nil {
			e.Message += fmt.Sprintf(" (this looks like a %s project: found %s)", m.kind, m.file)
			break
		}
	}
	found := findDockerfiles(repo)
	if len(found) > 0 {
		e.WithDetail("dockerfiles_found", found)
		dir, file := filepath.Split(found[0])
		fix := map[string]any{"dockerfile": file}
		if dir = strings.TrimSuffix(dir, "/"); dir != "" {
			fix["context"] = dir
		} else {
			fix["context"] = "."
		}
		e.WithFix(map[string]any{"build": fix})
		e.Hint = "point build.context and build.dockerfile at one of the Dockerfiles found"
	} else {
		e.Hint = "add the Dockerfile, or remove builder: dockerfile / build.dockerfile so Coldship detects the project and generates one"
	}
	return e
}

// findDockerfiles lists Dockerfiles in the repository, a few levels deep.
func findDockerfiles(repo string) []string {
	var found []string
	filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil || len(found) >= 10 {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(repo, p)
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || strings.Count(rel, string(filepath.Separator)) >= 3 {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "Dockerfile" || strings.HasPrefix(d.Name(), "Dockerfile.") || strings.HasSuffix(d.Name(), ".Dockerfile") {
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	})
	return found
}

func parseLsRemote(out string) map[string]string {
	refs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && spec.IsCommitSHA(sha) {
			refs[name] = sha
		}
	}
	return refs
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// LogTail is an io.Writer that keeps the last N lines written to it, each
// truncated, so build logs stay bounded in storage and in agent context.
type LogTail struct {
	mu      sync.Mutex
	max     int
	lines   []string
	partial []byte
	dropped int
}

func NewLogTail(maxLines int) *LogTail { return &LogTail{max: maxLines} }

const maxLineLen = 2000

func (t *LogTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.partial = append(t.partial, p...)
	for {
		i := bytes.IndexByte(t.partial, '\n')
		if i < 0 {
			break
		}
		t.add(string(t.partial[:i]))
		t.partial = t.partial[i+1:]
	}
	return len(p), nil
}

func (t *LogTail) add(line string) {
	line = strings.TrimRight(line, "\r")
	if len(line) > maxLineLen {
		line = line[:maxLineLen] + "…"
	}
	t.lines = append(t.lines, line)
	if len(t.lines) > t.max {
		t.lines = t.lines[1:]
		t.dropped++
	}
}

// Lines returns the kept lines, prefixed by a note if earlier lines were dropped.
func (t *LogTail) Lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := []string{}
	if t.dropped > 0 {
		out = append(out, fmt.Sprintf("coldship: %d earlier lines omitted", t.dropped))
	}
	out = append(out, t.lines...)
	if len(t.partial) > 0 {
		out = append(out, string(t.partial))
	}
	return out
}

// copySite copies a static site's regular files from src to dst. It skips
// what a build would ignore (.git, .env files, .dockerignore entries), since
// every copied file becomes publicly downloadable, and it skips symlinks,
// which could point outside the repository.
func copySite(src, dst, ignore string) (int, error) {
	var patterns []string
	for _, l := range strings.Split(ignore, "\n") {
		l = strings.Trim(strings.TrimSpace(l), "/")
		if l != "" && !strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "!") {
			patterns = append(patterns, l)
		}
	}
	ignored := func(rel string) bool {
		for _, p := range patterns {
			if ok, _ := path.Match(p, rel); ok {
				return true
			}
			if ok, _ := path.Match(p, path.Base(rel)); ok {
				return true
			}
		}
		return false
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, err
	}
	n := 0
	err := filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if ignored(rel) {
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		switch {
		case e.IsDir():
			return os.MkdirAll(target, 0o755)
		case e.Type().IsRegular():
			n++
			return copyFile(p, target)
		default: // symlinks, sockets, devices
			return nil
		}
	})
	return n, err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
