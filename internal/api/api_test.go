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

	"hangar/internal/api"
	"hangar/internal/apierr"
	"hangar/internal/builder"
	"hangar/internal/client"
	"hangar/internal/engine"
	"hangar/internal/runtime"
	"hangar/internal/secrets"
	"hangar/internal/spec"
	"hangar/internal/store"
	"hangar/internal/webhook"
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

	if r := do("POST", "/v1/sessions", `{"token":"hgr_wrong"}`, nil, false); r.StatusCode != 401 {
		t.Fatalf("bad token: want 401, got %d", r.StatusCode)
	}
	r := do("POST", "/v1/sessions", `{"token":"`+admin+`"}`, nil, false)
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
