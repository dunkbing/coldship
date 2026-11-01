package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/builder"
	"github.com/dunkbing/coldship/internal/proxy"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

func dbSetup(t *testing.T) (*Engine, *runtime.Fake) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "h.db"), testKeys)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rt := runtime.NewFake()
	e := New(st, rt, builder.NewFake(), &proxy.Fake{})
	e.SnapshotDir = filepath.Join(dir, "snapshots")
	return e, rt
}

func withDB(stage spec.Stage, dbs ...spec.Database) *spec.App {
	return &spec.App{Name: "api", Stage: stage, Image: "api:1", Port: 3000, Databases: dbs}
}

var pg = spec.Database{Name: "main", Engine: spec.EnginePostgres}
var cache = spec.Database{Name: "cache", Engine: spec.EngineRedis}

func TestDatabaseInjectedButNeverShown(t *testing.T) {
	e, rt := dbSetup(t)
	ctx := context.Background()
	p, err := e.Plan(ctx, "agent", PlanRequest{Spec: withDB(spec.StageStaging, pg, cache)})
	if err != nil {
		t.Fatal(err)
	}
	if p.Desired.Databases[0].Version != "17" || p.Desired.Databases[0].Env != "DATABASE_URL" || p.Desired.Databases[1].Env != "REDIS_URL" {
		t.Fatalf("defaults not applied: %+v", p.Desired.Databases)
	}
	res, err := e.Apply(ctx, "agent", p.ID, "create")
	if err != nil {
		t.Fatal(err)
	}
	env := rt.Containers["api"].Env
	if !strings.HasPrefix(env["DATABASE_URL"], "postgres://app:") || !strings.Contains(env["DATABASE_URL"], "@api-db-main:5432/app") ||
		!strings.HasPrefix(env["REDIS_URL"], "redis://default:") {
		t.Fatalf("connection URLs not injected: %v", env)
	}
	creds, _ := e.st.GetDatabase(ctx, "api", "main")
	// The password must not appear anywhere the API returns.
	view, _ := e.GetApp(ctx, "api")
	log, _ := e.DeployLog(ctx, res.Deployment.ID)
	for name, v := range map[string]any{"plan": p, "apply": res, "app": view, "deploy log": log} {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), creds.Password) {
			t.Fatalf("the database password leaked into the %s", name)
		}
	}
	if len(view.Databases) != 2 || !view.Databases[0].Status.Running {
		t.Fatalf("databases view: %+v", view.Databases)
	}

	// Redeploying keeps the same credentials.
	deploy(t, e, withDB(spec.StageStaging, pg, cache).Clone())
	again, _ := e.st.GetDatabase(ctx, "api", "main")
	if again.Password != creds.Password {
		t.Fatal("credentials must be stable across deploys")
	}
}

func TestRemovingDatabaseSnapshotsFirst(t *testing.T) {
	e, rt := dbSetup(t)
	ctx := context.Background()
	deploy(t, e, withDB(spec.StageProduction, pg))
	rt.DBs["api/main"] = "precious rows"

	p, _ := e.Plan(ctx, "agent", PlanRequest{Spec: withDB(spec.StageProduction)})
	if !p.Destructive || !p.RequiresApproval || !strings.Contains(p.Changes[0].Reason, "snapshot") {
		t.Fatalf("removing a production database must be destructive and need approval: %+v", p)
	}
	e.Approve(ctx, "human", p.ID)

	// A failing snapshot keeps the database.
	rt.FailSnapshot = apierr.New(502, "snapshot_failed", "disk full")
	if _, err := e.Apply(ctx, "agent", p.ID, "drop db"); err != nil {
		t.Fatalf("the app deploy itself should succeed: %v", err)
	}
	if _, ok := rt.DBs["api/main"]; !ok {
		t.Fatal("a database whose snapshot failed must not be deleted")
	}

	// With a working snapshot it goes, and the snapshot has the data.
	deploy(t, e, withDB(spec.StageProduction, pg))
	p, _ = e.Plan(ctx, "agent", PlanRequest{Spec: withDB(spec.StageProduction)})
	e.Approve(ctx, "human", p.ID)
	if _, err := e.Apply(ctx, "agent", p.ID, "drop db"); err != nil {
		t.Fatal(err)
	}
	if _, ok := rt.DBs["api/main"]; ok {
		t.Fatal("database should be removed")
	}
	snaps, _ := e.Snapshots(ctx, "api", "main")
	if len(snaps) == 0 {
		t.Fatal("expected a snapshot")
	}
	data, _ := os.ReadFile(snaps[0].Path)
	if string(data) != "precious rows" || !strings.HasPrefix(snaps[0].Reason, "removed from the spec") {
		t.Fatalf("snapshot content %q reason %q", data, snaps[0].Reason)
	}
	if c, _ := e.st.GetDatabase(ctx, "api", "main"); c != nil {
		t.Fatal("credentials should be deleted with the database")
	}
}

