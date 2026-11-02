package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/dunkbing/coldship/internal/api"
	"github.com/dunkbing/coldship/internal/builder"
	"github.com/dunkbing/coldship/internal/client"
	"github.com/dunkbing/coldship/internal/engine"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

func TestJobsAPI(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), testKeys)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := engine.New(st, runtime.NewFake(), builder.NewFake(), nil)
	srv := httptest.NewServer(api.New(eng, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	defer srv.Close()
	adminTok, _ := eng.EnsureAdminToken(ctx)
	admin := client.New(srv.URL, adminTok)
	vt, _ := admin.CreateToken(ctx, "viewer", "read")
	viewer := client.New(srv.URL, vt.Token)
	lt, _ := admin.CreateToken(ctx, "limited", "deploy", "other*")
	limited := client.New(srv.URL, lt.Token)

	p := must(admin.Plan(ctx, engine.PlanRequest{Spec: &spec.App{Name: "web", Image: "web:1",
		Jobs: []spec.Job{{Name: "cleanup", Schedule: "0 3 * * *", Command: "echo hi"}}}}))
	if _, err := admin.Apply(ctx, p.ID, "ship"); err != nil {
		t.Fatal(err)
	}
	jobs, err := viewer.Jobs(ctx, "web")
	if err != nil || len(jobs) != 1 || jobs[0].Next == nil || jobs[0].Next.Hour() != 3 {
		t.Fatalf("jobs %+v %v", jobs, err)
	}
	if _, err := viewer.RunJob(ctx, "web", "cleanup"); errCode(err) != "insufficient_scope" {
		t.Fatalf("a viewer ran a job: %v", err)
	}
	if _, err := limited.Jobs(ctx, "web"); errCode(err) != "app_not_allowed" {
		t.Fatalf("limited token saw another app's jobs: %v", err)
	}
	r, err := admin.RunJob(ctx, "web", "cleanup")
	if err != nil || r.Status != store.JobRunning || r.Trigger != "admin" {
		t.Fatalf("run %+v %v", r, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := viewer.JobRun(ctx, "web", r.ID)
		if err == nil && got.Status == store.JobSucceeded && got.Log != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %+v %v", got, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if runs, err := viewer.JobRuns(ctx, "web", "cleanup"); err != nil || len(runs) != 1 || runs[0].Log != "" {
		t.Fatalf("runs %+v %v", runs, err)
	}
}
