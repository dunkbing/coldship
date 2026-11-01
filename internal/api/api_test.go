package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"hangar/internal/api"
	"hangar/internal/apierr"
	"hangar/internal/client"
	"hangar/internal/engine"
	"hangar/internal/runtime"
	"hangar/internal/spec"
	"hangar/internal/store"
)

// End to end through HTTP: scoped tokens, plan/apply, and agent-blocked approval.
func TestAPIEndToEnd(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := engine.New(st, runtime.NewFake())
	srv := httptest.NewServer(api.New(eng, st, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()

	adminTok, err := eng.EnsureAdminToken(ctx)
	if err != nil || adminTok == "" {
		t.Fatal("expected bootstrap admin token", err)
	}
	admin := client.New(srv.URL, adminTok)
	tr, err := admin.CreateToken(ctx, "claude", "deploy")
	if err != nil {
		t.Fatal(err)
	}
	agent := client.New(srv.URL, tr.Token)

	if _, err := client.New(srv.URL, "nope").ListApps(ctx); errCode(err) != "unauthenticated" {
		t.Fatalf("want unauthenticated, got %v", err)
	}

	db := &spec.App{Name: "db", Stage: spec.StageProduction, Image: "postgres:16", Volumes: []spec.Volume{{Name: "data", Path: "/var/lib/postgresql/data"}}}
	p, err := agent.Plan(ctx, engine.PlanRequest{Spec: db})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Apply(ctx, p.ID, "create db"); err != nil {
		t.Fatal(err)
	}

	p, err = agent.Plan(ctx, engine.PlanRequest{Delete: "db"})
	if err != nil || !p.RequiresApproval {
		t.Fatalf("prod delete must need approval: %+v %v", p, err)
	}
	if _, err := agent.Approve(ctx, p.ID); errCode(err) != "insufficient_scope" {
		t.Fatalf("agent must not self-approve, got %v", err)
	}
	if _, err := agent.Apply(ctx, p.ID, "delete"); errCode(err) != "approval_required" {
		t.Fatalf("want approval_required, got %v", err)
	}
	if _, err := admin.Approve(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Apply(ctx, p.ID, "delete"); err != nil {
		t.Fatal(err)
	}

	entries, err := agent.Audit(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	actors := map[string]bool{}
	for _, e := range entries {
		actors[e.Actor+":"+e.Action] = true
	}
	for _, want := range []string{"claude:plan.apply", "admin:plan.approve", "system:token.create", "admin:token.create"} {
		if !actors[want] {
			t.Errorf("audit missing %s; have %v", want, actors)
		}
	}
}

func errCode(err error) string {
	if ae, ok := apierr.As(err); ok {
		return ae.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}
