package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dunkbing/coldship/internal/api"
	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/builder"
	"github.com/dunkbing/coldship/internal/client"
	"github.com/dunkbing/coldship/internal/engine"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/secrets"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
	"github.com/dunkbing/coldship/internal/webhook"
)

// testKeys encrypts every test database; tests that reopen a database reuse it.
var testKeys = secrets.New()

// End to end through HTTP: scoped tokens, plan/apply, and agent-blocked approval.
func TestAPIEndToEnd(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), testKeys)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := engine.New(st, runtime.NewFake(), builder.NewFake(), nil)
	srv := httptest.NewServer(api.New(eng, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
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

func TestDashboardSessions(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), testKeys)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := engine.New(st, runtime.NewFake(), builder.NewFake(), nil)
	srv := httptest.NewServer(api.New(eng, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	defer srv.Close()
	admin, _ := eng.EnsureAdminToken(ctx)

	do := func(method, path, body string, cookie *http.Cookie, csrf bool) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if csrf {
			req.Header.Set(api.CSRFHeader, "1")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	if r := do("POST", "/v1/sessions", `{"token":"cst_wrong"}`, nil, true); r.StatusCode != 401 {
		t.Fatalf("bad token: want 401, got %d", r.StatusCode)
	}
	if r := do("POST", "/v1/sessions", `{"token":"`+admin+`"}`, nil, false); r.StatusCode != 403 {
		t.Fatalf("sign-in without the CSRF header (login CSRF): want 403, got %d", r.StatusCode)
	}
	r := do("POST", "/v1/sessions", `{"token":"`+admin+`"}`, nil, true)
	var c *http.Cookie
	for _, x := range r.Cookies() {
		if x.Name == api.SessionCookie {
			c = x
		}
	}
	if r.StatusCode != 200 || c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("login should set an HttpOnly SameSite=Strict cookie: %d %+v", r.StatusCode, c)
	}
	if c.Value == admin {
		t.Fatal("the cookie must not contain the API token")
	}

	if r := do("GET", "/v1/whoami", "", c, false); r.StatusCode != 200 {
		t.Fatalf("GET with session cookie: want 200, got %d", r.StatusCode)
	}
	plan := `{"spec":{"name":"web","image":"nginx"}}`
	if r := do("POST", "/v1/plans", plan, c, false); r.StatusCode != 403 {
		t.Fatalf("cookie POST without CSRF header: want 403, got %d", r.StatusCode)
	}
	if r := do("POST", "/v1/plans", plan, c, true); r.StatusCode != 200 {
		t.Fatalf("cookie POST with CSRF header: want 200, got %d", r.StatusCode)
	}

	if r := do("DELETE", "/v1/sessions", "", c, true); r.StatusCode != 200 {
		t.Fatalf("sign out: want 200, got %d", r.StatusCode)
	}
	if r := do("GET", "/v1/whoami", "", c, false); r.StatusCode != 401 {
		t.Fatalf("after sign out the session must be dead, got %d", r.StatusCode)
	}

	// Revoking a token kills it and every session signed in with it.
	ac := client.New(srv.URL, admin)
	ci, err := ac.CreateToken(ctx, "ci", "deploy")
	if err != nil {
		t.Fatal(err)
	}
	r = do("POST", "/v1/sessions", `{"token":"`+ci.Token+`"}`, nil, true)
	var ciCookie *http.Cookie
	for _, x := range r.Cookies() {
		if x.Name == api.SessionCookie {
			ciCookie = x
		}
	}
	if ciCookie == nil {
		t.Fatal("no session for the ci token")
	}
	if err := ac.RevokeToken(ctx, "ci", "rotated"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.New(srv.URL, ci.Token).ListApps(ctx); errCode(err) != "unauthenticated" {
		t.Fatalf("revoked token: want unauthenticated, got %v", err)
	}
	if r := do("GET", "/v1/whoami", "", ciCookie, false); r.StatusCode != 401 {
		t.Fatalf("a revoked token's session must be dead, got %d", r.StatusCode)
	}
	if err := ac.RevokeToken(ctx, "admin", ""); errCode(err) != "last_admin_token" {
		t.Fatalf("revoking the only admin token: want last_admin_token, got %v", err)
	}
	if _, err := ac.CreateToken(ctx, "webhook", "deploy"); errCode(err) != "invalid_token_name" {
		t.Fatalf("reserved token name: want invalid_token_name, got %v", err)
	}
	toks, err := ac.ListTokens(ctx)
	if err != nil || len(toks) != 1 || toks[0].Name != "admin" {
		t.Fatalf("tokens after revoke: %+v %v", toks, err)
	}
}

func TestGitHookEndpoint(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), testKeys)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bd := builder.NewFake()
	bd.Refs["https://github.com/acme/shop.git@main"] = strings.Repeat("a", 40)
	eng := engine.New(st, runtime.NewFake(), bd, nil)
	srv := httptest.NewServer(api.New(eng, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	defer srv.Close()
	adminTok, _ := eng.EnsureAdminToken(ctx)
	admin := client.New(srv.URL, adminTok)
	shop := &spec.App{Name: "shop", Image: "", Port: 80, Build: &spec.Build{Git: "https://github.com/acme/shop.git", Ref: "main"},
		OnPush: &spec.OnPush{Action: "plan"}}
	p, err := admin.Plan(ctx, engine.PlanRequest{Spec: shop})
	if err != nil {
		t.Fatal(err)
	}
	admin.Apply(ctx, p.ID, "create")

	post := func(body []byte, h map[string]string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/hooks/git/shop", strings.NewReader(string(body)))
		for k, v := range h {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	body := []byte(`{"ref":"refs/heads/main","after":"` + strings.Repeat("b", 40) + `","repository":{"clone_url":"https://github.com/acme/shop.git"}}`)
	if c := post(body, map[string]string{"X-GitHub-Event": "push"}); c != 404 {
		t.Fatalf("webhook not enabled yet: want 404, got %d", c)
	}

	tr, _ := admin.CreateToken(ctx, "reader", "read")
	if _, err := client.New(srv.URL, tr.Token).EnableWebhook(ctx, "shop", false); errCode(err) != "insufficient_scope" {
		t.Fatalf("only admins may create webhook secrets, got %v", err)
	}
	info, err := admin.EnableWebhook(ctx, "shop", false)
	if err != nil || len(info.Secret) != 64 || !strings.HasSuffix(info.URL, "/hooks/git/shop") {
		t.Fatalf("enable: %+v %v", info, err)
	}
	if again, _ := admin.WebhookStatus(ctx, "shop"); again.Secret != "" {
		t.Fatal("the secret must only be shown when created")
	}
	if _, err := admin.EnableWebhook(ctx, "shop", false); errCode(err) != "webhook_exists" {
		t.Fatalf("want webhook_exists, got %v", err)
	}

	bd.Refs["https://github.com/acme/shop.git@main"] = strings.Repeat("b", 40) // the pushed commit is the branch tip
	sig := "sha256=" + webhook.Sign(body, info.Secret)
	if c := post(body, map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": "sha256=" + webhook.Sign(body, "guess")}); c != 401 {
		t.Fatalf("bad signature: want 401, got %d", c)
	}
	if c := post(body, map[string]string{"X-GitHub-Event": "push", "X-GitHub-Delivery": "abc", "X-Hub-Signature-256": sig}); c != 202 {
		t.Fatalf("valid push: want 202, got %d", c)
	}
	if c := post(body, map[string]string{"X-GitHub-Event": "push", "X-GitHub-Delivery": "abc", "X-Hub-Signature-256": sig}); c != 200 {
		t.Fatalf("redelivery: want 200 duplicate, got %d", c)
	}
	eng.WaitHooks()
	status, _ := admin.WebhookStatus(ctx, "shop")
	if len(status.Deliveries) != 1 || status.Deliveries[0].Result != "planned" {
		t.Fatalf("deliveries: %+v", status.Deliveries)
	}

	rotated, _ := admin.EnableWebhook(ctx, "shop", true)
	if c := post(body, map[string]string{"X-GitHub-Event": "push", "X-GitHub-Delivery": "def", "X-Hub-Signature-256": sig}); c != 401 {
		t.Fatalf("the old secret must stop working after rotation, got %d", c)
	}
	_ = rotated
}

// A token limited to some apps can't see or change any other app, by any
// route, including the dashboard's session.
func TestTokensLimitedToApps(t *testing.T) {
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

	deploy := func(c *client.Client, name string) (*engine.ApplyResult, error) {
		p, err := c.Plan(ctx, engine.PlanRequest{Spec: &spec.App{Name: name, Image: "nginx:1", Port: 80}})
		if err != nil {
			return nil, err
		}
		return c.Apply(ctx, p.ID, "test")
	}
	var apiDep *engine.ApplyResult
	for _, n := range []string{"web", "api", "shop-a"} {
		res, err := deploy(admin, n)
		if err != nil {
			t.Fatal(n, err)
		}
		if n == "api" {
			apiDep = res
		}
	}
	apiPlan, _ := admin.Plan(ctx, engine.PlanRequest{Spec: &spec.App{Name: "api", Image: "nginx:2", Port: 80}})

	if _, err := admin.CreateToken(ctx, "boss", "admin", "web"); errCode(err) != "invalid_request" {
		t.Fatalf("admin tokens can't be limited: %v", err)
	}
	if _, err := admin.CreateToken(ctx, "bad", "deploy", "*"); errCode(err) != "invalid_request" {
		t.Fatalf("a lone * means every app; leave apps out instead: %v", err)
	}
	tr, err := admin.CreateToken(ctx, "agent", "deploy", "web", "shop-*")
	if err != nil {
		t.Fatal(err)
	}
	agent := client.New(srv.URL, tr.Token)
	denied := func(what string, err error) {
		t.Helper()
		if errCode(err) != "app_not_allowed" {
			t.Errorf("%s: want app_not_allowed, got %v", what, err)
		}
	}

	if me, err := agent.WhoAmI(ctx); err != nil || len(me.Apps) != 2 {
		t.Fatalf("whoami: %+v %v", me, err)
	}
	apps, _ := agent.ListApps(ctx)
	if len(apps) != 2 || apps[0].Name == "api" || apps[1].Name == "api" {
		t.Fatalf("list must hide other apps: %+v", apps)
	}
	_, err = agent.GetApp(ctx, "api")
	denied("get", err)
	_, err = agent.Logs(ctx, "api", 10, "")
	denied("logs", err)
	_, err = agent.Rollback(ctx, "api", "", "x")
	denied("rollback", err)
	_, err = agent.DeployLog(ctx, apiDep.Deployment.ID)
	denied("deploy log", err)
	_, err = deploy(agent, "api")
	denied("plan another app", err)
	_, err = agent.Plan(ctx, engine.PlanRequest{Delete: "api"})
	denied("delete another app", err)
	_, err = agent.Plan(ctx, engine.PlanRequest{Patch: &spec.Patch{Name: "api", Env: map[string]string{"X": "1"}}})
	denied("patch another app", err)
	_, err = agent.GetPlan(ctx, apiPlan.ID)
	denied("read another app's plan", err)
	_, err = agent.Apply(ctx, apiPlan.ID, "sneaky")
	denied("apply another app's plan", err)
	_, err = deploy(agent, "other")
	denied("create an app outside the patterns", err)

	if _, err := deploy(agent, "web"); err != nil {
		t.Fatalf("own app: %v", err)
	}
	if _, err := deploy(agent, "shop-new"); err != nil {
		t.Fatalf("new app matching a pattern: %v", err)
	}
	plans, _ := agent.ListPlans(ctx, "", 100)
	for _, p := range plans {
		if p.App == "api" {
			t.Fatal("plan list must hide other apps' plans")
		}
	}
	entries, _ := agent.Audit(ctx, 1000)
	for _, e := range entries {
		if e.Target == "api" {
			t.Fatal("audit must hide other apps' entries")
		}
	}

	// Previews of allowed apps are allowed; of others, not.
	pv, err := agent.CreatePreview(ctx, engine.PreviewRequest{App: "web", Image: "nginx:2", Name: "pr-1"})
	if err != nil {
		t.Fatalf("preview of own app: %v", err)
	}
	if _, err := agent.GetApp(ctx, pv.App); err != nil {
		t.Fatalf("own preview: %v", err)
	}
	_, err = agent.CreatePreview(ctx, engine.PreviewRequest{App: "api", Image: "nginx:2", Name: "pr-1"})
	denied("preview of another app", err)

	// The dashboard session carries the same limits.
	req, _ := http.NewRequest("POST", srv.URL+"/v1/sessions", strings.NewReader(`{"token":"`+tr.Token+`"}`))
	req.Header.Set(api.CSRFHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == api.SessionCookie {
			cookie = c
		}
	}
	req, _ = http.NewRequest("GET", srv.URL+"/v1/apps/api", nil)
	req.AddCookie(cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("session for a limited token: want 403 for another app, got %d", resp.StatusCode)
	}
}

func TestDataServiceAPI(t *testing.T) {
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
	tr, _ := admin.CreateToken(ctx, "agent", "deploy", "agent-*")
	agent := client.New(srv.URL, tr.Token)

	if _, err := admin.CreateData(ctx, engine.DataRequest{Name: "shared", Engine: "postgres"}); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.CreateData(ctx, engine.DataRequest{Name: "agent-db", Engine: "redis"}); err != nil {
		t.Fatalf("a limited token creates services matching its patterns: %v", err)
	}
	if _, err := agent.CreateData(ctx, engine.DataRequest{Name: "other", Engine: "redis"}); errCode(err) != "app_not_allowed" {
		t.Fatalf("outside its patterns: %v", err)
	}
	list, _ := agent.ListData(ctx)
	if len(list) != 1 || list[0].Name != "agent-db" {
		t.Fatalf("a limited token sees only its services: %+v", list)
	}
	if _, err := agent.GetData(ctx, "shared"); errCode(err) != "app_not_allowed" {
		t.Fatalf("get another service: %v", err)
	}
	if _, err := agent.DataConnection(ctx, "agent-db"); errCode(err) != "insufficient_scope" {
		t.Fatalf("credentials are admin-only: %v", err)
	}
	if err := agent.DeleteData(ctx, "agent-db", ""); errCode(err) != "insufficient_scope" {
		t.Fatalf("delete is admin-only: %v", err)
	}
	env, err := admin.DataConnection(ctx, "shared")
	if err != nil || !strings.HasPrefix(env["DATABASE_URL"], "postgres://app:") {
		t.Fatalf("connection: %v %v", env, err)
	}
	if err := admin.DeleteData(ctx, "shared", "test"); err != nil {
		t.Fatal(err)
	}
}
