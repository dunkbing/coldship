package spec

import (
	"testing"

	"hangar/internal/apierr"
)

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte("name: web\nimage: nginx\nimgae: typo\n"))
	if ae, ok := apierr.As(err); !ok || ae.Code != "invalid_spec" {
		t.Fatalf("want invalid_spec, got %v", err)
	}
}

func TestValidateSuggestsFixes(t *testing.T) {
	cases := []struct {
		app   App
		field string
		fix   map[string]any
	}{
		{App{Name: "My App", Image: "x"}, "name", map[string]any{"name": "my-app"}},
		{App{Name: "web", Image: "x", HostPort: 8080}, "port", map[string]any{"port": 8080}},
		{App{Name: "web", Image: "x", Volumes: []Volume{{Name: "data", Path: "data"}}}, "volumes[0].path", map[string]any{"path": "/data"}},
	}
	for _, c := range cases {
		c.app.Normalize()
		ae, ok := apierr.As(c.app.Validate())
		if !ok || ae.Field != c.field {
			t.Fatalf("%+v: want field %s, got %v", c.app, c.field, ae)
		}
		got := ae.SuggestedFix.(map[string]any)
		for k, v := range c.fix {
			if got[k] != v {
				t.Fatalf("%+v: want fix %v, got %v", c.app, c.fix, got)
			}
		}
	}
}

func TestRedactedDoesNotMutate(t *testing.T) {
	a := &App{Name: "web", Image: "x", Env: map[string]string{"K": "secret"}}
	r := a.Redacted()
	if r.Env["K"] != RedactedValue || a.Env["K"] != "secret" {
		t.Fatal("redaction must copy")
	}
}

func TestBuildValidation(t *testing.T) {
	ok := []string{
		"https://github.com/acme/api.git",
		"http://gitea.internal:3000/acme/api",
		"ssh://git@github.com/acme/api.git",
		"git@github.com:acme/api.git",
	}
	for _, u := range ok {
		a := App{Name: "api", Build: &Build{Git: u}}
		a.Normalize()
		if err := a.Validate(); err != nil {
			t.Errorf("%s should be valid: %v", u, err)
		}
	}
	bad := map[string]string{
		"/srv/repos/api":                              "build.git",
		"file:///srv/repos/api":                       "build.git",
		"ext::sh -c touch% /tmp/pwned":                "build.git",
		"-uhttps://evil":                              "build.git",
		"https://user:ghp_secret@github.com/acme/api": "build.git",
		"https://ghp_secret@github.com/acme/api":      "build.git",
	}
	for u, field := range bad {
		a := App{Name: "api", Build: &Build{Git: u}}
		a.Normalize()
		ae, ok := apierr.As(a.Validate())
		if !ok || ae.Field != field {
			t.Errorf("%s: want error on %s, got %v", u, field, ae)
		}
	}
	cases := []struct {
		b     Build
		field string
	}{
		{Build{Git: ok[0], Ref: "--upload-pack=x"}, "build.ref"},
		{Build{Git: ok[0], Ref: "main..dev"}, "build.ref"},
		{Build{Git: ok[0], Commit: "abc123"}, "build.commit"},
		{Build{Git: ok[0], Context: "../.."}, "build.context"},
		{Build{Git: ok[0], Dockerfile: "/etc/passwd"}, "build.dockerfile"},
		{Build{Git: ok[0], Args: map[string]string{"bad-key": "x"}}, "build.args.bad-key"},
	}
	for _, c := range cases {
		a := App{Name: "api", Build: &c.b}
		a.Normalize()
		ae, ok := apierr.As(a.Validate())
		if !ok || ae.Field != c.field {
			t.Errorf("%+v: want error on %s, got %v", c.b, c.field, ae)
		}
	}
	both := App{Name: "api", Image: "nginx", Build: &Build{Git: ok[0]}}
	both.Normalize()
	if both.Validate() == nil {
		t.Error("image and build together must be rejected")
	}
}

func TestDomainsAndHealthValidation(t *testing.T) {
	cases := []struct {
		app   App
		field string
	}{
		{App{Name: "w", Image: "x", Port: 80, Domains: []string{"https://shop.example.com/"}}, "domains[0]"},
		{App{Name: "w", Image: "x", Port: 80, Domains: []string{"Shop.Example.com"}}, "domains[0]"},
		{App{Name: "w", Image: "x", Port: 80, Domains: []string{"*.example.com"}}, "domains[0]"},
		{App{Name: "w", Image: "x", Port: 80, Domains: []string{"localhost"}}, "domains[0]"},
		{App{Name: "w", Image: "x", Port: 80, Domains: []string{"a.example.com", "a.example.com"}}, "domains[1]"},
		{App{Name: "w", Image: "x", Domains: []string{"a.example.com"}}, "port"},
		{App{Name: "w", Image: "x", Port: 80, Health: &Health{Path: "healthz"}}, "health.path"},
		{App{Name: "w", Image: "x", Port: 80, Health: &Health{Timeout: "1h"}}, "health.timeout"},
	}
	for _, c := range cases {
		c.app.Normalize()
		ae, ok := apierr.As(c.app.Validate())
		if !ok || ae.Field != c.field {
			t.Errorf("%+v: want error on %s, got %v", c.app, c.field, ae)
		}
	}
	ok := App{Name: "w", Image: "x", Port: 80, Domains: []string{"shop.example.com", "app.localhost"}, Health: &Health{Path: "/healthz", Timeout: "90s"}}
	ok.Normalize()
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	withScheme := App{Name: "w", Image: "x", Port: 80, Domains: []string{"https://shop.example.com/"}}
	withScheme.Normalize()
	fix, _ := apierr.As(withScheme.Validate())
	if fix.SuggestedFix.(map[string]any)["domains"].([]string)[0] != "shop.example.com" {
		t.Errorf("fix = %v", fix.SuggestedFix)
	}
}
