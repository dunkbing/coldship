package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHub(t *testing.T) {
	e, _ := dbSetup(t)
	ctx := context.Background()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/user":
			json.NewEncoder(w).Encode(map[string]any{"login": "octo"})
		case "/user/repos":
			json.NewEncoder(w).Encode([]map[string]any{{"full_name": "octo/site", "private": true, "default_branch": "main",
				"clone_url": "https://github.com/octo/site.git", "updated_at": "2026-10-01T00:00:00Z"}})
		case "/repos/octo/site/branches":
			json.NewEncoder(w).Encode([]map[string]any{{"name": "main"}, {"name": "dev"}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer gh.Close()
	e.GitHubAPI = gh.URL

	if _, err := e.GitHubRepos(ctx); code(err) != "github_not_connected" {
		t.Fatalf("not connected: %v", err)
	}
	if _, err := e.ConnectGitHub(ctx, "admin", "bad"); code(err) != "github_token_invalid" {
		t.Fatalf("bad token: %v", err)
	}
	st, err := e.ConnectGitHub(ctx, "admin", "good")
	if err != nil || st.Account != "octo" || e.GitHubToken(ctx) != "good" {
		t.Fatalf("%+v %v", st, err)
	}
	repos, err := e.GitHubRepos(ctx)
	if err != nil || len(repos) != 1 || !repos[0].Private || repos[0].CloneURL != "https://github.com/octo/site.git" {
		t.Fatalf("%+v %v", repos, err)
	}
	br, err := e.GitHubBranches(ctx, "octo/site")
	if err != nil || len(br) != 2 {
		t.Fatalf("%v %v", br, err)
	}
	if _, err := e.GitHubBranches(ctx, "../etc"); code(err) != "invalid_request" {
		t.Fatalf("bad name: %v", err)
	}
	if err := e.DisconnectGitHub(ctx, "admin"); err != nil || e.GitHubToken(ctx) != "" {
		t.Fatal("disconnect")
	}
}
