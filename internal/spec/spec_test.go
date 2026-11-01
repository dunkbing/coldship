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
