package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dunkbing/coldship/internal/api"
	"github.com/dunkbing/coldship/internal/builder"
	"github.com/dunkbing/coldship/internal/client"
	"github.com/dunkbing/coldship/internal/engine"
	"github.com/dunkbing/coldship/internal/offsite"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/store"
)

func TestBackupsAPI(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), testKeys)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := engine.New(st, runtime.NewFake(), builder.NewFake(), nil)
	eng.SnapshotDir = t.TempDir()
	buckets := map[string]*offsite.Memory{}
	var publicOnly []bool
	eng.NewRemote = func(cfg offsite.Config, restricted bool) (offsite.Remote, error) {
		publicOnly = append(publicOnly, restricted)
		if buckets[cfg.Bucket] == nil {
			buckets[cfg.Bucket] = offsite.NewMemory()
		}
		return buckets[cfg.Bucket], nil
	}
	srv := httptest.NewServer(api.New(eng, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	defer srv.Close()
	adminTok, _ := eng.EnsureAdminToken(ctx)
	admin := client.New(srv.URL, adminTok)
	dt, _ := admin.CreateToken(ctx, "ci", "deploy")
	deployer := client.New(srv.URL, dt.Token)
	lt, _ := admin.CreateToken(ctx, "limited", "deploy", "main*")
	limited := client.New(srv.URL, lt.Token)

	cfg := offsite.Config{Endpoint: "https://s3.example.com", Bucket: "bk-main", AccessKey: "ak", SecretKey: "very-secret"}
	if _, err := deployer.SaveBackup(ctx, cfg); errCode(err) != "insufficient_scope" {
		t.Fatalf("deployers can't set the bucket: %v", err)
	}
	if _, err := limited.BackupStatus(ctx); errCode(err) != "app_not_allowed" {
		t.Fatalf("limited tokens stay out: %v", err)
	}
	v, err := admin.SaveBackup(ctx, cfg)
	if err != nil || !v.Configured || v.PublicOnly {
		t.Fatalf("save: %+v %v", v, err)
	}
	// The secret never comes back.
	b := newBrowser(t, srv.URL)
	b.tok = adminTok
	if _, out := b.call("GET", "/v1/backups", nil); out["secret_key_set"] != true || strings.Contains(stringify(out), "very-secret") {
		t.Fatalf("status leaks the secret: %v", out)
	}

	if _, err := admin.CreateData(ctx, engine.DataRequest{Name: "maindb", Engine: "postgres"}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.SnapshotData(ctx, "maindb", "test"); err != nil {
		t.Fatal(err)
	}
	if v, err := admin.SyncBackups(ctx); err != nil || v.Uploaded != 1 {
		t.Fatalf("sync: %+v %v", v, err)
	}
	list, err := deployer.Backups(ctx, "", "maindb")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if _, err := limited.RestoreDataBackup(ctx, "maindb", list[0].Key, "x"); err == nil {
		t.Fatalf("limited tokens can't restore off-site backups: %v", err)
	}
	if _, err := admin.RestoreDataBackup(ctx, "maindb", list[0].Key, "drill"); err != nil {
		t.Fatal(err)
	}

	// Another org: public endpoints only, and none of the default org's data.
	if _, err := st.CreateOrg(ctx, "acme", "Acme"); err != nil {
		t.Fatal(err)
	}
	acmeTok, _ := st.CreateToken(ctx, "acme", "acme-admin", store.ScopeAdmin, nil)
	acme := client.New(srv.URL, acmeTok)
	if _, err := acme.Backups(ctx, "", "maindb"); errCode(err) != "data_service_not_found" && errCode(err) != "not_found" {
		t.Fatalf("other org's data service: %v", err)
	}
	if v, err := acme.BackupStatus(ctx); err != nil || v.Configured || !v.PublicOnly {
		t.Fatalf("acme status: %+v %v", v, err)
	}
	publicOnly = nil
	acmeCfg := cfg
	acmeCfg.Bucket, acmeCfg.SecretKey = "bk-acme", "s2"
	if _, err := acme.SaveBackup(ctx, acmeCfg); err != nil {
		t.Fatal(err)
	}
	if len(publicOnly) == 0 || !publicOnly[0] {
		t.Fatalf("other orgs' buckets must be public-only: %v", publicOnly)
	}
}

func stringify(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
