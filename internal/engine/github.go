package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/store"
)

// GitHub lets the dashboard list repositories and deploy private ones,
// with a token the admin pastes in. It's stored encrypted.

const githubAPI = "https://api.github.com"

// githubName is an owner or repository name; "." and ".." are not names.
var githubName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}$`)

type GitHubStatus struct {
	Connected bool      `json:"connected"`
	Account   string    `json:"account,omitempty"`
	Since     time.Time `json:"since,omitempty"`
}

type GitHubRepo struct {
	FullName      string    `json:"full_name"`
	Private       bool      `json:"private"`
	DefaultBranch string    `json:"default_branch"`
	CloneURL      string    `json:"clone_url"`
	Description   string    `json:"description,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (e *Engine) githubBase() string {
	if e.GitHubAPI != "" {
		return e.GitHubAPI
	}
	return githubAPI
}

// githubGet calls the GitHub API with token and decodes the JSON reply.
func (e *Engine) githubGet(ctx context.Context, token, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", e.githubBase()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return apierr.New(502, "github_unreachable", "can't reach GitHub: %v", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 401:
		return apierr.New(401, "github_token_invalid", "GitHub rejected the token").
			WithHint("create a new token (fine-grained, with read access to Contents and Metadata) and connect again")
	case resp.StatusCode == 404:
		return apierr.New(404, "github_not_found", "GitHub has no %s for this token", path)
	case resp.StatusCode >= 300:
		return apierr.New(502, "github_error", "GitHub answered %s for %s", resp.Status, path)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// GitHubToken is the connected token, or "" (for the builder).
func (e *Engine) GitHubToken(ctx context.Context) string {
	in, err := e.st.GetIntegration(ctx, "github")
	if err != nil || in == nil {
		return ""
	}
	return in.Secret
}

func (e *Engine) GitHubStatus(ctx context.Context) (GitHubStatus, error) {
	in, err := e.st.GetIntegration(ctx, "github")
	if err != nil || in == nil {
		return GitHubStatus{}, err
	}
	return GitHubStatus{Connected: true, Account: in.Account, Since: in.CreatedAt}, nil
}

// ConnectGitHub checks the token with GitHub, then stores it.
func (e *Engine) ConnectGitHub(ctx context.Context, actor, token string) (GitHubStatus, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return GitHubStatus{}, apierr.New(400, "invalid_request", "give a GitHub token").WithField("token")
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := e.githubGet(ctx, token, "/user", &user); err != nil {
		return GitHubStatus{}, err
	}
	in := &store.Integration{Name: "github", Secret: token, Account: user.Login, CreatedAt: e.Now()}
	if err := e.st.SaveIntegration(ctx, in); err != nil {
		return GitHubStatus{}, err
	}
	e.audit(ctx, actor, "github.connect", user.Login, "", nil)
	return GitHubStatus{Connected: true, Account: user.Login, Since: in.CreatedAt}, nil
}

func (e *Engine) DisconnectGitHub(ctx context.Context, actor string) error {
	if err := e.st.DeleteIntegration(ctx, "github"); err != nil {
		return err
	}
	e.audit(ctx, actor, "github.disconnect", "github", "", nil)
	return nil
}

func (e *Engine) githubTokenOrErr(ctx context.Context) (string, error) {
	t := e.GitHubToken(ctx)
	if t == "" {
		return "", apierr.New(409, "github_not_connected", "GitHub isn't connected").
			WithHint("an admin connects it in the dashboard (Apps → New app → GitHub) with a token")
	}
	return t, nil
}

// GitHubRepos lists the repositories the token can read, recently updated first.
func (e *Engine) GitHubRepos(ctx context.Context) ([]GitHubRepo, error) {
	token, err := e.githubTokenOrErr(ctx)
	if err != nil {
		return nil, err
	}
	var all []GitHubRepo
	for page := 1; page <= 5; page++ {
		var repos []GitHubRepo
		if err := e.githubGet(ctx, token, fmt.Sprintf("/user/repos?per_page=100&sort=updated&page=%d", page), &repos); err != nil {
			return nil, err
		}
		all = append(all, repos...)
		if len(repos) < 100 {
			break
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].UpdatedAt.After(all[j].UpdatedAt) })
	return all, nil
}

// GitHubBranches lists a repository's branches.
func (e *Engine) GitHubBranches(ctx context.Context, fullName string) ([]string, error) {
	token, err := e.githubTokenOrErr(ctx)
	if err != nil {
		return nil, err
	}
	owner, repo, ok := strings.Cut(fullName, "/")
	if !ok || !githubName.MatchString(owner) || !githubName.MatchString(repo) {
		return nil, apierr.New(400, "invalid_request", "repository %q must be owner/name", fullName)
	}
	var branches []struct {
		Name string `json:"name"`
	}
	if err := e.githubGet(ctx, token, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/branches?per_page=100", &branches); err != nil {
		return nil, err
	}
	names := make([]string, len(branches))
	for i, b := range branches {
		names[i] = b.Name
	}
	return names, nil
}
