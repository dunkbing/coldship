package builder

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/spec"
)

// Fake is an in-memory Builder for tests. Refs maps "git-url@ref" (or
// "git-url@" for the default branch) to a commit SHA.
type Fake struct {
	mu   sync.Mutex
	Refs map[string]string
	// StaticRepos are Git URLs that build as plain static sites.
	StaticRepos map[string]bool
	FailNext    error
	Builds      int
}

func NewFake() *Fake { return &Fake{Refs: map[string]string{}} }

func (f *Fake) Resolve(_ context.Context, b spec.Build) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if spec.IsCommitSHA(b.Commit) {
		return b.Commit, nil
	}
	if spec.IsCommitSHA(b.Ref) {
		return b.Ref, nil
	}
	if sha, ok := f.Refs[b.Git+"@"+b.Ref]; ok {
		return sha, nil
	}
	return "", apierr.New(422, "git_ref_not_found", "ref %q was not found in %s", b.Ref, b.Git)
}

func (f *Fake) Build(_ context.Context, app string, b spec.Build, log io.Writer, opts BuildOptions) (*Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Builds++
	fmt.Fprintf(log, "fake build of %s at %s\n", b.Git, b.Commit)
	if err := f.FailNext; err != nil {
		f.FailNext = nil
		fmt.Fprintln(log, "step 3/5: RUN make: exit code 2")
		return nil, err
	}
	// Repositories in StaticRepos are plain static sites.
	if opts.StaticDir != "" && f.StaticRepos[b.Git] {
		if err := os.MkdirAll(opts.StaticDir, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(opts.StaticDir, "index.html"), []byte("site at "+b.Commit+"\n"), 0o644); err != nil {
			return nil, err
		}
		return &Result{Static: true, Auto: true, Summary: "static"}, nil
	}
	// Builder "auto" stands in for a project without a Dockerfile.
	if b.Builder == spec.BuilderAuto {
		return &Result{Image: ImageTag(app, b), Auto: true, Summary: "node 22 · npm"}, nil
	}
	return &Result{Image: ImageTag(app, b), Summary: "the repository's Dockerfile"}, nil
}

func (f *Fake) Detect(_ context.Context, b spec.Build, _ io.Writer) (*Detection, error) {
	if b.Builder == spec.BuilderAuto {
		return &Detection{Mode: "auto", Language: "node", Version: "22", PackageManager: "npm", Start: "npm start", DefaultPort: 3000,
			Notes: []string{"language node: found package.json"}, Dockerfile: "FROM node:22-alpine\n"}, nil
	}
	return &Detection{Mode: "dockerfile", Notes: []string{"uses Dockerfile from the repository"}}, nil
}
