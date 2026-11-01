package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dunkbing/coldship/internal/spec"
)

func TestDeployFlagsInferFromGitRepo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My Shop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "feature")
	git("commit", "-q", "--allow-empty", "-m", "x")
	git("remote", "add", "origin", "git@github.com:acme/shop.git")
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	cmd := deployCmd()
	if err := cmd.ParseFlags([]string{"--env", "A=1=2", "--db", "postgres", "--db", "redis:cache", "--domain", "shop.example.com", "--production"}); err != nil {
		t.Fatal(err)
	}
	var f deployFlags
	f.env, f.dbs, f.domains, f.production = []string{"A=1=2"}, []string{"postgres", "redis:cache"}, []string{"shop.example.com"}, true
	p, err := f.patch(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "my-shop" || p.Build == nil || p.Build.Git != "https://github.com/acme/shop.git" || p.Build.Ref != "feature" {
		t.Fatalf("inferred from the repository: %+v %+v", p, p.Build)
	}
	if p.Env["A"] != "1=2" || len(p.AddDatabases) != 2 || p.AddDatabases[1].Name != "cache" || p.AddDatabases[1].Engine != spec.EngineRedis ||
		p.Stage != spec.StageProduction || p.AddDomains[0] != "shop.example.com" {
		t.Fatalf("flags: %+v", p)
	}

	// With --name, the folder is not the app: no source is inferred.
	g := deployFlags{name: "api", env: []string{"X=1"}}
	p, err = g.patch(cmd)
	if err != nil || p.Build != nil || p.Image != "" || p.Name != "api" {
		t.Fatalf("explicit name: %+v %v", p, err)
	}
	if _, err := (&deployFlags{name: "api", env: []string{"novalue"}}).patch(cmd); err == nil {
		t.Fatal("--env without = must fail")
	}
}

func TestEngineHostFromUnit(t *testing.T) {
	unit := "[Service]\nEnvironment=DOCKER_HOST=unix:///run/podman/podman.sock\nExecStart=/usr/local/bin/coldship server --data /var/lib/coldship/coldship.db\n"
	if m := engineHostRe.FindStringSubmatch(unit); m == nil || m[1] != "unix:///run/podman/podman.sock" {
		t.Fatalf("engine host: %v", m)
	}
	if m := dataFlagRe.FindStringSubmatch(unit); m == nil || m[1] != "/var/lib/coldship/coldship.db" {
		t.Fatalf("data: %v", m)
	}
}
