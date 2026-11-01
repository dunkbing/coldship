package builder

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"hangar/internal/apierr"
	"hangar/internal/spec"
)

// These tests use real git against local repositories. Docker is replaced by
// a stub script that records its arguments.

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo creates a repository with the given files committed on main and
// returns its path.
func newRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	for name, body := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func testBuilder(t *testing.T) (*Git, string) {
	t.Helper()
	stubDir := t.TempDir()
	argsFile := filepath.Join(stubDir, "args")
	stub := filepath.Join(stubDir, "docker")
	script := "#!/bin/sh\nif [ \"$1\" = image ]; then exit 1; fi\nprintf '%s\\n' \"$@\" > " + argsFile + "\necho 'step 1/1 : FROM scratch'\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g := NewGit()
	g.DockerBin = stub
	g.AllowedProtocols = "file" // local test repos; production allows only http, https and ssh
	g.WorkDir = t.TempDir()
	return g, argsFile
}

func code(err error) string {
	if ae, ok := apierr.As(err); ok {
		return ae.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestResolve(t *testing.T) {
	repo := newRepo(t, map[string]string{"Dockerfile": "FROM scratch\n"})
	first := gitRun(t, repo, "rev-parse", "HEAD")
	gitRun(t, repo, "tag", "-a", "v1", "-m", "v1")
	os.WriteFile(filepath.Join(repo, "x"), []byte("x"), 0o644)
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "-q", "-m", "second")
	second := gitRun(t, repo, "rev-parse", "HEAD")
	g, _ := testBuilder(t)
	ctx := context.Background()

	cases := map[string]string{"": second, "main": second, "v1": first, first: first}
	for ref, want := range cases {
		got, err := g.Resolve(ctx, spec.Build{Git: repo, Ref: ref})
		if err != nil || got != want {
			t.Errorf("ref %q: got %q, %v; want %q", ref, got, err, want)
		}
	}

	_, err := g.Resolve(ctx, spec.Build{Git: repo, Ref: "nope"})
	ae, _ := apierr.As(err)
	if code(err) != "git_ref_not_found" || ae.Details["available_refs"] == nil {
		t.Fatalf("want git_ref_not_found with available refs, got %v", err)
	}
	refs := ae.Details["available_refs"].([]string)
	if strings.Join(refs, ",") != "main,v1" {
		t.Errorf("available refs = %v", refs)
	}

	if _, err := g.Resolve(ctx, spec.Build{Git: filepath.Join(repo, "missing")}); code(err) != "git_unreachable" {
		t.Errorf("want git_unreachable, got %v", err)
	}
}

func TestBlockedProtocols(t *testing.T) {
	repo := newRepo(t, map[string]string{"Dockerfile": "FROM scratch\n"})
	g := NewGit() // default: http, https, ssh only
	if _, err := g.Resolve(context.Background(), spec.Build{Git: repo, Ref: "main"}); err == nil {
		t.Fatal("local repositories must be refused by the default protocol allowlist")
	}
	if _, err := g.Resolve(context.Background(), spec.Build{Git: "ext::sh -c touch% /tmp/pwned", Ref: "main"}); err == nil {
		t.Fatal("ext:: transport must be refused")
	}
}

func TestBuild(t *testing.T) {
	repo := newRepo(t, map[string]string{
		"services/api/Dockerfile": "FROM scratch\n",
		"services/api/main.go":    "package main\n",
	})
	commit := gitRun(t, repo, "rev-parse", "HEAD")
	g, argsFile := testBuilder(t)
	b := spec.Build{Git: repo, Commit: commit, Context: "services/api", Dockerfile: "Dockerfile", Args: map[string]string{"VERSION": "1.2"}}
	log := NewLogTail(100)

	tag, err := g.Build(context.Background(), "api", b, log)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, strings.Join(log.Lines(), "\n"))
	}
	if tag != ImageTag("api", b) || !strings.HasPrefix(tag, "hangar-build/api:"+commit[:12]+"-") {
		t.Errorf("unexpected tag %s", tag)
	}
	raw, _ := os.ReadFile(argsFile)
	args := string(raw)
	for _, want := range []string{"--build-arg\nVERSION=1.2", "hangar.commit=" + commit, "-t\n" + tag, "services/api/Dockerfile"} {
		if !strings.Contains(args, want) {
			t.Errorf("docker args missing %q:\n%s", want, args)
		}
	}
	if !strings.Contains(strings.Join(log.Lines(), "\n"), "step 1/1") {
		t.Error("docker output should be in the build log")
	}
	entries, _ := os.ReadDir(g.WorkDir)
	if len(entries) != 0 {
		t.Error("build checkout should be removed")
	}
}

func TestBuildMissingDockerfileSuggestsOne(t *testing.T) {
	repo := newRepo(t, map[string]string{"package.json": "{}", "deploy/web.Dockerfile": "FROM scratch\n"})
	commit := gitRun(t, repo, "rev-parse", "HEAD")
	g, _ := testBuilder(t)
	_, err := g.Build(context.Background(), "web", spec.Build{Git: repo, Commit: commit, Context: ".", Dockerfile: "Dockerfile"}, NewLogTail(10))
	ae, _ := apierr.As(err)
	if code(err) != "dockerfile_not_found" || !strings.Contains(ae.Message, "Node.js") {
		t.Fatalf("want dockerfile_not_found mentioning Node.js, got %v", err)
	}
	fix := ae.SuggestedFix.(map[string]any)["build"].(map[string]any)
	if fix["context"] != "deploy" || fix["dockerfile"] != "web.Dockerfile" {
		t.Errorf("suggested fix = %v", fix)
	}
}

func TestBuildRejectsSymlinkEscape(t *testing.T) {
	repo := newRepo(t, map[string]string{"README": "x"})
	if err := os.Symlink("/etc", filepath.Join(repo, "ctx")); err != nil {
		t.Skip(err)
	}
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "-q", "-m", "symlink")
	commit := gitRun(t, repo, "rev-parse", "HEAD")
	g, argsFile := testBuilder(t)
	_, err := g.Build(context.Background(), "x", spec.Build{Git: repo, Commit: commit, Context: "ctx", Dockerfile: "Dockerfile"}, NewLogTail(10))
	if code(err) != "invalid_spec" {
		t.Fatalf("a context symlinked outside the repo must be refused, got %v", err)
	}
	if _, err := os.Stat(argsFile); err == nil {
		t.Fatal("docker build must not run")
	}
}

func TestBuildUnknownCommit(t *testing.T) {
	repo := newRepo(t, map[string]string{"Dockerfile": "FROM scratch\n"})
	g, _ := testBuilder(t)
	_, err := g.Build(context.Background(), "x", spec.Build{Git: repo, Commit: strings.Repeat("a", 40), Context: ".", Dockerfile: "Dockerfile"}, NewLogTail(10))
	if code(err) != "git_commit_not_found" {
		t.Fatalf("want git_commit_not_found, got %v", err)
	}
}

func TestLogTail(t *testing.T) {
	l := NewLogTail(3)
	l.Write([]byte("a\nb\nc\nd\npartial"))
	got := strings.Join(l.Lines(), "|")
	if got != "hangar: 1 earlier lines omitted|b|c|d|partial" {
		t.Errorf("got %q", got)
	}
}
