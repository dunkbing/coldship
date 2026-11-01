package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hangar/internal/apierr"
	"hangar/internal/builder"
	"hangar/internal/proxy"
	"hangar/internal/runtime"
	"hangar/internal/spec"
	"hangar/internal/store"
)

func setup(t *testing.T) (*Engine, *runtime.Fake) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rt := runtime.NewFake()
	return New(st, rt, builder.NewFake(), &proxy.Fake{}), rt
}

func web(stage spec.Stage, image string, vols ...string) *spec.App {
	a := &spec.App{Name: "web", Stage: stage, Image: image, Port: 80, Env: map[string]string{"SECRET": "hunter2"}}
	for _, v := range vols {
		a.Volumes = append(a.Volumes, spec.Volume{Name: v, Path: "/" + v})
	}
	return a
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

func deploy(t *testing.T, e *Engine, a *spec.App) *ApplyResult {
	t.Helper()
	ctx := context.Background()
	p, err := e.Plan(ctx, "agent", PlanRequest{Spec: a})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Apply(ctx, "agent", p.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestPlanApplyAndIdempotency(t *testing.T) {
	e, rt := setup(t)
	ctx := context.Background()
	p, err := e.Plan(ctx, "agent", PlanRequest{Spec: web(spec.StageStaging, "nginx:1")})
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != "create" || p.Desired.Env["SECRET"] != spec.RedactedValue {
		t.Fatalf("unexpected plan: action=%s env=%v", p.Action, p.Desired.Env)
	}
	if _, err := e.Apply(ctx, "agent", p.ID, ""); code(err) != "reason_required" {
		t.Fatalf("want reason_required, got %v", err)
	}
	r1, err := e.Apply(ctx, "agent", p.ID, "first deploy")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := e.Apply(ctx, "agent", p.ID, "retry")
	if err != nil || r2.Deployment.ID != r1.Deployment.ID || rt.Deploys != 1 {
		t.Fatalf("re-apply should be idempotent: err=%v deploys=%d", err, rt.Deploys)
	}
	if rt.Containers["web"].Env["SECRET"] != "hunter2" {
		t.Fatal("runtime must receive the real secret")
	}
	p2, _ := e.Plan(ctx, "agent", PlanRequest{Spec: web(spec.StageStaging, "nginx:1")})
	if p2.Action != "noop" {
		t.Fatalf("want noop, got %s", p2.Action)
	}
}

func TestStalePlan(t *testing.T) {
	e, _ := setup(t)
	ctx := context.Background()
	deploy(t, e, web(spec.StageStaging, "nginx:1"))
	a, _ := e.Plan(ctx, "agent", PlanRequest{Spec: web(spec.StageStaging, "nginx:2")})
	deploy(t, e, web(spec.StageStaging, "nginx:3"))
	if _, err := e.Apply(ctx, "agent", a.ID, "x"); code(err) != "plan_stale" {
		t.Fatalf("want plan_stale, got %v", err)
	}
}

func TestExpiredPlan(t *testing.T) {
	e, _ := setup(t)
	ctx := context.Background()
	p, _ := e.Plan(ctx, "agent", PlanRequest{Spec: web(spec.StageStaging, "nginx:1")})
	e.Now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, err := e.Apply(ctx, "agent", p.ID, "x"); code(err) != "plan_expired" {
		t.Fatalf("want plan_expired, got %v", err)
	}
}

func TestProductionDestructiveNeedsApproval(t *testing.T) {
	e, rt := setup(t)
	ctx := context.Background()
	deploy(t, e, web(spec.StageProduction, "postgres:16", "data"))

	// Non-destructive production change: no approval needed.
	p, _ := e.Plan(ctx, "agent", PlanRequest{Spec: web(spec.StageProduction, "postgres:16.1", "data")})
	if p.RequiresApproval || p.Destructive {
		t.Fatal("image bump should not need approval")
	}

	// Dropping the volume is destructive.
	p, _ = e.Plan(ctx, "agent", PlanRequest{Spec: web(spec.StageProduction, "postgres:16")})
	if !p.Destructive || !p.RequiresApproval {
		t.Fatalf("volume removal on prod must need approval: %+v", p)
	}
	if _, err := e.Apply(ctx, "agent", p.ID, "cleanup"); code(err) != "approval_required" {
		t.Fatalf("want approval_required, got %v", err)
	}
	if !rt.Volumes["web/data"] {
		t.Fatal("volume must survive a refused apply")
	}
	if _, err := e.Approve(ctx, "alice", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, "agent", p.ID, "cleanup"); err != nil {
		t.Fatal(err)
	}
	if rt.Volumes["web/data"] {
		t.Fatal("volume should be deleted after approved apply")
	}

	// Demoting prod to staging while deleting a volume still needs approval.
	deploy(t, e, web(spec.StageProduction, "postgres:16", "data"))
	p, _ = e.Plan(ctx, "agent", PlanRequest{Spec: web(spec.StageStaging, "postgres:16")})
	if !p.RequiresApproval {
		t.Fatal("stage demotion must not bypass approval")
	}
}

// recreated is an app with a host port, so it deploys by stop-and-replace and
// a failed deploy takes the old container down with it.
func recreated(image string) *spec.App {
	a := web(spec.StageStaging, image)
	a.HostPort = 8080
	return a
}

func TestFailedDeployAndRollback(t *testing.T) {
	e, rt := setup(t)
	ctx := context.Background()
	first := deploy(t, e, recreated("app:1"))
	deploy(t, e, recreated("app:2"))

	// A failed deploy keeps the last good spec as current.
	rt.FailNext = apierr.New(502, "container_exited", "boom")
	p, _ := e.Plan(ctx, "agent", PlanRequest{Spec: recreated("app:3")})
	_, err := e.Apply(ctx, "agent", p.ID, "upgrade")
	if code(err) != "container_exited" {
		t.Fatalf("want container_exited, got %v", err)
	}
	ae, _ := apierr.As(err)
	if ae.Details["deployment"] == nil || ae.SuggestedFix == nil {
		t.Fatal("failure should point at the deployment and suggest a fix")
	}
	diag, err := e.Diagnose(ctx, "web")
	if err != nil || diag.Healthy || diag.Findings[0].Code != "last_deploy_failed" {
		t.Fatalf("diagnose should flag the failed deploy: %+v %v", diag, err)
	}

	// Diagnose checks logs against the failed deployment's spec, not the last good one.
	rt.LogLines["web"] = []string{"listening on port 9000"}
	diag, _ = e.Diagnose(ctx, "web")
	if !hasFinding(diag, "port_mismatch") {
		t.Fatalf("want port_mismatch vs failed spec port 80, got %+v", diag.Findings)
	}

	// Rollback after a failure restores the current good deployment (app:2).
	if _, err := e.Rollback(ctx, "agent", "web", "", "fix outage"); err != nil {
		t.Fatal(err)
	}
	if rt.Containers["web"].Image != "app:2" {
		t.Fatalf("want app:2 after rollback, got %s", rt.Containers["web"].Image)
	}
	// Rollback when healthy goes to the previous good deployment.
	if _, err := e.Rollback(ctx, "agent", "web", first.Deployment.ID, "go back to v1"); err != nil {
		t.Fatal(err)
	}
	if rt.Containers["web"].Image != "app:1" {
		t.Fatalf("want app:1, got %s", rt.Containers["web"].Image)
	}
}

func TestDeleteApp(t *testing.T) {
	e, rt := setup(t)
	ctx := context.Background()
	deploy(t, e, web(spec.StageStaging, "nginx:1", "data"))
	p, err := e.Plan(ctx, "agent", PlanRequest{Delete: "web"})
	if err != nil || !p.Destructive || p.RequiresApproval {
		t.Fatalf("staging delete: destructive but no approval; got %+v %v", p, err)
	}
	if !strings.Contains(p.Changes[0].Reason, "data") {
		t.Fatalf("delete reason should name the volume: %q", p.Changes[0].Reason)
	}
	if _, err := e.Apply(ctx, "agent", p.ID, "decommission"); err != nil {
		t.Fatal(err)
	}
	if len(rt.Containers) != 0 || len(rt.Volumes) != 0 {
		t.Fatal("container and volumes should be gone")
	}
	if _, err := e.GetApp(ctx, "web"); code(err) != "app_not_found" {
		t.Fatalf("want app_not_found, got %v", err)
	}
}

func TestDiagnosePortMismatch(t *testing.T) {
	e, rt := setup(t)
	ctx := context.Background()
	deploy(t, e, web(spec.StageStaging, "node:22"))
	rt.LogLines["web"] = []string{"booting", "Server listening on port 3000"}
	d, err := e.Diagnose(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range d.Findings {
		if f.Code == "port_mismatch" && f.SuggestedFix.(map[string]any)["port"] == 3000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected port_mismatch finding, got %+v", d.Findings)
	}
}

func hasFinding(d *Diagnosis, code string) bool {
	for _, f := range d.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

func TestMissingEnvVar(t *testing.T) {
	a := &spec.App{Env: map[string]string{"QUEUE": "x"}}
	cases := map[string]string{
		"fatal: DATABASE_URL is required but not set":   "DATABASE_URL",
		"ERROR missing environment variable REDIS_HOST": "REDIS_HOST",
		"QUEUE is required":                             "",
		"server started, nothing missing here":          "",
		"Error: required value not set":                 "",
	}
	for line, want := range cases {
		if got := missingEnvVar([]string{line}, a); got != want {
			t.Errorf("%q: got %q, want %q", line, got, want)
		}
	}
}

func gitApp(ref string) *spec.App {
	return &spec.App{Name: "api", Stage: spec.StageStaging, Port: 8080,
		Build: &spec.Build{Git: "https://github.com/acme/api.git", Ref: ref}}
}

func TestGitBuildFlow(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rt, bd := runtime.NewFake(), builder.NewFake()
	e := New(st, rt, bd, &proxy.Fake{})
	ctx := context.Background()
	shaA, shaB := strings.Repeat("a", 40), strings.Repeat("b", 40)
	bd.Refs["https://github.com/acme/api.git@main"] = shaA

	// The plan pins the commit; the deploy runs the built image.
	p, err := e.Plan(ctx, "agent", PlanRequest{Spec: gitApp("main")})
	if err != nil {
		t.Fatal(err)
	}
	if p.Desired.Build.Commit != shaA {
		t.Fatalf("plan should pin commit %s, got %+v", shaA, p.Desired.Build)
	}
	res, err := e.Apply(ctx, "agent", p.ID, "first")
	if err != nil {
		t.Fatal(err)
	}
	if res.Deployment.Commit != shaA || !strings.HasPrefix(res.Deployment.Image, "hangar-build/api:aaaaaaaaaaaa-") {
		t.Fatalf("deployment should record commit and built image: %+v", res.Deployment)
	}
	if rt.Containers["api"].Image != res.Deployment.Image || rt.Containers["api"].Build != nil {
		t.Fatalf("runtime should get the built image, got %+v", rt.Containers["api"])
	}
	logRes, err := e.DeployLog(ctx, res.Deployment.ID)
	if err != nil || len(logRes.Lines) == 0 || !logRes.Untrusted {
		t.Fatalf("build log should be stored and marked untrusted: %+v %v", logRes, err)
	}

	// Same branch, no new commits: nothing to do.
	p, _ = e.Plan(ctx, "agent", PlanRequest{Spec: gitApp("main")})
	if p.Action != "noop" {
		t.Fatalf("want noop, got %s %+v", p.Action, p.Changes)
	}

	// The branch moved: the plan shows the commit change.
	bd.Refs["https://github.com/acme/api.git@main"] = shaB
	p, _ = e.Plan(ctx, "agent", PlanRequest{Spec: gitApp("main")})
	if p.Action != "update" || len(p.Changes) != 1 || p.Changes[0].Path != "build.commit" || p.Changes[0].To != shaB {
		t.Fatalf("want a build.commit change, got %+v", p.Changes)
	}

	// A failed build leaves the running version alone.
	before := rt.Containers["api"]
	bd.FailNext = apierr.New(422, "build_failed", "docker build failed for commit bbbbbbbbbbbb")
	_, err = e.Apply(ctx, "agent", p.ID, "upgrade")
	ae, _ := apierr.As(err)
	if code(err) != "build_failed" || ae.Details["failed_in"] != store.PhaseBuild || ae.SuggestedFix.(map[string]any)["tool"] != "deploy_log" {
		t.Fatalf("want build_failed pointing at the build log, got %+v", err)
	}
	if rt.Containers["api"].Image != before.Image {
		t.Fatal("a failed build must not touch the running container")
	}
	diag, _ := e.Diagnose(ctx, "api")
	if !diag.Healthy || !hasFinding(diag, "build_failed") || len(diag.DeployLog) == 0 {
		t.Fatalf("diagnose: app still healthy, with a build_failed warning and the build log; got %+v", diag)
	}
	if hasFinding(diag, "last_deploy_failed") {
		t.Fatal("a build failure is not a deploy failure")
	}

	// Rolling back to the first deployment reuses its pinned commit.
	if _, err := e.Plan(ctx, "agent", PlanRequest{Spec: gitApp("main")}); err != nil {
		t.Fatal(err)
	}
	app, _ := e.GetApp(ctx, "api")
	if app.Spec.Build.Commit != shaA {
		t.Fatalf("current spec should still be commit A, got %s", app.Spec.Build.Commit)
	}
}

func TestGitBuildBadRef(t *testing.T) {
	e, _ := setup(t)
	if _, err := e.Plan(context.Background(), "agent", PlanRequest{Spec: gitApp("nope")}); code(err) != "git_ref_not_found" {
		t.Fatalf("want git_ref_not_found at plan time, got %v", err)
	}
}

func TestPerAppLocking(t *testing.T) {
	e, _ := setup(t)
	ctx := context.Background()
	unlock := e.lockApp("web")
	done := make(chan error, 1)
	go func() {
		_, err := e.Plan(ctx, "agent", PlanRequest{Spec: &spec.App{Name: "other", Image: "nginx"}})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a lock on one app must not block another app")
	}
	unlock()
}

func webDomains(image string, domains ...string) *spec.App {
	return &spec.App{Name: "web", Image: image, Port: 8080, Domains: domains,
		Health: &spec.Health{Path: "/healthz"}}
}

func setupProxy(t *testing.T) (*Engine, *runtime.Fake, *proxy.Fake) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rt, px := runtime.NewFake(), &proxy.Fake{}
	return New(st, rt, builder.NewFake(), px), rt, px
}

func TestDomainsRoutingAndZeroDowntime(t *testing.T) {
	e, rt, px := setupProxy(t)
	ctx := context.Background()

	first := deploy(t, e, webDomains("app:1", "web.example.com"))
	if got := px.Upstream("web"); got != runtime.Upstream(first.Deployment.ID, 8080) {
		t.Fatalf("route should point at the first deployment, got %q", got)
	}
	if !rt.LastOpts.ZeroDowntime || first.Deployment.Strategy != "zero-downtime" {
		t.Fatal("an app without host_port or volumes deploys with zero downtime")
	}
	v, _ := e.GetApp(ctx, "web")
	if len(v.URLs) != 1 || v.URLs[0] != "https://web.example.com" {
		t.Fatalf("urls = %v", v.URLs)
	}

	p, _ := e.Plan(ctx, "agent", PlanRequest{Spec: webDomains("app:2", "web.example.com")})
	if p.Downtime {
		t.Fatal("a zero-downtime update must not be flagged as downtime")
	}
	second, err := e.Apply(ctx, "agent", p.ID, "v2")
	if err != nil {
		t.Fatal(err)
	}
	if got := px.Upstream("web"); got != runtime.Upstream(second.Deployment.ID, 8080) {
		t.Fatalf("route should switch to the new deployment, got %q", got)
	}

	// The new version fails its health check: traffic stays on v2.
	rt.FailNext = apierr.New(502, "health_check_failed", "the new container did not become healthy within 60s")
	rt.FailOutput = []string{"Server listening on port 3000"}
	p, _ = e.Plan(ctx, "agent", PlanRequest{Spec: webDomains("app:3", "web.example.com")})
	_, err = e.Apply(ctx, "agent", p.ID, "v3")
	ae, _ := apierr.As(err)
	if code(err) != "health_check_failed" || ae.Details[runtime.DetailPreviousRunning] != true || ae.SuggestedFix.(map[string]any)["tool"] != "deploy_log" {
		t.Fatalf("want health_check_failed keeping the previous version, got %+v", err)
	}
	if got := px.Upstream("web"); got != runtime.Upstream(second.Deployment.ID, 8080) {
		t.Fatalf("traffic must stay on v2, route is %q", got)
	}
	diag, _ := e.Diagnose(ctx, "web")
	if !diag.Healthy || !hasFinding(diag, "new_version_unhealthy") || !hasFinding(diag, "port_mismatch") {
		t.Fatalf("diagnose should explain the failed version from its captured output: %+v", diag.Findings)
	}
	app, _ := e.GetApp(ctx, "web")
	if app.CurrentDeployment != second.Deployment.ID {
		t.Fatal("the current deployment must still be v2")
	}

	// Removing the domains removes the route; deleting the app too.
	deploy(t, e, webDomains("app:2"))
	if px.Upstream("web") != "" {
		t.Fatal("route should be removed when domains are removed")
	}
	deploy(t, e, webDomains("app:2", "web.example.com"))
	p, _ = e.Plan(ctx, "agent", PlanRequest{Delete: "web"})
	if _, err := e.Apply(ctx, "agent", p.ID, "remove"); err != nil {
		t.Fatal(err)
	}
	if px.Upstream("web") != "" {
		t.Fatal("deleting the app must remove its route")
	}
}

func TestDowntimeFlagFollowsStrategy(t *testing.T) {
	e, rt, _ := setupProxy(t)
	ctx := context.Background()
	withVol := &spec.App{Name: "db", Image: "postgres:16", Port: 5432, Volumes: []spec.Volume{{Name: "data", Path: "/data"}}}
	deploy(t, e, withVol)
	if rt.LastOpts.ZeroDowntime {
		t.Fatal("apps with volumes must be recreated, never run twice on the same data")
	}
	upd := withVol.Clone()
	upd.Image = "postgres:16.1"
	p, _ := e.Plan(ctx, "agent", PlanRequest{Spec: upd})
	if !p.Downtime {
		t.Fatal("recreate updates must be flagged as downtime")
	}
}

func TestDomainConflictsAndProxyDisabled(t *testing.T) {
	e, _, _ := setupProxy(t)
	ctx := context.Background()
	deploy(t, e, webDomains("app:1", "shop.example.com"))
	other := &spec.App{Name: "other", Image: "x", Port: 80, Domains: []string{"shop.example.com"}}
	_, err := e.Plan(ctx, "agent", PlanRequest{Spec: other})
	if ae, _ := apierr.As(err); code(err) != "domain_in_use" || ae.Details["app"] != "web" {
		t.Fatalf("want domain_in_use naming web, got %v", err)
	}

	st, _ := store.Open(filepath.Join(t.TempDir(), "h.db"))
	defer st.Close()
	noProxy := New(st, runtime.NewFake(), builder.NewFake(), nil)
	if _, err := noProxy.Plan(ctx, "agent", PlanRequest{Spec: webDomains("app:1", "a.example.com")}); code(err) != "proxy_disabled" {
		t.Fatalf("want proxy_disabled, got %v", err)
	}
}

func TestStartProxyRestoresRoutes(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "h.db"))
	e := New(st, runtime.NewFake(), builder.NewFake(), &proxy.Fake{})
	res := deploy(t, e, webDomains("app:1", "web.example.com"))
	st.Close()

	st2, _ := store.Open(filepath.Join(dir, "h.db"))
	defer st2.Close()
	px := &proxy.Fake{}
	e2 := New(st2, runtime.NewFake(), builder.NewFake(), px)
	if err := e2.StartProxy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if px.Upstream("web") != runtime.Upstream(res.Deployment.ID, 8080) {
		t.Fatalf("routes must be rebuilt from state on start, got %+v", px.Routes)
	}
}

func TestAutoBuildInjectsPortAndRecordsDetection(t *testing.T) {
	e, rt := setup(t)
	ctx := context.Background()
	bd := e.bd.(*builder.Fake)
	bd.Refs["https://github.com/acme/web.git@main"] = strings.Repeat("a", 40)
	app := &spec.App{Name: "web", Port: 8080, Build: &spec.Build{Git: "https://github.com/acme/web.git", Ref: "main", Builder: spec.BuilderAuto}}
	res := deploy(t, e, app)
	if rt.Containers["web"].Env["PORT"] != "8080" || res.Deployment.Detected != "node 22 · npm · npm start" {
		t.Fatalf("auto builds must get PORT and record what was detected: env=%v detected=%q", rt.Containers["web"].Env, res.Deployment.Detected)
	}
	// An explicit PORT wins.
	app2 := app.Clone()
	app2.Env = map[string]string{"PORT": "9000"}
	deploy(t, e, app2)
	if rt.Containers["web"].Env["PORT"] != "9000" {
		t.Fatal("an explicit PORT must not be overridden")
	}
	// Dockerfile builds don't get PORT injected.
	plain := &spec.App{Name: "plain", Port: 8080, Build: &spec.Build{Git: "https://github.com/acme/web.git", Ref: "main"}}
	deploy(t, e, plain)
	if _, ok := rt.Containers["plain"].Env["PORT"]; ok {
		t.Fatal("Dockerfile builds must not get PORT injected")
	}
	// A preview of an auto-built app works the same way (this staging
	// parent's env, including its explicit PORT, is inherited).
	pr, err := e.CreatePreview(ctx, "agent", PreviewRequest{App: "web", Ref: "main", Name: "try"})
	if err != nil || rt.Containers[pr.App].Env["PORT"] != "9000" {
		t.Fatalf("preview of an auto-built app: %v %v", err, rt.Containers[pr.App].Env)
	}
	det, err := e.DetectBuild(ctx, DetectRequest{App: "web"})
	if err != nil || det.Detection.Language != "node" {
		t.Fatalf("detect: %+v %v", det, err)
	}
}
