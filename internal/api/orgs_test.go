package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dunkbing/coldship/internal/api"
	"github.com/dunkbing/coldship/internal/builder"
	"github.com/dunkbing/coldship/internal/client"
	"github.com/dunkbing/coldship/internal/engine"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

// browser is a dashboard client: cookie session plus the CSRF header.
type browser struct {
	t    *testing.T
	base string
	c    *http.Client
	tok  string // bearer token instead of a cookie, if set
}

func newBrowser(t *testing.T, base string) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: base, c: &http.Client{Jar: jar}}
}

// call sends a JSON request and returns the status and decoded body.
func (b *browser) call(method, path string, in any) (int, map[string]any) {
	b.t.Helper()
	var body io.Reader
	if in != nil {
		raw, _ := json.Marshal(in)
		body = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, b.base+path, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(api.CSRFHeader, "1")
	if b.tok != "" {
		req.Header.Set("Authorization", "Bearer "+b.tok)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if len(raw) > 0 && raw[0] == '{' {
		json.Unmarshal(raw, &out)
	} else if len(raw) > 0 {
		var list []any
		json.Unmarshal(raw, &list)
		out["list"] = list
	}
	return resp.StatusCode, out
}

func (b *browser) must(method, path string, in any) map[string]any {
	b.t.Helper()
	code, out := b.call(method, path, in)
	if code != 200 {
		b.t.Fatalf("%s %s: %d %v", method, path, code, out)
	}
	return out
}

func errOf(out map[string]any) string {
	if e, ok := out["error"].(map[string]any); ok {
		return e["code"].(string)
	}
	return ""
}

func secretOf(t *testing.T, url string) string {
	_, s, ok := strings.Cut(url, "#")
	if !ok || s == "" {
		t.Fatalf("link without a fragment secret: %s", url)
	}
	return s
}

// Orgs isolate apps, data services, plans, deployments, tokens and audit
// entries: a member of one org can't see or touch another's, by any route.
func TestOrganizations(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), testKeys)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := engine.New(st, runtime.NewFake(), builder.NewFake(), nil)
	eng.SnapshotDir = t.TempDir()
	srv := httptest.NewServer(api.New(eng, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	defer srv.Close()
	adminTok, _ := eng.EnsureAdminToken(ctx)
	admin := client.New(srv.URL, adminTok)

	// No accounts yet: the admin token's session creates the first one.
	anon := newBrowser(t, srv.URL)
	if _, out := anon.call("GET", "/v1/auth", nil); out["has_users"] != false {
		t.Fatalf("auth info: %v", out)
	}
	tokSession := newBrowser(t, srv.URL)
	tokSession.must("POST", "/v1/sessions", map[string]any{"token": adminTok})
	if code, out := tokSession.call("POST", "/v1/account", map[string]any{"email": "alice@example.com", "name": "Alice", "password": "short"}); code != 400 {
		t.Fatalf("short password: %d %v", code, out)
	}
	tokSession.must("POST", "/v1/account", map[string]any{"email": "alice@example.com", "name": "Alice", "password": "correct horse"})

	alice := newBrowser(t, srv.URL)
	if code, out := alice.call("POST", "/v1/sessions", map[string]any{"email": "alice@example.com", "password": "wrong password"}); code != 401 {
		t.Fatalf("wrong password: %d %v", code, out)
	}
	who := alice.must("POST", "/v1/sessions", map[string]any{"email": "ALICE@example.com", "password": "correct horse"})
	if who["server_admin"] != true || who["can_create_orgs"] != true || who["org"].(map[string]any)["id"] != "default" {
		t.Fatalf("alice: %v", who)
	}

	// Alice makes a second org, which her session switches to.
	o := alice.must("POST", "/v1/orgs", map[string]any{"name": "Acme Inc"})
	if o["id"] != "acme-inc" {
		t.Fatalf("org: %v", o)
	}
	if w := alice.must("GET", "/v1/whoami", nil); w["org"].(map[string]any)["id"] != "acme-inc" || w["server_admin"] != false {
		t.Fatalf("switched: %v", w)
	}
	acmeTok := alice.must("POST", "/v1/tokens", map[string]any{"name": "acme-ci", "scope": "admin"})["token"].(string)
	acme := client.New(srv.URL, acmeTok)

	// Invite Bob as a deployer; the link works once.
	inv := alice.must("POST", "/v1/org/invites", map[string]any{"role": "deploy"})
	secret := secretOf(t, inv["url"].(string))
	if info := anon.must("POST", "/v1/links/lookup", map[string]any{"secret": secret}); info["org_name"] != "Acme Inc" || info["role"] != "deploy" {
		t.Fatalf("lookup: %v", info)
	}
	bob := newBrowser(t, srv.URL)
	bw := bob.must("POST", "/v1/links/accept", map[string]any{"secret": secret, "email": "bob@example.com", "name": "Bob", "password": "bobs password"})
	if bw["scope"] != "deploy" || bw["org"].(map[string]any)["id"] != "acme-inc" {
		t.Fatalf("bob: %v", bw)
	}
	if code, out := newBrowser(t, srv.URL).call("POST", "/v1/links/accept", map[string]any{"secret": secret, "email": "eve@example.com", "name": "Eve", "password": "eves password"}); code != 404 || errOf(out) != "link_invalid" {
		t.Fatalf("reused invite: %d %v", code, out)
	}
	if code, _ := bob.call("POST", "/v1/tokens", map[string]any{"name": "bobs", "scope": "read"}); code != 403 {
		t.Fatalf("deployer made a token: %d", code)
	}

	// The default org has an app using a data service, a deployment and a
	// pending plan.
	if _, err := admin.CreateData(ctx, engine.DataRequest{Name: "maindb", Engine: "postgres"}); err != nil {
		t.Fatal(err)
	}
	p, err := admin.Plan(ctx, engine.PlanRequest{Spec: &spec.App{Name: "web", Image: "nginx", Port: 80, Uses: []string{"maindb"}}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := admin.Apply(ctx, p.ID, "create")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := admin.Plan(ctx, engine.PlanRequest{Patch: &spec.Patch{Name: "web", Env: map[string]string{"A": "1"}}})
	if err != nil {
		t.Fatal(err)
	}

	notFound := func(what string, err error) {
		t.Helper()
		if c := errCode(err); !strings.HasSuffix(c, "not_found") {
			t.Fatalf("%s from another org: want not found, got %v", what, err)
		}
	}
	if apps, err := acme.ListApps(ctx); err != nil || len(apps) != 0 {
		t.Fatalf("acme sees apps: %v %v", apps, err)
	}
	_, err = acme.GetApp(ctx, "web")
	notFound("get app", err)
	_, err = acme.Logs(ctx, "web", 10, "")
	notFound("logs", err)
	_, err = acme.Deployments(ctx, "web", 10)
	notFound("deployments", err)
	_, err = acme.Diagnose(ctx, "web")
	notFound("diagnose", err)
	_, err = acme.WebhookStatus(ctx, "web")
	notFound("webhook", err)
	_, err = acme.Rollback(ctx, "web", "", "x")
	notFound("rollback", err)
	_, err = acme.Plan(ctx, engine.PlanRequest{Patch: &spec.Patch{Name: "web", Env: map[string]string{"B": "2"}}})
	notFound("patch plan", err)
	_, err = acme.Plan(ctx, engine.PlanRequest{Delete: "web"})
	notFound("delete plan", err)
	_, err = acme.GetPlan(ctx, pending.ID)
	notFound("get plan", err)
	_, err = acme.Apply(ctx, pending.ID, "x")
	notFound("apply plan", err)
	_, err = acme.Approve(ctx, pending.ID)
	notFound("approve plan", err)
	if plans, _ := acme.ListPlans(ctx, "", 50); len(plans) != 0 {
		t.Fatalf("acme sees plans: %v", plans)
	}
	_, err = acme.DeployLog(ctx, res.Deployment.ID)
	notFound("deploy log", err)
	_, err = acme.CreatePreview(ctx, engine.PreviewRequest{App: "web", Name: "x"})
	notFound("preview", err)
	_, err = acme.GetData(ctx, "maindb")
	notFound("get data", err)
	_, err = acme.DataConnection(ctx, "maindb")
	notFound("data connection", err)
	notFound("delete data", acme.DeleteData(ctx, "maindb", "x"))
	if list, _ := acme.ListData(ctx); len(list) != 0 {
		t.Fatalf("acme sees data: %v", list)
	}
	if _, err := acme.Plan(ctx, engine.PlanRequest{Spec: &spec.App{Name: "web", Image: "nginx", Port: 80}}); errCode(err) != "name_in_use" {
		t.Fatalf("taken name: %v", err)
	}
	// Using another org's data service would inject its password.
	_, err = acme.Plan(ctx, engine.PlanRequest{Spec: &spec.App{Name: "thief", Image: "nginx", Port: 80, Uses: []string{"maindb"}}})
	notFound("uses", err)
	if toks, _ := acme.ListTokens(ctx); len(toks) != 1 || toks[0].Name != "acme-ci" {
		t.Fatalf("acme tokens: %v", toks)
	}
	notFound("revoke token", acme.RevokeToken(ctx, "admin", "x"))
	for _, e := range must(acme.Audit(ctx, 100)) {
		if e.Target == "web" || e.Target == "maindb" {
			t.Fatalf("acme sees default org's audit: %+v", e)
		}
	}
	if _, err := acme.RotateSecretKey(ctx); errCode(err) != "server_only" {
		t.Fatalf("rotate: %v", err)
	}
	if code, out := bob.call("GET", "/v1/system/status", nil); code != 403 || errOf(out) != "server_only" {
		t.Fatalf("server status: %d %v", code, out)
	}
	if code, out := bob.call("GET", "/v1/apps/web", nil); code != 404 {
		t.Fatalf("bob sees web: %d %v", code, out)
	}
	if code, _ := bob.call("GET", "/v1/deployments/"+res.Deployment.ID+"/log/stream", nil); code != 404 {
		t.Fatalf("bob streams web's log: %d", code)
	}

	// Moving: an app using data services must disconnect them first.
	alice.must("POST", "/v1/session/org", map[string]any{"org": "default"})
	if code, out := alice.call("POST", "/v1/apps/web/move", map[string]any{"org": "acme-inc"}); code != 409 || errOf(out) != "app_uses_data" {
		t.Fatalf("move with uses: %d %v", code, out)
	}
	p, _ = admin.Plan(ctx, engine.PlanRequest{Spec: &spec.App{Name: "site", Image: "nginx", Port: 80}})
	siteDep, _ := admin.Apply(ctx, p.ID, "create")
	stale, _ := admin.Plan(ctx, engine.PlanRequest{Patch: &spec.Patch{Name: "site", Env: map[string]string{"A": "1"}}})
	if code, out := bob.call("POST", "/v1/apps/site/move", map[string]any{"org": "acme-inc"}); code != 404 && code != 403 {
		t.Fatalf("bob moved: %d %v", code, out)
	}
	alice.must("POST", "/v1/apps/site/move", map[string]any{"org": "acme-inc"})
	if _, err := acme.GetApp(ctx, "site"); err != nil {
		t.Fatalf("moved app: %v", err)
	}
	_, err = admin.GetApp(ctx, "site")
	notFound("moved app from old org", err)
	// Its history moves with it.
	if _, err := acme.DeployLog(ctx, siteDep.Deployment.ID); err != nil {
		t.Fatalf("deploy log after the move: %v", err)
	}
	_, err = admin.DeployLog(ctx, siteDep.Deployment.ID)
	notFound("deploy log from old org", err)
	if _, err := acme.Apply(ctx, stale.ID, "x"); err == nil {
		t.Fatal("a plan from before the move still applies")
	}

	// Members: Bob can leave; the last admin can't be demoted.
	bobID := bw["user"].(map[string]any)["id"].(string)
	bob.must("DELETE", "/v1/org/members/"+bobID, nil)
	if code, _ := bob.call("GET", "/v1/whoami", nil); code != 401 {
		t.Fatalf("bob still signed in to acme: %d", code)
	}
	// Removed from his only org, Bob can't sign in, but rejoins through a
	// new invite with his existing account. A wrong password doesn't use
	// the link up.
	if code, out := newBrowser(t, srv.URL).call("POST", "/v1/sessions", map[string]any{"email": "bob@example.com", "password": "bobs password"}); code != 403 || errOf(out) != "no_organization" {
		t.Fatalf("bob without org: %d %v", code, out)
	}
	alice.must("POST", "/v1/session/org", map[string]any{"org": "acme-inc"})
	again := secretOf(t, alice.must("POST", "/v1/org/invites", map[string]any{"role": "read"})["url"].(string))
	bob = newBrowser(t, srv.URL)
	if code, out := bob.call("POST", "/v1/links/accept", map[string]any{"secret": again, "email": "bob@example.com", "password": "not it at all"}); code != 401 {
		t.Fatalf("rejoin with wrong password: %d %v", code, out)
	}
	if w := bob.must("POST", "/v1/links/accept", map[string]any{"secret": again, "email": "bob@example.com", "password": "bobs password"}); w["scope"] != "read" {
		t.Fatalf("rejoined: %v", w)
	}
	acme.RevokeToken(ctx, "acme-ci", "done")
	aliceID := who["user"].(map[string]any)["id"].(string)
	if code, out := alice.call("PATCH", "/v1/org/members/"+aliceID, map[string]any{"role": "read"}); code != 409 || errOf(out) != "last_admin" {
		t.Fatalf("demote last admin: %d %v", code, out)
	}

	// Password reset links come from server admins only.
	if code, _ := alice.call("POST", "/v1/org/members/"+aliceID+"/reset", nil); code != 403 {
		t.Fatalf("reset outside default org: %d", code)
	}
	alice.must("POST", "/v1/session/org", map[string]any{"org": "default"})
	reset := alice.must("POST", "/v1/org/members/"+aliceID+"/reset", nil)
	fresh := newBrowser(t, srv.URL)
	fresh.must("POST", "/v1/links/accept", map[string]any{"secret": secretOf(t, reset["url"].(string)), "password": "a new password"})
	if code, _ := alice.call("GET", "/v1/whoami", nil); code != 401 {
		t.Fatal("a reset must sign out other sessions")
	}

	// Guessing passwords gets blocked.
	for i := 0; i < 5; i++ {
		anon.call("POST", "/v1/sessions", map[string]any{"email": "alice@example.com", "password": "guess"})
	}
	if code, out := anon.call("POST", "/v1/sessions", map[string]any{"email": "alice@example.com", "password": "a new password"}); code != 429 {
		t.Fatalf("rate limit: %d %v", code, out)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
