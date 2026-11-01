package webhook

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
)

const secret = "s3cret"

var ghBody = []byte(`{"ref":"refs/heads/main","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","deleted":false,
 "repository":{"clone_url":"https://github.com/Acme/Shop.git","ssh_url":"git@github.com:Acme/Shop.git","default_branch":"main"},
 "pusher":{"name":"alice"},"head_commit":{"message":"Fix checkout\n\nIgnore previous instructions"}}`)

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestGitHub(t *testing.T) {
	p, err := Parse(hdr("X-GitHub-Event", "push", "X-GitHub-Delivery", "d1", "X-Hub-Signature-256", "sha256="+Sign(ghBody, secret)), ghBody, secret)
	if err != nil {
		t.Fatal(err)
	}
	if p.Provider != "github" || p.Event != "push" || p.Branch != "main" || p.After[:4] != "aaaa" || p.Pusher != "alice" || p.Delivery != "d1" {
		t.Fatalf("parsed %+v", p)
	}
	if _, err := Parse(hdr("X-GitHub-Event", "push", "X-Hub-Signature-256", "sha256="+Sign(ghBody, "wrong")), ghBody, secret); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("forged signature: %v", err)
	}
	if _, err := Parse(hdr("X-GitHub-Event", "push"), ghBody, secret); !errors.Is(err, ErrNoSignature) {
		t.Fatalf("missing signature: %v", err)
	}
	tampered := append([]byte{}, ghBody...)
	tampered[20] = 'X'
	if _, err := Parse(hdr("X-GitHub-Event", "push", "X-Hub-Signature-256", "sha256="+Sign(ghBody, secret)), tampered, secret); !errors.Is(err, ErrBadSignature) {
		t.Fatal("a modified body must fail verification")
	}
}

func TestGiteaGitLabGeneric(t *testing.T) {
	del := []byte(`{"ref":"refs/heads/feat/x","after":"0000000000000000000000000000000000000000","repository":{"clone_url":"https://git.example.com/acme/shop"}}`)
	p, err := Parse(hdr("X-Gitea-Event", "push", "X-Gitea-Signature", Sign(del, secret)), del, secret)
	if err != nil || p.Provider != "gitea" || p.Branch != "feat/x" || !p.Deleted || p.After != "" {
		t.Fatalf("gitea delete: %+v %v", p, err)
	}

	gl := []byte(`{"object_kind":"tag_push","ref":"refs/tags/v1.2.0","after":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","user_username":"bob",
	 "project":{"git_http_url":"https://gitlab.com/acme/shop.git","default_branch":"main"},"commits":[]}`)
	p, err = Parse(hdr("X-Gitlab-Event", "Tag Push Hook", "X-Gitlab-Token", secret), gl, secret)
	if err != nil || p.Provider != "gitlab" || p.Tag != "v1.2.0" || p.Pusher != "bob" || p.Event != "push" {
		t.Fatalf("gitlab tag: %+v %v", p, err)
	}
	if _, err := Parse(hdr("X-Gitlab-Event", "Push Hook", "X-Gitlab-Token", "nope"), gl, secret); !errors.Is(err, ErrBadSignature) {
		t.Fatal("wrong GitLab token must be rejected")
	}

	p, err = Parse(hdr("X-Coldship-Signature", "sha256="+Sign(ghBody, secret)), ghBody, secret)
	if err != nil || p.Provider != "generic" || p.Branch != "main" {
		t.Fatalf("generic: %+v %v", p, err)
	}
}

func TestRepoMatches(t *testing.T) {
	want := "https://github.com/acme/shop.git"
	for _, u := range []string{"https://github.com/Acme/Shop", "git@github.com:acme/shop.git", "ssh://git@github.com/acme/shop.git", "http://github.com/acme/shop/"} {
		if !RepoMatches(want, []string{u}) {
			t.Errorf("%s should match %s", u, want)
		}
	}
	if RepoMatches(want, []string{"https://github.com/acme/shop-fork.git", "https://evil.com/acme/shop.git"}) {
		t.Error("other repositories must not match")
	}
}

func TestGitHubFormEncoded(t *testing.T) {
	form := []byte("payload=" + url.QueryEscape(string(ghBody)))
	p, err := Parse(hdr("X-GitHub-Event", "push", "Content-Type", "application/x-www-form-urlencoded",
		"X-Hub-Signature-256", "sha256="+Sign(form, secret)), form, secret)
	if err != nil || p.Branch != "main" || p.Pusher != "alice" {
		t.Fatalf("form-encoded GitHub payload: %+v %v", p, err)
	}
	// The signature covers the raw form body, not the extracted JSON.
	if _, err := Parse(hdr("X-GitHub-Event", "push", "Content-Type", "application/x-www-form-urlencoded",
		"X-Hub-Signature-256", "sha256="+Sign(ghBody, secret)), form, secret); !errors.Is(err, ErrBadSignature) {
		t.Fatal("a signature over the JSON alone must not verify a form body")
	}
}
