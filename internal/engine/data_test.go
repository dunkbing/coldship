package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/dunkbing/coldship/internal/spec"
)

func TestDataServices(t *testing.T) {
	e, rt := dbSetup(t)
	ctx := context.Background()

	v, err := e.CreateData(ctx, "admin", DataRequest{Name: "maindb", Engine: "postgres"})
	if err != nil || v.Version != "17" || v.Host != "coldship-data-maindb" || !v.Status.Running {
		t.Fatalf("create: %+v %v", v, err)
	}
	if _, err := e.CreateData(ctx, "admin", DataRequest{Name: "maindb", Engine: "redis"}); code(err) != "data_exists" {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := e.CreateData(ctx, "admin", DataRequest{Name: "x", Engine: "oracle"}); code(err) != "invalid_spec" {
		t.Fatalf("bad engine: %v", err)
	}
	if _, err := e.CreateData(ctx, "admin", DataRequest{Name: "files", Engine: "s3"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateData(ctx, "admin", DataRequest{Name: "other", Engine: "postgres"}); err != nil {
		t.Fatal(err)
	}

	// Apps connect with uses and get the env.
	app := &spec.App{Name: "web", Image: "web:1", Port: 80, Uses: []string{"maindb", "files"}}
	deploy(t, e, app)
	env := rt.Containers["web"].Env
	if !strings.Contains(env["DATABASE_URL"], "@coldship-data-maindb:5432/") || env["S3_BUCKET"] != "files" ||
		env["AWS_ENDPOINT_URL"] != "http://coldship-data-files:8333" || env["AWS_ACCESS_KEY_ID"] == "" {
		t.Fatalf("env from data services: %v", env)
	}

	// Two postgres services need distinct variables.
	two := &spec.App{Name: "api", Image: "api:1", Uses: []string{"maindb", "other"}}
	if _, err := e.Plan(ctx, "agent", PlanRequest{Spec: two}); code(err) != "invalid_spec" {
		t.Fatalf("env conflict: %v", err)
	}
	two.Uses = []string{"maindb", "other=OTHER_URL"}
	deploy(t, e, two)
	if !strings.Contains(rt.Containers["api"].Env["OTHER_URL"], "coldship-data-other") {
		t.Fatalf("renamed URL: %v", rt.Containers["api"].Env)
	}
	if _, err := e.Plan(ctx, "agent", PlanRequest{Spec: &spec.App{Name: "z", Image: "z:1", Uses: []string{"nope"}}}); code(err) != "data_service_not_found" {
		t.Fatalf("missing service: %v", err)
	}
	// An app can't take a data service's container name.
	if _, err := e.Plan(ctx, "agent", PlanRequest{Spec: &spec.App{Name: "data-maindb", Image: "z:1"}}); code(err) != "name_in_use" {
		t.Fatalf("name collision: %v", err)
	}

	list, err := e.ListData(ctx)
	if err != nil || len(list) != 3 || list[0].App != "" {
		t.Fatalf("list: %+v %v", list, err)
	}
	for _, d := range list {
		if d.Name == "maindb" && strings.Join(d.UsedBy, ",") != "api,web" {
			t.Fatalf("used by: %+v", d.UsedBy)
		}
	}

	// Snapshot and restore.
	rt.DBs["/maindb"] = "v1"
	sn, err := e.SnapshotData(ctx, "agent", "maindb", "")
	if err != nil {
		t.Fatal(err)
	}
	rt.DBs["/maindb"] = "v2"
	if _, err := e.RestoreData(ctx, "admin", "maindb", sn.ID, "undo"); err != nil || rt.DBs["/maindb"] != "v1" {
		t.Fatalf("restore: %v %q", err, rt.DBs["/maindb"])
	}
	if _, err := e.SnapshotData(ctx, "agent", "files", ""); code(err) == "" {
		t.Fatal("s3 snapshots aren't supported and must say so")
	}

	// Deleting is refused while apps use it, and snapshots first.
	if err := e.DeleteData(ctx, "admin", "maindb", "x"); code(err) != "data_in_use" {
		t.Fatalf("delete in use: %v", err)
	}
	for _, a := range []string{"web", "api"} {
		p, err := e.Plan(ctx, "agent", PlanRequest{Patch: &spec.Patch{Name: a, RemoveUses: []string{"maindb"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Apply(ctx, "agent", p.ID, "disconnect"); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := e.DataSnapshots(ctx, "maindb")
	if err := e.DeleteData(ctx, "admin", "maindb", "done"); err != nil {
		t.Fatal(err)
	}
	if _, ok := rt.DBs["/maindb"]; ok {
		t.Fatal("container still there")
	}
	if after, _ := e.st.ListSnapshots(ctx, "", "maindb"); len(after) != len(before)+1 {
		t.Fatal("deleting must snapshot first")
	}
}

func TestRustFSStorage(t *testing.T) {
	e, rt := dbSetup(t)
	ctx := context.Background()
	v, err := e.CreateData(ctx, "admin", DataRequest{Name: "media", Engine: "rustfs"})
	if err != nil || v.Version != "1.0.4" {
		t.Fatalf("create: %+v %v", v, err)
	}
	deploy(t, e, &spec.App{Name: "web", Image: "web:1", Port: 80, Uses: []string{"media"}})
	env := rt.Containers["web"].Env
	if env["AWS_ENDPOINT_URL"] != "http://coldship-data-media:9000" || env["S3_BUCKET"] != "media" || env["AWS_SECRET_ACCESS_KEY"] == "" {
		t.Fatalf("env: %v", env)
	}
	if _, err := e.SnapshotData(ctx, "agent", "media", ""); err == nil {
		t.Fatal("object storage snapshots aren't supported")
	}
	bad := &spec.App{Name: "x", Image: "x:1", Databases: []spec.Database{{Name: "m", Engine: "rustfs", Version: "1.0.4"}}}
	if _, err := e.Plan(ctx, "agent", PlanRequest{Spec: bad}); code(err) != "invalid_spec" {
		t.Fatalf("rustfs as an app database: %v", err)
	}
}
