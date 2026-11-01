package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/dunkbing/coldship/internal/builder"
	"github.com/dunkbing/coldship/internal/spec"
)

func applyPatch(t *testing.T, e *Engine, p *spec.Patch) *spec.App {
	t.Helper()
	ctx := context.Background()
	plan, err := e.Plan(ctx, "human", PlanRequest{Patch: p})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "noop" {
		if _, err := e.Apply(ctx, "human", plan.ID, "patch"); err != nil {
			t.Fatal(err)
		}
	}
	a, err := e.st.GetApp(ctx, p.Name)
	if err != nil || a == nil {
		t.Fatal(a, err)
	}
	return a.Spec
}

// Flags like --env change one thing and keep the rest, secrets included.
func TestPatchKeepsTheRest(t *testing.T) {
	e, rt := dbSetup(t)
	ctx := context.Background()

	if _, err := e.Plan(ctx, "human", PlanRequest{Patch: &spec.Patch{Name: "web", Port: 80}}); code(err) != "invalid_spec" {
		t.Fatalf("a new app needs a source: got %v", err)
	}
	s := applyPatch(t, e, &spec.Patch{Name: "web", Image: "nginx:1", Port: 80,
		Env: map[string]string{"SECRET": "s3cret", "MODE": "a"}, AddDatabases: []spec.Database{{Engine: spec.EnginePostgres}}})
	if s.Image != "nginx:1" || len(s.Databases) != 1 || s.Databases[0].Name != "main" || s.Stage != spec.StageStaging {
		t.Fatalf("created: %+v", s)
	}

	s = applyPatch(t, e, &spec.Patch{Name: "web", Env: map[string]string{"MODE": "b"}, AddDomains: []string{"web.example.com"}})
	if s.Env["SECRET"] != "s3cret" || s.Env["MODE"] != "b" || s.Image != "nginx:1" || len(s.Databases) != 1 || s.Port != 80 {
		t.Fatalf("a patch must keep what it doesn't mention: %+v", s)
	}
	if rt.Containers["web"].Env["SECRET"] != "s3cret" {
		t.Fatal("secret lost from the running container")
	}

	s = applyPatch(t, e, &spec.Patch{Name: "web", UnsetEnv: []string{"MODE"}, RemoveDomains: []string{"web.example.com"}})
	if _, ok := s.Env["MODE"]; ok || len(s.Domains) != 0 {
		t.Fatalf("unset and remove: %+v", s)
	}

	// A spec copied from get_app or export keeps redacted values.
	cp := s.Redacted()
	cp.Image = "nginx:2"
	p, err := e.Plan(ctx, "human", PlanRequest{Spec: cp})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := e.st.GetPlan(ctx, p.ID)
	if err != nil || stored.Desired.Env["SECRET"] != "s3cret" {
		t.Fatalf("redacted value must mean keep: %v %v", stored, err)
	}
	fresh := &spec.App{Name: "other", Image: "x:1", Env: map[string]string{"K": spec.RedactedValue}}
	if _, err := e.Plan(ctx, "human", PlanRequest{Spec: fresh}); code(err) != "invalid_spec" {
		t.Fatalf("redacted value with nothing to keep: got %v", err)
	}
}

func TestPatchDetectsPortForGitApps(t *testing.T) {
	e, _ := dbSetup(t)
	e.bd.(*builder.Fake).Refs["https://github.com/acme/site.git@main"] = strings.Repeat("b", 40)
	s := applyPatch(t, e, &spec.Patch{Name: "site", Build: &spec.Build{Git: "https://github.com/acme/site.git", Ref: "main", Builder: spec.BuilderAuto},
		AddDomains: []string{"site.example.com"}})
	if s.Port != 3000 || s.Build.Commit == "" {
		t.Fatalf("port should come from detection and the commit be pinned: %+v %+v", s, s.Build)
	}
	// Switching to an image drops the build.
	s = applyPatch(t, e, &spec.Patch{Name: "site", Image: "nginx:1"})
	if s.Build != nil || s.Image != "nginx:1" || s.Port != 3000 {
		t.Fatalf("switch to image: %+v", s)
	}
}

func TestPatchDetectsPortFromImage(t *testing.T) {
	e, rt := dbSetup(t)
	rt.Ports["nginx:1.27-alpine"] = []int{80}
	rt.Ports["multi:1"] = []int{80, 443}
	s := applyPatch(t, e, &spec.Patch{Name: "web", Image: "nginx:1.27-alpine"})
	if s.Port != 80 {
		t.Fatalf("one declared port is the app's port: %d", s.Port)
	}
	s = applyPatch(t, e, &spec.Patch{Name: "multi", Image: "multi:1"})
	if s.Port != 0 {
		t.Fatalf("several ports: leave it to the user, got %d", s.Port)
	}
	s = applyPatch(t, e, &spec.Patch{Name: "web2", Image: "nginx:1.27-alpine", Port: 8080})
	if s.Port != 8080 {
		t.Fatal("an explicit port wins")
	}
}
