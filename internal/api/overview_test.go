package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// The overview works out what needs attention from how things are, and
// shows a token limited to some apps nothing about the others.
func TestOverview(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), testKeys)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rt := runtime.NewFake()
	eng := engine.New(st, rt, builder.NewFake(), nil)
	eng.SnapshotDir = t.TempDir()
	srv := httptest.NewServer(api.New(eng, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	defer srv.Close()
	adminTok, _ := eng.EnsureAdminToken(ctx)
	admin := client.New(srv.URL, adminTok)

	for _, n := range []string{"web", "api", "shop-a"} {
		p, err := admin.Plan(ctx, engine.PlanRequest{Spec: &spec.App{Name: n, Image: "nginx:1", Port: 80,
			Jobs: []spec.Job{{Name: "nightly", Schedule: "@daily", Command: "true"}}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Apply(ctx, p.ID, "test"); err != nil {
			t.Fatal(n, err)
		}
	}
	rt.Statuses = map[string]runtime.Status{ // set before any request reads it
		"api": {Exists: false},
		"web": {Exists: true, Running: true, RestartCount: 3},
	}
	if err := st.SaveJobRun(ctx, &store.JobRun{ID: "r1", Org: "default", App: "api", Job: "nightly", Trigger: "schedule", Command: "true", StartedAt: time.Now(),
		Status: store.JobFailed, Error: "exit 1"}); err != nil {
		t.Fatal(err)
	}

	get := func(tok string) api.OverviewView {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+"/v1/overview", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("overview: %d %s", resp.StatusCode, b)
		}
		var v api.OverviewView
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	v := get(adminTok)
	if len(v.Apps) != 3 || len(v.Deploys) != 3 || v.Jobs != 3 || v.Server == nil {
		t.Fatalf("admin overview: %d apps, %d deploys, %d jobs, server %v", len(v.Apps), len(v.Deploys), v.Jobs, v.Server)
	}
	kinds := map[string]string{}
	for _, a := range v.Attention {
		kinds[a.Kind+":"+a.App] = a.Severity
	}
	if kinds["app_failing:api"] != "error" || kinds["app_failing:web"] != "error" || kinds["job_failed:api"] != "warning" {
		t.Fatalf("attention: %+v", v.Attention)
	}
	if v.Attention[0].Severity != "error" || v.Attention[len(v.Attention)-1].Severity == "error" {
		t.Fatalf("errors come first: %+v", v.Attention)
	}
	if _, ok := kinds["app_failing:shop-a"]; ok {
		t.Fatalf("shop-a is healthy: %+v", v.Attention)
	}

	tr, err := admin.CreateToken(ctx, "limited", "deploy", "web", "shop-*")
	if err != nil {
		t.Fatal(err)
	}
	lv := get(tr.Token)
	if len(lv.Apps) != 2 || len(lv.Deploys) != 2 || lv.Server != nil || lv.Agents != nil {
		t.Fatalf("limited overview: %d apps, %d deploys, server %v, agents %v", len(lv.Apps), len(lv.Deploys), lv.Server, lv.Agents)
	}
	b, _ := json.Marshal(lv)
	if strings.Contains(string(b), `"api"`) || strings.Contains(string(b), "api:") || strings.Contains(string(b), "/apps/api") {
		t.Fatalf("a limited token sees another app: %s", b)
	}
}
