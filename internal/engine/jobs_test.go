package engine

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dunkbing/coldship/internal/builder"
	"github.com/dunkbing/coldship/internal/planner"
	"github.com/dunkbing/coldship/internal/proxy"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

func waitUntil(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestJobs(t *testing.T) {
	e, rt, box, ctx := notifySetup(t)
	if _, err := e.SaveChannel(ctx, "admin", "", ChannelRequest{Kind: "webhook", URL: "https://ops.example.com/hook",
		Events: []string{EvJobFailed, EvJobRecovered}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 10, 0, 30, 0, time.UTC)
	e.Now = func() time.Time { return now }

	bad := []spec.Job{{Name: "x", Schedule: "every day", Command: "true"}}
	if _, err := e.Plan(ctx, "agent", PlanRequest{Spec: &spec.App{Name: "web", Image: "web:1", Jobs: bad}}); code(err) != "invalid_spec" {
		t.Fatalf("bad schedule: %v", err)
	}
	app := &spec.App{Name: "web", Image: "web:1", Port: 80, Env: map[string]string{"A": "1"}, Memory: "256m",
		Volumes: []spec.Volume{{Name: "files", Path: "/files"}}, Databases: []spec.Database{pg},
		Jobs: []spec.Job{{Name: "cleanup", Schedule: "*/5 * * * *", Command: "echo hi"}}}
	p, err := e.Plan(ctx, "agent", PlanRequest{Spec: app})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, "agent", p.ID, "ship"); err != nil {
		t.Fatal(err)
	}
	deploys := rt.Deploys

	// Changing only jobs doesn't redeploy.
	app2 := app.Clone()
	app2.Jobs = append(app2.Jobs, spec.Job{Name: "report", Schedule: "@daily", Command: "exit 3", Timeout: "1m"})
	p, err = e.Plan(ctx, "agent", PlanRequest{Spec: app2})
	if err != nil || p.Action != planner.OpConfig || p.Downtime || len(p.Changes) != 1 || p.Changes[0].Path != "jobs.report" {
		t.Fatalf("plan %+v %v", p, err)
	}
	res, err := e.Apply(ctx, "agent", p.ID, "add a job")
	if err != nil || res.Deployment != nil || rt.Deploys != deploys {
		t.Fatalf("apply %+v %v (deploys %d→%d)", res, err, deploys, rt.Deploys)
	}

	// The first pass only learns the schedule: missed runs aren't caught up.
	log := discardLog()
	e.scheduleJobs(ctx, log)
	if n := len(rt.JobRuns()); n != 0 {
		t.Fatalf("%d runs on the first pass", n)
	}
	views, _ := e.Jobs(ctx, "web")
	if len(views) != 2 || views[0].Next == nil || !views[0].Next.Equal(time.Date(2026, 11, 2, 10, 5, 0, 0, time.UTC)) {
		t.Fatalf("views %+v", views)
	}
	now = now.Add(5 * time.Minute)
	e.scheduleJobs(ctx, log)
	waitUntil(t, "cleanup ran", func() bool {
		r, _ := e.JobRuns(ctx, "web", "cleanup")
		return len(r) == 1 && r[0].Status == store.JobSucceeded
	})
	js := rt.JobRuns()[0]
	env := strings.Join(js.Env, " ")
	if js.Image != "web:1" || js.Command != "echo hi" || js.Memory != "256m" || js.Timeout != spec.DefaultJobTimeout ||
		!strings.Contains(env, "A=1") || !strings.Contains(env, "DATABASE_URL=postgres://") || strings.Join(js.Binds, ",") != "coldship-web-files:/files" {
		t.Fatalf("job spec %+v", js)
	}
	runs, _ := e.JobRuns(ctx, "web", "cleanup")
	if r, _ := e.JobRun(ctx, "web", runs[0].ID); r == nil || !strings.Contains(r.Log, "fake: ran echo hi") || r.Trigger != "schedule" {
		t.Fatalf("run %+v", r)
	}
	if _, err := e.JobRun(ctx, "api", runs[0].ID); err == nil {
		t.Fatal("a run read through another app")
	}

	// A run still going isn't overlapped: by hand it's refused, by
	// schedule it's skipped.
	release := make(chan struct{})
	rt.Job = func(ctx context.Context, s runtime.JobSpec, _ io.Writer) (int, error) {
		if s.Job == "report" {
			return 3, nil
		}
		<-release
		return 0, nil
	}
	r, err := e.RunJob(ctx, "dana", "web", "cleanup")
	if err != nil || r.Trigger != "dana" || r.Status != store.JobRunning {
		t.Fatalf("run %+v %v", r, err)
	}
	if !e.Busy() {
		t.Fatal("not busy while a job runs")
	}
	if _, err := e.RunJob(ctx, "dana", "web", "cleanup"); code(err) != "job_running" {
		t.Fatalf("overlap: %v", err)
	}
	now = now.Add(5 * time.Minute)
	e.scheduleJobs(ctx, log)
	runs, _ = e.JobRuns(ctx, "web", "cleanup")
	if runs[0].Status != store.JobSkipped {
		t.Fatalf("runs %+v", runs)
	}
	close(release)
	waitUntil(t, "idle", func() bool { return e.JobsRunning() == 0 })

	// Failures notify once, then once more when it works again.
	for range 2 {
		if _, err := e.RunJob(ctx, "dana", "web", "report"); err != nil {
			t.Fatal(err)
		}
		waitUntil(t, "report done", func() bool { return e.JobsRunning() == 0 })
	}
	box.wait(t, 1)
	_, body := box.last()
	var ev Event
	json.Unmarshal([]byte(body), &ev)
	if ev.Kind != EvJobFailed || ev.App != "web" || ev.Actor != "dana" || !strings.Contains(ev.Detail, "exited with code 3") {
		t.Fatalf("event %+v", ev)
	}
	box.none(t)
	rt.Job = func(context.Context, runtime.JobSpec, io.Writer) (int, error) { return 0, nil }
	e.RunJob(ctx, "dana", "web", "report")
	box.wait(t, 1)
	_, body = box.last()
	json.Unmarshal([]byte(body), &ev)
	if ev.Kind != EvJobRecovered {
		t.Fatalf("event %+v", ev)
	}

	// A timeout is its own status.
	rt.Job = func(context.Context, runtime.JobSpec, io.Writer) (int, error) { return -1, runtime.ErrJobTimeout }
	e.RunJob(ctx, "dana", "web", "cleanup")
	waitUntil(t, "timeout", func() bool {
		r, _ := e.JobRuns(ctx, "web", "cleanup")
		return r[0].Status == store.JobTimedOut
	})
	waitUntil(t, "idle", func() bool { return e.JobsRunning() == 0 })

	if _, err := e.RunJob(ctx, "dana", "web", "nope"); code(err) != "job_not_found" {
		t.Fatalf("unknown job: %v", err)
	}

	// Deleting the app stops its jobs and forgets their runs.
	p, err = e.Plan(ctx, "agent", PlanRequest{Delete: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, "agent", p.ID, "bye"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(rt.RemovedJobs, ","), "web") {
		t.Fatalf("removed jobs %v", rt.RemovedJobs)
	}
	if r, _ := e.JobRuns(ctx, "web", ""); len(r) != 0 {
		t.Fatalf("runs left: %d", len(r))
	}
}

func TestJobsAfterRestart(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), testKeys)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rt := runtime.NewFake()
	e := New(st, rt, builder.NewFake(), &proxy.Fake{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st.SaveJobRun(ctx, &store.JobRun{ID: "run_left", Org: "default", App: "web", Job: "x", Trigger: "schedule", Status: store.JobRunning, StartedAt: time.Now()})
	go e.RunJobs(ctx, discardLog())
	waitUntil(t, "interrupted", func() bool {
		r, _ := st.GetJobRun(ctx, "run_left")
		return r.Status == store.JobInterrupted && r.FinishedAt != nil
	})
	waitUntil(t, "leftovers removed", func() bool {
		rt.RunJob(ctx, runtime.JobSpec{}, io.Discard) // sync on the fake's lock
		return len(rt.RemovedJobs) > 0 && rt.RemovedJobs[0] == ""
	})
}

// A jobs-only change to a Git app neither rebuilds nor moves its commit.
func TestJobsOnGitApp(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), testKeys)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rt, bd := runtime.NewFake(), builder.NewFake()
	e := New(st, rt, bd, &proxy.Fake{})
	ctx := context.Background()
	shaA, shaB := strings.Repeat("a", 40), strings.Repeat("b", 40)
	bd.Refs["https://github.com/acme/api.git@main"] = shaA
	p, err := e.Plan(ctx, "agent", PlanRequest{Spec: gitApp("main")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, "agent", p.ID, "ship"); err != nil {
		t.Fatal(err)
	}
	builds, deploys := bd.Builds, rt.Deploys
	bd.Refs["https://github.com/acme/api.git@main"] = shaB // the branch moved
	p, err = e.Plan(ctx, "agent", PlanRequest{Patch: &spec.Patch{Name: "api", SetJobs: []spec.Job{{Name: "sync", Schedule: "@hourly", Command: "./sync"}}}})
	if err != nil || p.Action != planner.OpConfig || p.Desired.Build.Commit != shaA {
		t.Fatalf("plan %+v %v", p, err)
	}
	if _, err := e.Apply(ctx, "agent", p.ID, "add sync"); err != nil || bd.Builds != builds || rt.Deploys != deploys {
		t.Fatalf("apply: %v, builds %d→%d deploys %d→%d", err, builds, bd.Builds, deploys, rt.Deploys)
	}
	a, _ := st.GetApp(ctx, "api")
	if len(a.Spec.Jobs) != 1 {
		t.Fatalf("spec %+v", a.Spec)
	}
	// Jobs run the built image.
	if _, err := e.RunJob(ctx, "dana", "api", "sync"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "ran", func() bool { return len(rt.JobRuns()) == 1 })
	d, _ := st.GetDeployment(ctx, a.CurrentDeployment)
	if img := rt.JobRuns()[0].Image; img != d.Image || img == "" {
		t.Fatalf("job image %q, deployed %q", img, d.Image)
	}
	p, err = e.Plan(ctx, "agent", PlanRequest{Patch: &spec.Patch{Name: "api", RemoveJobs: []string{"nope"}}})
	if err == nil {
		t.Fatal("removing a missing job")
	}
}
