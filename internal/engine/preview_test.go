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

const repo = "https://github.com/acme/shop.git"

func previewSetup(t *testing.T) (*Engine, *runtime.Fake, *builder.Fake) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rt, bd := runtime.NewFake(), builder.NewFake()
	e := New(st, rt, bd, &proxy.Fake{})
	e.Previews.Domain = "previews.example.com"
	bd.Refs[repo+"@main"] = strings.Repeat("a", 40)
	bd.Refs[repo+"@feat/login"] = strings.Repeat("b", 40)
	return e, rt, bd
}

func shop() *spec.App {
	return &spec.App{Name: "shop", Stage: spec.StageProduction, Port: 8080, HostPort: 8080,
		Domains: []string{"shop.example.com"}, Env: map[string]string{"DATABASE_URL": "postgres://prod"},
		Volumes: []spec.Volume{{Name: "uploads", Path: "/uploads"}},
		Build:   &spec.Build{Git: repo, Ref: "main"}}
}

func TestCreatePreview(t *testing.T) {
	e, rt, _ := previewSetup(t)
	ctx := context.Background()
	deploy(t, e, shop())

	res, err := e.CreatePreview(ctx, "agent", PreviewRequest{App: "shop", Ref: "feat/login", TTL: "2h",
		Env: map[string]string{"FEATURE_LOGIN": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.App != "shop-feat-login" || !res.Created || res.PreviewOf != "shop" {
		t.Fatalf("unexpected result %+v", res)
	}
	if len(res.URLs) != 1 || res.URLs[0] != "https://shop-feat-login.previews.example.com" {
		t.Fatalf("urls = %v", res.URLs)
	}
	if d := time.Until(res.ExpiresAt); d < 119*time.Minute || d > 121*time.Minute {
		t.Fatalf("expires in %s, want ~2h", d)
	}
	got := rt.Containers["shop-feat-login"]
	if got.Stage != spec.StagePreview || got.HostPort != 0 || len(got.Domains) != 1 || got.Domains[0] != "shop-feat-login.previews.example.com" {
		t.Fatalf("preview spec should be a preview without the parent's host port and domains: %+v", got)
	}
	if _, leaked := got.Env["DATABASE_URL"]; leaked {
		t.Fatal("a preview of a production app must not inherit its env by default")
	}
	if got.Env["FEATURE_LOGIN"] != "1" {
		t.Fatal("env overrides must apply")
	}
	if res.Deployment.Commit != strings.Repeat("b", 40) {
		t.Fatalf("the preview should build the ref's commit, got %s", res.Deployment.Commit)
	}
	app, _ := e.GetApp(ctx, "shop-feat-login")
	if app.PreviewOf != "shop" || app.ExpiresAt == nil {
		t.Fatalf("preview metadata missing: %+v", app.App)
	}

	// Same request again: nothing to deploy, but the expiry is reset.
	// (Requests are declarative: leaving out env would plan its removal.)
	res2, err := e.CreatePreview(ctx, "agent", PreviewRequest{App: "shop", Ref: "feat/login", TTL: "5h",
		Env: map[string]string{"FEATURE_LOGIN": "1"}})
	if err != nil || res2.Created || res2.Plan.Action != "noop" || time.Until(res2.ExpiresAt) < 299*time.Minute {
		t.Fatalf("re-create should extend without redeploying: %+v %v", res2, err)
	}

	// Opting in copies the env.
	in := true
	if _, err := e.CreatePreview(ctx, "agent", PreviewRequest{App: "shop", Ref: "main", Name: "with-env", InheritEnv: &in}); err != nil {
		t.Fatal(err)
	}
	if rt.Containers["shop-with-env"].Env["DATABASE_URL"] != "postgres://prod" {
		t.Fatal("inherit_env should copy the env")
	}
}

func TestPreviewGuards(t *testing.T) {
	e, _, _ := previewSetup(t)
	ctx := context.Background()
	deploy(t, e, shop())
	deploy(t, e, &spec.App{Name: "img", Image: "nginx:1", Port: 80})

	_, err := e.CreatePreview(ctx, "agent", PreviewRequest{App: "img", Ref: "main"})
	if ae, _ := apierr.As(err); code(err) != "invalid_request" || ae.SuggestedFix == nil {
		t.Fatalf("ref on an image app: want invalid_request with a fix, got %v", err)
	}
	if _, err := e.CreatePreview(ctx, "agent", PreviewRequest{App: "shop", Image: "x"}); code(err) != "invalid_request" {
		t.Fatalf("image on a git app: got %v", err)
	}
	if _, err := e.CreatePreview(ctx, "agent", PreviewRequest{App: "shop", Ref: "nope"}); code(err) != "git_ref_not_found" {
		t.Fatalf("unknown ref: got %v", err)
	}
	if _, err := e.CreatePreview(ctx, "agent", PreviewRequest{App: "shop", Ref: "main", TTL: "720h"}); code(err) != "invalid_request" {
		t.Fatalf("ttl over the max: got %v", err)
	}
	if _, err := e.DeletePreview(ctx, "agent", "shop", ""); code(err) != "not_a_preview" {
		t.Fatalf("deleting a regular app as a preview: got %v", err)
	}

	res, err := e.CreatePreview(ctx, "agent", PreviewRequest{App: "img", Image: "nginx:2", Name: "next"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreatePreview(ctx, "agent", PreviewRequest{App: res.App, Image: "x", Name: "y"}); code(err) != "invalid_request" {
		t.Fatalf("previews of previews must be refused, got %v", err)
	}
	prod := &spec.App{Name: res.App, Stage: spec.StageProduction, Image: "nginx:2", Port: 80}
	if _, err := e.Plan(ctx, "agent", PlanRequest{Spec: prod}); code(err) != "invalid_spec" {
		t.Fatalf("a preview must not be promoted to production through a plan, got %v", err)
	}
}

func TestPreviewLimitAndReaper(t *testing.T) {
	e, rt, _ := previewSetup(t)
	ctx := context.Background()
	e.Previews.Max = 2
	deploy(t, e, &spec.App{Name: "img", Image: "nginx:1", Port: 80})
	for _, n := range []string{"a", "b"} {
		if _, err := e.CreatePreview(ctx, "agent", PreviewRequest{App: "img", Image: "nginx:2", Name: n, TTL: "1h"}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := e.CreatePreview(ctx, "agent", PreviewRequest{App: "img", Image: "nginx:2", Name: "c"})
	if ae, _ := apierr.As(err); code(err) != "preview_limit" || len(ae.Details["previews"].([]string)) != 2 {
		t.Fatalf("want preview_limit listing the previews, got %v", err)
	}
	// Updating an existing preview is not blocked by the limit.
	if _, err := e.CreatePreview(ctx, "agent", PreviewRequest{App: "img", Image: "nginx:3", Name: "a", TTL: "3h"}); err != nil {
		t.Fatal(err)
	}

	e.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	deleted, err := e.ReapPreviews(ctx)
	if err != nil || len(deleted) != 1 || deleted[0] != "img-b" {
		t.Fatalf("only the expired preview should go: %v %v", deleted, err)
	}
	if _, ok := rt.Containers["img-b"]; ok {
		t.Fatal("expired preview's container should be removed")
	}
	if _, ok := rt.Containers["img"]; !ok {
		t.Fatal("regular apps must never be reaped")
	}
	entries, _ := e.Audit(ctx, 10)
	if entries[0].Actor != "hangar" || entries[0].Reason != "preview expired" {
		t.Fatalf("the reaper should be audited: %+v", entries[0])
	}
}

func TestFailedPreviewStillExpires(t *testing.T) {
	e, rt, _ := previewSetup(t)
	ctx := context.Background()
	deploy(t, e, &spec.App{Name: "img", Image: "nginx:1", Port: 80})
	rt.FailNext = apierr.New(502, "health_check_failed", "never healthy")
	if _, err := e.CreatePreview(ctx, "agent", PreviewRequest{App: "img", Image: "broken", Name: "x", TTL: "1h"}); code(err) != "health_check_failed" {
		t.Fatalf("want the deploy error, got %v", err)
	}
	e.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if deleted, _ := e.ReapPreviews(ctx); len(deleted) != 1 {
		t.Fatal("a preview whose first deploy failed must still be cleaned up")
	}
}

func TestPreviewName(t *testing.T) {
	if got := previewName("shop", "Feat/Login_Page!"); got != "shop-feat-login-page" {
		t.Errorf("got %s", got)
	}
	long := previewName("storefront-api", "feature/some-really-long-branch-name-for-testing")
	if len(long) > 40 || !strings.HasPrefix(long, "storefront-api-") {
		t.Errorf("long name %q (%d chars)", long, len(long))
	}
	a := &spec.App{Name: long, Image: "x"}
	a.Normalize()
	if err := a.Validate(); err != nil {
		t.Errorf("generated name must be a valid app name: %v", err)
	}
}
