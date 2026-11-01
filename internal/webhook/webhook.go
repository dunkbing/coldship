// Package webhook verifies and parses Git push webhooks from GitHub, GitLab,
// Gitea/Forgejo, and a generic GitHub-shaped format.
//
// Verification always happens on the raw body, before anything is parsed.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// Push is a provider-independent push or branch deletion.
type Push struct {
	Provider      string
	Event         string // push | ping | other
	Delivery      string // provider's delivery id, for de-duplication
	Branch        string // set for branch pushes
	Tag           string // set for tag pushes
	After         string // pushed commit SHA ("" when deleted)
	Deleted       bool
	RepoURLs      []string
	DefaultBranch string
	Pusher        string
	Message       string // head commit message: untrusted
}

var (
	ErrNoSignature  = errors.New("the request carries no recognized webhook signature")
	ErrBadSignature = errors.New("the webhook signature does not match this app's secret")
)

// Parse identifies the provider from the headers, verifies the request
// against secret, and parses the payload.
func Parse(h http.Header, body []byte, secret string) (*Push, error) {
	var p *Push
	var err error
	switch {
	case h.Get("X-Gitlab-Event") != "":
		if !equal(h.Get("X-Gitlab-Token"), secret) {
			return nil, sigErr(h.Get("X-Gitlab-Token"))
		}
		p, err = parseGitLab(h, body)
	case h.Get("X-Gitea-Event") != "" || h.Get("X-Forgejo-Event") != "":
		sig := first(h.Get("X-Forgejo-Signature"), h.Get("X-Gitea-Signature"))
		if !validHMAC(body, secret, sig) {
			return nil, sigErr(sig)
		}
		p, err = parseGitHubShaped(body)
		p.Provider = "gitea"
		p.Event = normEvent(first(h.Get("X-Forgejo-Event"), h.Get("X-Gitea-Event")))
		p.Delivery = first(h.Get("X-Forgejo-Delivery"), h.Get("X-Gitea-Delivery"))
	case h.Get("X-GitHub-Event") != "":
		sig := strings.TrimPrefix(h.Get("X-Hub-Signature-256"), "sha256=")
		if !validHMAC(body, secret, sig) {
			return nil, sigErr(sig)
		}
		p, err = parseGitHubShaped(body)
		p.Provider = "github"
		p.Event = normEvent(h.Get("X-GitHub-Event"))
		p.Delivery = h.Get("X-GitHub-Delivery")
	case h.Get("X-Hangar-Signature") != "":
		sig := strings.TrimPrefix(h.Get("X-Hangar-Signature"), "sha256=")
		if !validHMAC(body, secret, sig) {
			return nil, ErrBadSignature
		}
		p, err = parseGitHubShaped(body)
		p.Provider = "generic"
		p.Event = normEvent(first(h.Get("X-Hangar-Event"), "push"))
		p.Delivery = h.Get("X-Hangar-Delivery")
	default:
		return nil, ErrNoSignature
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

func sigErr(got string) error {
	if got == "" {
		return ErrNoSignature
	}
	return ErrBadSignature
}

func validHMAC(body []byte, secret, sigHex string) bool {
	got, err := hex.DecodeString(strings.TrimSpace(sigHex))
	if err != nil || len(got) == 0 {
		return false
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hmac.Equal(got, m.Sum(nil))
}

func equal(a, b string) bool { return a != "" && hmac.Equal([]byte(a), []byte(b)) }

// Sign returns the hex HMAC-SHA256 of body, as GitHub, Gitea and the generic
// format expect it.
func Sign(body []byte, secret string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func first(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func normEvent(e string) string {
	switch strings.ToLower(e) {
	case "push", "push hook":
		return "push"
	case "ping":
		return "ping"
	}
	return "other"
}

const zeroSHA = "0000000000000000000000000000000000000000"

func setRef(p *Push, ref, after string) {
	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		p.Branch = strings.TrimPrefix(ref, "refs/heads/")
	case strings.HasPrefix(ref, "refs/tags/"):
		p.Tag = strings.TrimPrefix(ref, "refs/tags/")
	}
	if after == zeroSHA || after == "" {
		p.Deleted = true
	} else {
		p.After = after
	}
}

// GitHub, Gitea/Forgejo and the generic format share this payload shape.
func parseGitHubShaped(body []byte) (*Push, error) {
	var b struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Deleted    bool   `json:"deleted"`
		Repository struct {
			CloneURL      string `json:"clone_url"`
			SSHURL        string `json:"ssh_url"`
			HTMLURL       string `json:"html_url"`
			DefaultBranch string `json:"default_branch"`
		} `json:"repository"`
		Pusher struct {
			Name     string `json:"name"`
			Login    string `json:"login"`
			Username string `json:"username"`
		} `json:"pusher"`
		HeadCommit *struct {
			Message string `json:"message"`
		} `json:"head_commit"`
	}
	p := &Push{}
	if err := json.Unmarshal(body, &b); err != nil {
		return p, errors.New("the webhook payload is not valid JSON")
	}
	setRef(p, b.Ref, b.After)
	p.Deleted = p.Deleted || b.Deleted
	if p.Deleted {
		p.After = ""
	}
	p.RepoURLs = nonEmpty(b.Repository.CloneURL, b.Repository.SSHURL, b.Repository.HTMLURL)
	p.DefaultBranch = b.Repository.DefaultBranch
	p.Pusher = first(b.Pusher.Login, b.Pusher.Username, b.Pusher.Name)
	if b.HeadCommit != nil {
		p.Message = b.HeadCommit.Message
	}
	return p, nil
}

func parseGitLab(h http.Header, body []byte) (*Push, error) {
	var b struct {
		ObjectKind string `json:"object_kind"`
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Username   string `json:"user_username"`
		Project    struct {
			HTTPURL       string `json:"git_http_url"`
			SSHURL        string `json:"git_ssh_url"`
			WebURL        string `json:"web_url"`
			DefaultBranch string `json:"default_branch"`
		} `json:"project"`
		Commits []struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		} `json:"commits"`
	}
	p := &Push{Provider: "gitlab", Delivery: first(h.Get("X-Gitlab-Webhook-UUID"), h.Get("X-Gitlab-Event-UUID"))}
	if err := json.Unmarshal(body, &b); err != nil {
		return p, errors.New("the webhook payload is not valid JSON")
	}
	p.Event = "other"
	if b.ObjectKind == "push" || b.ObjectKind == "tag_push" {
		p.Event = "push"
	}
	setRef(p, b.Ref, b.After)
	p.RepoURLs = nonEmpty(b.Project.HTTPURL, b.Project.SSHURL, b.Project.WebURL)
	p.DefaultBranch = b.Project.DefaultBranch
	p.Pusher = b.Username
	for _, c := range b.Commits {
		if c.ID == b.After {
			p.Message = c.Message
		}
	}
	return p, nil
}

func nonEmpty(xs ...string) []string {
	var out []string
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

// NormalizeRepo reduces a repository URL to "host/path" so https, ssh and
// web URLs of the same repository compare equal.
func NormalizeRepo(u string) string {
	u = strings.TrimSpace(strings.ToLower(u))
	if strings.Contains(u, "://") {
		if parsed, err := url.Parse(u); err == nil {
			u = parsed.Hostname() + parsed.Path
		}
	} else if at := strings.Index(u, "@"); at >= 0 {
		// scp-like git@host:org/repo.git
		u = strings.Replace(u[at+1:], ":", "/", 1)
	}
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	return u
}

// RepoMatches reports whether any of the payload's URLs is the same
// repository as want.
func RepoMatches(want string, urls []string) bool {
	w := NormalizeRepo(want)
	for _, u := range urls {
		if NormalizeRepo(u) == w {
			return true
		}
	}
	return false
}
