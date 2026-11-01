package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dunkbing/coldship/internal/builder"
	"github.com/dunkbing/coldship/internal/proxy"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

const siteRepo = "https://github.com/acme/site.git"

func staticSetup(t *testing.T) (*Engine, *runtime.Fake, *proxy.Fake, *builder.Fake) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), testKeys)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rt, bd, px := runtime.NewFake(), builder.NewFake(), &proxy.Fake{Sites: t.TempDir()}
	bd.Refs[siteRepo+"@main"] = strings.Repeat("a", 40)
	bd.StaticRepos = map[string]bool{siteRepo: true}
	return New(st, rt, bd, px), rt, px, bd
}

func site(mods ...func(*spec.App)) *spec.App {
	a := &spec.App{Name: "site", Port: 80, Domains: []string{"site.example.com"},
		Build: &spec.Build{Git: siteRepo, Ref: "main"}}
	for _, m := range mods {
		m(a)
	}
	return a
}

func TestStaticSiteServedWithoutContainer(t *testing.T) {
	e, rt, px, bd := staticSetup(t)
	ctx := context.Background()

	res := deploy(t, e, site())
	d := res.Deployment
	if !d.Static || d.Strategy != "static" || d.Image != "" {
		t.Fatalf("a static site with a domain is served as files: %+v", d)
	}
	if _, ok := rt.Containers["site"]; ok {
		t.Fatal("no container for a static site")
	}
	if px.Root("site") != "site/"+d.ID {
		t.Fatalf("route root: %q", px.Root("site"))
	}
	if _, err := os.Stat(filepath.Join(px.Sites, "site", d.ID, "index.html")); err != nil {
		t.Fatal("site files missing:", err)
	}
	v, _ := e.GetApp(ctx, "site")
	if v.Runtime == nil || !v.Runtime.Running || v.Runtime.State != "static" {
		t.Fatalf("status of a static site: %+v", v.Runtime)
	}
	dg, err := e.Diagnose(ctx, "site")
	if err != nil || !dg.Runtime.Running {
		t.Fatalf("diagnose: %+v %v", dg, err)
	}

	// A new commit is a new directory; the route switches to it.
	bd.Refs[siteRepo+"@main"] = strings.Repeat("b", 40)
	d2 := deploy(t, e, site()).Deployment
	if px.Root("site") != "site/"+d2.ID {
		t.Fatal("route must switch to the new deployment")
	}

	// After a restart the route is rebuilt from the store.
	e2 := New(e.st, rt, bd, px)
	px.Routes = nil
	if err := e2.StartProxy(ctx); err != nil || px.Root("site") != "site/"+d2.ID {
		t.Fatalf("routes after restart: %v %+v", err, px.Routes)
	}

	// A host_port needs a process: it moves into a container.
	d3 := deploy(t, e, site(func(a *spec.App) { a.HostPort = 8090 })).Deployment
	if d3.Static || rt.Containers["site"].Image == "" || px.Upstream("site") == "" {
		t.Fatalf("with a host_port the site runs in a container: %+v", d3)
	}
	if _, err := os.Stat(filepath.Join(px.Sites, "site")); !os.IsNotExist(err) {
		t.Fatal("static files must go once the site runs in a container")
	}

	// And back: the container goes.
	deploy(t, e, site())
	if _, ok := rt.Containers["site"]; ok {
		t.Fatal("the container must go once the site is static again")
	}

	p, err := e.Plan(ctx, "agent", PlanRequest{Delete: "site"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, "agent", p.ID, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(px.Sites, "site")); !os.IsNotExist(err) {
		t.Fatal("deleting the app removes its files")
	}
}

func TestStaticNeedsADomain(t *testing.T) {
	e, rt, _, _ := staticSetup(t)
	d := deploy(t, e, site(func(a *spec.App) { a.Domains = nil; a.HostPort = 8090 })).Deployment
	if d.Static || rt.Containers["site"].Image == "" {
		t.Fatalf("without a domain, a static site still runs in a container: %+v", d)
	}
}
