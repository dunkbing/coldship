package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfig(t *testing.T) {
	cfg := Config([]Route{
		{App: "web", Domains: []string{"b.example.com", "www.b.example.com"}, Upstream: "dep-2:8080"},
		{App: "api", Domains: []string{"a.example.com"}, Upstream: "dep-1:3000"},
		{App: "nodomains", Upstream: "dep-3:80"},
	}, "ops@example.com")
	b, _ := json.Marshal(cfg)
	s := string(b)
	if strings.Index(s, "a.example.com") > strings.Index(s, "b.example.com") {
		t.Error("routes should be sorted by app for a stable config")
	}
	for _, want := range []string{`"dial":"dep-2:8080"`, `"dial":"dep-1:3000"`, `"email":"ops@example.com"`, `"status_code":404`, `"listen":"localhost:2019"`} {
		if !strings.Contains(s, want) {
			t.Errorf("config missing %s:\n%s", want, s)
		}
	}
	if strings.Contains(s, "dep-3") {
		t.Error("apps without domains must not be routed")
	}
}

func TestParseHTTPStats(t *testing.T) {
	text := `# HELP caddy_http_request_duration_seconds ...
caddy_http_request_duration_seconds_count{code="200",handler="reverse_proxy",host="a.test",method="GET",server="coldship"} 7
caddy_http_request_duration_seconds_count{code="502",handler="reverse_proxy",host="a.test",method="GET",server="coldship"} 3
caddy_http_request_duration_seconds_sum{code="200",handler="reverse_proxy",host="a.test",method="GET",server="coldship"} 0.5
caddy_http_request_duration_seconds_count{code="404",handler="static_response",host="b.test",method="GET",server="coldship"} 1
caddy_http_request_duration_seconds_bucket{code="200",host="a.test",le="0.005"} 7`
	h := parseHTTPStats(text)
	a := h["a.test"]
	if a.Requests != 10 || a.ByClass["2xx"] != 7 || a.ByClass["5xx"] != 3 || a.DurationSec != 0.5 || h["b.test"].ByClass["4xx"] != 1 {
		t.Fatalf("%+v", h)
	}
}
