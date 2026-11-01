package builder

import (
	"context"
	"fmt"
	"io"
	"sync"

	"hangar/internal/apierr"
	"hangar/internal/spec"
)

// Fake is an in-memory Builder for tests. Refs maps "git-url@ref" (or
// "git-url@" for the default branch) to a commit SHA.
type Fake struct {
	mu       sync.Mutex
	Refs     map[string]string
	FailNext error
	Builds   int
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

func (f *Fake) Build(_ context.Context, app string, b spec.Build, log io.Writer) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Builds++
	fmt.Fprintf(log, "fake build of %s at %s\n", b.Git, b.Commit)
	if err := f.FailNext; err != nil {
		f.FailNext = nil
		fmt.Fprintln(log, "step 3/5: RUN make: exit code 2")
		return "", err
	}
	return ImageTag(app, b), nil
}
