package engine

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/dunkbing/coldship/internal/offsite"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

func backupSetup(t *testing.T) (*Engine, *runtime.Fake, *offsite.Memory) {
	t.Helper()
	e, rt := dbSetup(t)
	mem := offsite.NewMemory()
	e.NewRemote = func(offsite.Config, bool) (offsite.Remote, error) { return mem, nil }
	return e, rt, mem
}

func org(ctx context.Context) context.Context { return store.WithOrg(ctx, store.DefaultOrg) }

func TestOffsiteBackups(t *testing.T) {
	e, fake, mem := backupSetup(t)
	ctx := org(context.Background())
	if _, err := e.CreateData(ctx, "admin", DataRequest{Name: "maindb", Engine: "postgres"}); err != nil {
		t.Fatal(err)
	}
	cfg := offsite.Config{Endpoint: "https://s3.example.com", Bucket: "backups", Prefix: "prod", AccessKey: "ak", SecretKey: "sk", Keep: 2}
	if _, err := e.Backups(ctx, "", "maindb"); code(err) != "backup_not_configured" {
		t.Fatalf("unconfigured: %v", err)
	}
	// A destination that can't be written to isn't saved.
	mem.SetFail(errors.New("nope"))
	if _, err := e.SaveBackup(ctx, "admin", cfg); code(err) != "backup_unreachable" {
		t.Fatalf("broken destination: %v", err)
	}
	mem.SetFail(nil)
	v, err := e.SaveBackup(ctx, "admin", cfg)
	if err != nil || !v.Configured || !v.SecretSet || v.Keep != 2 {
		t.Fatalf("save: %+v %v", v, err)
	}
	// Saving again with a blank secret keeps the old one.
	cfg2 := cfg
	cfg2.SecretKey = ""
	if _, err := e.SaveBackup(ctx, "admin", cfg2); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := e.backupConfig(ctx, store.DefaultOrg); c.SecretKey != "sk" {
		t.Fatalf("secret kept: %q", c.SecretKey)
	}

	var ids []string
	for _, data := range []string{"v1", "v2", "v3"} {
		fake.DBs["/maindb"] = data
		sn, err := e.SnapshotData(ctx, "agent", "maindb", "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sn.ID)
	}
	n, err := e.SyncBackups(ctx, "")
	if err != nil || n != 3 {
		t.Fatalf("sync: %d %v", n, err)
	}
	// Two kept (dump + manifest each), oldest pruned.
	keys := mem.Keys()
	if len(keys) != 4 || !strings.HasPrefix(keys[0], "prod/data/maindb/") || strings.Contains(strings.Join(keys, ","), ids[0]) {
		t.Fatalf("remote keys: %v", keys)
	}
	if n, err := e.SyncBackups(ctx, ""); err != nil || n != 0 {
		t.Fatalf("second sync: %d %v", n, err)
	}
	st, _ := e.BackupStatus(ctx)
	if st.Pending != 0 || st.Uploaded != 3 || st.LastSync == nil || st.LastError != "" {
		t.Fatalf("status: %+v", st)
	}

	// A failing bucket is reported, and the snapshot retried later.
	fake.DBs["/maindb"] = "v4"
	if _, err := e.SnapshotData(ctx, "agent", "maindb", ""); err != nil {
		t.Fatal(err)
	}
	mem.SetFail(errors.New("bucket down"))
	if _, err := e.SyncBackups(ctx, ""); err == nil {
		t.Fatal("sync to a failing bucket must fail")
	}
	if st, _ := e.BackupStatus(ctx); st.Pending != 1 || !strings.Contains(st.LastError, "bucket down") {
		t.Fatalf("status after failure: %+v", st)
	}
	mem.SetFail(nil)
	if n, err := e.SyncBackups(ctx, ""); err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}

	// Disaster: the server's snapshots are gone. The bucket alone restores.
	snaps, _ := e.st.AllSnapshots(ctx)
	for _, sn := range snaps {
		os.Remove(sn.Path)
		e.st.DeleteSnapshot(ctx, sn.ID)
	}
	list, err := e.Backups(ctx, "", "maindb")
	if err != nil || len(list) != 2 || list[0].Engine != "postgres" || list[0].Database != "maindb" {
		t.Fatalf("list: %+v %v", list, err)
	}
	all, err := e.Backups(ctx, "", "")
	if err != nil || len(all) != 2 {
		t.Fatalf("list all: %+v %v", all, err)
	}
	fake.DBs["/maindb"] = "lost"
	if _, err := e.RestoreDataBackup(ctx, "admin", "maindb", list[1].Key, "disaster"); err != nil || fake.DBs["/maindb"] != "v3" {
		t.Fatalf("restore from bucket: %v %q", err, fake.DBs["/maindb"])
	}
	// The imported snapshot isn't uploaded again.
	before := len(mem.Keys())
	if _, err := e.SyncBackups(ctx, ""); err != nil {
		t.Fatal(err)
	}
	// The safety snapshot ("lost") is new and is uploaded; prune keeps 2.
	if got := len(mem.Keys()); got != before {
		t.Fatalf("keys after sync: %d, want %d", got, before)
	}

	// Tampered dumps, other engines and outside keys are refused.
	if list, err = e.Backups(ctx, "", "maindb"); err != nil || len(list) != 2 {
		t.Fatalf("relist: %+v %v", list, err)
	}
	mem.Objects[list[0].Key] = []byte("tampered")
	if _, err := e.RestoreDataBackup(ctx, "admin", "maindb", list[0].Key, "x"); code(err) != "backup_corrupt" {
		t.Fatalf("tampered: %v", err)
	}
	if _, err := e.CreateData(ctx, "admin", DataRequest{Name: "cache", Engine: "redis"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RestoreDataBackup(ctx, "admin", "cache", list[1].Key, "x"); code(err) != "engine_mismatch" {
		t.Fatalf("engine mismatch: %v", err)
	}
	if _, err := e.RestoreDataBackup(ctx, "admin", "maindb", "elsewhere/data/x/20260101T000000.000000000Z-snap_ab.dump", "x"); code(err) != "invalid_request" {
		t.Fatalf("outside key: %v", err)
	}

	// Another org sees nothing of this one's.
	other := store.WithOrg(context.Background(), "org_other")
	if _, err := e.Backups(other, "", ""); code(err) != "backup_not_configured" {
		t.Fatalf("other org: %v", err)
	}
	if st, _ := e.BackupStatus(other); !st.PublicOnly || st.Configured {
		t.Fatalf("other org status: %+v", st)
	}
}

func TestBackupSyncRacesPrune(t *testing.T) {
	e, _, mem := backupSetup(t)
	ctx := org(context.Background())
	e.SnapshotKeep = 1
	if _, err := e.CreateData(ctx, "admin", DataRequest{Name: "db", Engine: "postgres"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SaveBackup(ctx, "admin", offsite.Config{Endpoint: "s3.example.com", Bucket: "bk1", AccessKey: "a", SecretKey: "b"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			e.SnapshotData(ctx, "agent", "db", "")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if _, err := e.SyncBackups(ctx, ""); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
	if _, err := e.SyncBackups(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if len(mem.Keys()) == 0 {
		t.Fatal("nothing uploaded")
	}
}

func TestPublicOnly(t *testing.T) {
	for ip, want := range map[string]bool{"8.8.8.8": true, "127.0.0.1": false, "10.0.0.5": false, "169.254.169.254": false,
		"172.17.0.1": false, "100.64.1.1": false, "::1": false, "fd00::1": false, "2606:4700::1": true, "::ffff:127.0.0.1": false} {
		if got := offsite.Public(net.ParseIP(ip)); got != want {
			t.Errorf("Public(%s) = %v", ip, got)
		}
	}
	if _, err := offsite.New(offsite.Config{Endpoint: "http://minio:9000", Bucket: "b", AccessKey: "a", SecretKey: "s"}, true); err == nil {
		t.Error("restricted remotes must refuse http")
	}
}

func TestBackupNewBucketGetsEverything(t *testing.T) {
	e, fake, _ := backupSetup(t)
	buckets := map[string]*offsite.Memory{}
	e.NewRemote = func(cfg offsite.Config, _ bool) (offsite.Remote, error) {
		if buckets[cfg.Bucket+"/"+cfg.Prefix] == nil {
			buckets[cfg.Bucket+"/"+cfg.Prefix] = offsite.NewMemory()
		}
		return buckets[cfg.Bucket+"/"+cfg.Prefix], nil
	}
	ctx := org(context.Background())
	if _, err := e.CreateData(ctx, "admin", DataRequest{Name: "db", Engine: "postgres"}); err != nil {
		t.Fatal(err)
	}
	fake.DBs["/db"] = "x"
	for i := 0; i < 2; i++ {
		if _, err := e.SnapshotData(ctx, "agent", "db", ""); err != nil {
			t.Fatal(err)
		}
	}
	a := offsite.Config{Endpoint: "s3.example.com", Bucket: "test-bucket", AccessKey: "a", SecretKey: "b"}
	if _, err := e.SaveBackup(ctx, "admin", a); err != nil {
		t.Fatal(err)
	}
	if n, err := e.SyncBackups(ctx, ""); err != nil || n != 2 {
		t.Fatalf("first bucket: %d %v", n, err)
	}
	// Same place, new keys: nothing is copied again.
	a.AccessKey = "a2"
	if _, err := e.SaveBackup(ctx, "admin", a); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.SyncBackups(ctx, ""); n != 0 {
		t.Fatalf("same bucket recopied %d", n)
	}
	// A new bucket gets every snapshot.
	b := a
	b.Bucket = "real-bucket"
	if _, err := e.SaveBackup(ctx, "admin", b); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.BackupStatus(ctx); st.Pending != 2 || st.Uploaded != 0 {
		t.Fatalf("status after switching: %+v", st)
	}
	if n, err := e.SyncBackups(ctx, ""); err != nil || n != 2 || len(buckets["real-bucket/"].Keys()) != 4 {
		t.Fatalf("new bucket: %d %v %v", n, err, buckets["real-bucket/"].Keys())
	}
	// Off, then on again elsewhere: everything again.
	if err := e.DeleteBackup(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	b.Prefix = "again"
	if _, err := e.SaveBackup(ctx, "admin", b); err != nil {
		t.Fatal(err)
	}
	if n, err := e.SyncBackups(ctx, ""); err != nil || n != 2 {
		t.Fatalf("after off and on: %d %v", n, err)
	}
}

func TestAppDatabaseFromBucket(t *testing.T) {
	e, fake, mem := backupSetup(t)
	ctx := org(context.Background())
	deploy(t, e, withDB(spec.StageStaging, pg))
	fake.DBs["api/main"] = "orders"
	if _, err := e.Snapshot(ctx, "agent", "api", "main", "nightly"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SaveBackup(ctx, "admin", offsite.Config{Endpoint: "s3.example.com", Bucket: "bk1", Prefix: "p", AccessKey: "a", SecretKey: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SyncBackups(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if keys := mem.Keys(); len(keys) != 2 || !strings.HasPrefix(keys[0], "p/apps/api/main/") {
		t.Fatalf("app database keys: %v", keys)
	}
	// The server loses its snapshots and data.
	snaps, _ := e.st.AllSnapshots(ctx)
	for _, sn := range snaps {
		os.Remove(sn.Path)
		e.st.DeleteSnapshot(ctx, sn.ID)
	}
	fake.DBs["api/main"] = "empty"
	list, err := e.Backups(ctx, "api", "main")
	if err != nil || len(list) != 1 || list[0].App != "api" {
		t.Fatalf("list: %+v %v", list, err)
	}
	if _, err := e.ImportAppBackup(ctx, "admin", "api", "nope", list[0].Key); err == nil {
		t.Fatal("unknown database")
	}
	sn, err := e.ImportAppBackup(ctx, "admin", "api", "main", list[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Plan(ctx, "admin", PlanRequest{Restore: &RestoreRequest{App: "api", Database: "main", Snapshot: sn.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, "admin", p.ID, "new server"); err != nil || fake.DBs["api/main"] != "orders" {
		t.Fatalf("restore: %v %q", err, fake.DBs["api/main"])
	}
	// Another org can't import into it.
	if _, err := e.ImportAppBackup(store.WithOrg(ctx, "org_other"), "admin", "api", "main", list[0].Key); code(err) != "app_not_found" && code(err) != "not_found" {
		t.Fatalf("other org: %v", err)
	}
}