func TestDeleteAppRefusedIfSnapshotFails(t *testing.T) {
	e, rt := dbSetup(t)
	ctx := context.Background()
	deploy(t, e, withDB(spec.StageStaging, pg))
	rt.FailSnapshot = apierr.New(502, "snapshot_failed", "disk full")
	p, _ := e.Plan(ctx, "agent", PlanRequest{Delete: "api"})
	_, err := e.Apply(ctx, "agent", p.ID, "delete")
	if code(err) != "snapshot_failed" {
		t.Fatalf("want snapshot_failed, got %v", err)
	}
	if _, ok := rt.Containers["api"]; !ok {
		t.Fatal("the app must not be deleted when its database can't be snapshotted")
	}
	if _, ok := rt.DBs["api/main"]; !ok {
		t.Fatal("the database must not be deleted")
	}
}

func TestRestore(t *testing.T) {
	e, rt := dbSetup(t)
	ctx := context.Background()
	deploy(t, e, withDB(spec.StageProduction, pg))
	rt.DBs["api/main"] = "good data"
	good, err := e.Snapshot(ctx, "agent", "api", "main", "before migration")
	if err != nil {
		t.Fatal(err)
	}
	rt.DBs["api/main"] = "broken by migration"

	p, err := e.Plan(ctx, "agent", PlanRequest{Restore: &RestoreRequest{App: "api", Database: "main", Snapshot: good.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != "restore" || !p.Destructive || !p.RequiresApproval {
		t.Fatalf("a production restore is destructive and needs approval: %+v", p)
	}
	if _, err := e.Apply(ctx, "agent", p.ID, "undo migration"); code(err) != "approval_required" {
		t.Fatalf("want approval_required, got %v", err)
	}
	e.Approve(ctx, "human", p.ID)
	res, err := e.Apply(ctx, "agent", p.ID, "undo migration")
	if err != nil {
		t.Fatal(err)
	}
	if rt.DBs["api/main"] != "good data" {
		t.Fatalf("data after restore: %q", rt.DBs["api/main"])
	}
	snaps, _ := e.Snapshots(ctx, "api", "main")
	saved, _ := os.ReadFile(snaps[0].Path)
	if string(saved) != "broken by migration" || !strings.Contains(res.Message, snaps[0].ID) {
		t.Fatalf("the pre-restore data should be saved and named in the result: %q / %s", saved, res.Message)
	}

	// A tampered snapshot is refused.
	os.WriteFile(good.Path, []byte("tampered"), 0o600)
	p, _ = e.Plan(ctx, "agent", PlanRequest{Restore: &RestoreRequest{App: "api", Database: "main", Snapshot: good.ID}})
	e.Approve(ctx, "human", p.ID)
	if _, err := e.Apply(ctx, "agent", p.ID, "again"); code(err) != "snapshot_corrupt" {
		t.Fatalf("want snapshot_corrupt, got %v", err)
	}
	if _, err := e.Plan(ctx, "agent", PlanRequest{Restore: &RestoreRequest{App: "api", Database: "main", Snapshot: "snap_nope"}}); code(err) != "snapshot_not_found" {
		t.Fatalf("want snapshot_not_found, got %v", err)
	}
}

func TestDatabaseGuards(t *testing.T) {
	e, _ := dbSetup(t)
	ctx := context.Background()
	deploy(t, e, withDB(spec.StageStaging, pg))
	upgraded := withDB(spec.StageStaging, spec.Database{Name: "main", Engine: spec.EnginePostgres, Version: "16"})
	if _, err := e.Plan(ctx, "agent", PlanRequest{Spec: upgraded}); code(err) != "database_version_change" {
		t.Fatalf("want database_version_change, got %v", err)
	}
	clash := withDB(spec.StageStaging, pg)
	clash.Env = map[string]string{"DATABASE_URL": "x"}
	if _, err := e.Plan(ctx, "agent", PlanRequest{Spec: clash}); code(err) != "invalid_spec" {
		t.Fatalf("env clash: got %v", err)
	}
	two := withDB(spec.StageStaging, pg, spec.Database{Name: "reports", Engine: spec.EnginePostgres})
	_, err := e.Plan(ctx, "agent", PlanRequest{Spec: two})
	if ae, _ := apierr.As(err); code(err) != "invalid_spec" || ae.SuggestedFix.(map[string]any)["env"] != "REPORTS_URL" {
		t.Fatalf("two postgres DBs need distinct env names: %v", err)
	}
}

func TestPreviewGetsOwnDatabaseAndRetention(t *testing.T) {
	e, rt := dbSetup(t)
	ctx := context.Background()
	e.SnapshotKeep = 2
	deploy(t, e, withDB(spec.StageProduction, pg))
	rt.DBs["api/main"] = "prod data"

	res, err := e.CreatePreview(ctx, "agent", PreviewRequest{App: "api", Image: "api:2", Name: "pr-1"})
	if err != nil {
		t.Fatal(err)
	}
	if rt.DBs["api-pr-1/main"] != "" || !strings.Contains(rt.Containers["api-pr-1"].Env["DATABASE_URL"], "@api-pr-1-db-main:") {
		t.Fatal("a preview must get its own empty database, not the parent's")
	}
	if _, err := e.DeletePreview(ctx, "agent", res.App, "done"); err != nil {
		t.Fatal(err)
	}
	if snaps, _ := e.Snapshots(ctx, "api-pr-1", ""); len(snaps) != 0 {
		t.Fatal("throwaway previews are not snapshotted")
	}

	// An automatic snapshot, then a flood of manual ones: the automatic one
	// must survive.
	if _, err := e.snapshotNamed(ctx, "coldship", "api", "main", "scheduled", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := e.Snapshot(ctx, "agent", "api", "main", ""); err != nil {
			t.Fatal(err)
		}
	}
	snaps, _ := e.Snapshots(ctx, "api", "main")
	files, _ := os.ReadDir(filepath.Join(e.SnapshotDir, "api", "main"))
	if len(snaps) != 3 || len(files) != 3 {
		t.Fatalf("retention should keep 2 manual and 1 automatic snapshot, have %d rows and %d files", len(snaps), len(files))
	}
	if last := snaps[len(snaps)-1]; last.Manual || last.Reason != "scheduled" {
		t.Fatalf("manual snapshots pushed out the automatic one: %+v", snaps)
	}
	if n, err := e.ScheduledSnapshots(ctx); err != nil || n != 1 {
		t.Fatalf("scheduled snapshots: %d %v", n, err)
	}
}

// A deploy token must not be able to demote production and then delete it,
// with its databases, without approval or snapshots.
func TestStageDemotionNeedsApproval(t *testing.T) {
	e, _ := dbSetup(t)
	ctx := context.Background()
	p, err := e.Plan(ctx, "agent", PlanRequest{Spec: withDB(spec.StageProduction, pg)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, "agent", p.ID, "create"); err != nil {
		t.Fatal(err)
	}

	// stage: preview is reserved for real previews.
	if _, err := e.Plan(ctx, "agent", PlanRequest{Spec: withDB(spec.StagePreview, pg)}); code(err) != "invalid_spec" {
		t.Fatalf("stage: preview on a regular app: want invalid_spec, got %v", err)
	}
	if _, err := e.Plan(ctx, "agent", PlanRequest{Spec: &spec.App{Name: "fresh", Stage: spec.StagePreview, Image: "x:1"}}); code(err) != "invalid_spec" {
		t.Fatalf("stage: preview on a new app: want invalid_spec, got %v", err)
	}

	// An app row without a spec (e.g. a first deploy that never finished)
	// must not crash the check.
	if err := e.st.UpsertApp(ctx, &store.App{Name: "ghost", CreatedAt: e.Now(), UpdatedAt: e.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Plan(ctx, "agent", PlanRequest{Spec: &spec.App{Name: "ghost", Stage: spec.StagePreview, Image: "x:1"}}); code(err) != "invalid_spec" {
		t.Fatalf("stage: preview on a spec-less app: want invalid_spec, got %v", err)
	}

	// Leaving production, even with no other change, needs approval.
	p, err = e.Plan(ctx, "agent", PlanRequest{Spec: withDB(spec.StageStaging, pg)})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Destructive || !p.RequiresApproval {
		t.Fatalf("demotion must need approval: %+v", p)
	}
	if _, err := e.Apply(ctx, "agent", p.ID, "demote"); code(err) != "approval_required" {
		t.Fatalf("want approval_required, got %v", err)
	}

	// Once approved and demoted, deleting still snapshots the database.
	if _, err := e.Approve(ctx, "alice", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, "agent", p.ID, "demote"); err != nil {
		t.Fatal(err)
	}
	p, err = e.Plan(ctx, "agent", PlanRequest{Delete: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, "agent", p.ID, "delete"); err != nil {
		t.Fatal(err)
	}
	snaps, err := e.st.ListSnapshots(ctx, "api", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) == 0 {
		t.Fatal("deleting a non-preview app must snapshot its databases")
	}
}

// Different apps must never map to the same Docker container, alias or volume.
func TestDockerNameCollisions(t *testing.T) {
	e, _ := dbSetup(t)
	ctx := context.Background()
	p, err := e.Plan(ctx, "agent", PlanRequest{Spec: &spec.App{Name: "web", Image: "web:1", Port: 80, Databases: []spec.Database{pg}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, "agent", p.ID, "create"); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*spec.App{
		{Name: "web-db-main", Image: "x:1"}, // web's database container
		{Name: "web-next", Image: "x:1"},    // web's zero-downtime container
		{Name: "proxy", Image: "x:1"},       // the Caddy proxy
		{Name: "dep-0123456789ab", Image: "x:1"},
		{Name: "web-db", Image: "x:1", Volumes: []spec.Volume{{Name: "main-data", Path: "/d"}}}, // web's database volume
		{Name: "solo", Image: "x:1", Volumes: []spec.Volume{{Name: "db-main-data", Path: "/d"}}, Databases: []spec.Database{pg}},
	} {
		_, err := e.Plan(ctx, "agent", PlanRequest{Spec: a})
		if c := code(err); c != "name_in_use" && c != "invalid_spec" {
			t.Errorf("%s: want a collision error, got %v", a.Name, err)
		}
	}
	if got := previewName("web", "db-main"); got == "web-db-main" || !strings.HasPrefix(got, "web-db-main-") {
		t.Errorf("preview of branch db-main: got %s", got)
	}
	if got := previewName("web", "next"); got == "web-next" {
		t.Errorf("preview of branch next: got %s", got)
	}
}
