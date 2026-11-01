package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

// PreviewConfig controls preview environments.
type PreviewConfig struct {
	// Domain, if set, gives each preview the URL https://<name>.<Domain>.
	// It needs a wildcard DNS record (*.<Domain>) pointing at the server.
	Domain     string
	DefaultTTL time.Duration
	MaxTTL     time.Duration
	// Max is the most previews that may exist at once.
	Max int
}

func defaultPreviewConfig() PreviewConfig {
	return PreviewConfig{DefaultTTL: 24 * time.Hour, MaxTTL: 7 * 24 * time.Hour, Max: 20}
}

type PreviewRequest struct {
	// App is the app to preview.
	App string `json:"app"`
	// Ref is the Git branch, tag or commit to deploy (apps built from Git).
	Ref string `json:"ref,omitempty"`
	// Image replaces the image (apps that run a prebuilt image).
	Image string `json:"image,omitempty"`
	// Name is the preview's suffix; the preview app is "<app>-<name>".
	// Defaults to a slug of Ref.
	Name string `json:"name,omitempty"`
	// TTL is how long the preview lives, e.g. "2h". Re-creating it resets
	// the clock.
	TTL string `json:"ttl,omitempty"`
	// Env adds or overrides environment variables.
	Env map[string]string `json:"env,omitempty"`
	// InheritEnv copies the parent's env. Defaults to true, except for
	// production apps, whose env (database URLs, API keys) a throwaway
	// preview must not get by accident.
	InheritEnv *bool  `json:"inherit_env,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type PreviewResult struct {
	App        string            `json:"app"`
	PreviewOf  string            `json:"preview_of"`
	Created    bool              `json:"created"`
	ExpiresAt  time.Time         `json:"expires_at"`
	URLs       []string          `json:"urls,omitempty"`
	Plan       *store.Plan       `json:"plan"`
	Deployment *store.Deployment `json:"deployment,omitempty"`
	Message    string            `json:"message"`
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// previewName builds "<app>-<slug>", shortened with a hash to fit the 40
// character app name limit.
func previewName(app, suffix string) string {
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(suffix), "-"), "-")
	if slug == "" {
		slug = "preview"
	}
	name := app + "-" + slug
	// Container names add "-next" and "-db-<name>" to the app name, so
	// web-next or web-db-main would collide with web's own containers.
	risky := slug == "next" || strings.HasSuffix(slug, "-next") || strings.HasPrefix(slug, "db-") || strings.Contains(slug, "-db-")
	if len(name) <= 40 && !risky {
		return name
	}
	h := sha256.Sum256([]byte(name))
	short := hex.EncodeToString(h[:])[:6]
	keep := 40 - len(app) - 1 - 7
	if keep < 1 {
		return app[:40-7] + "-" + short
	}
	return app + "-" + strings.Trim(slug[:min(keep, len(slug))], "-") + "-" + short
}

// CreatePreview creates a preview of an app, or updates it if it exists
// (new ref, image or env) and resets its expiry. It goes through the normal
// plan and apply path.
func (e *Engine) CreatePreview(ctx context.Context, actor string, req PreviewRequest) (*PreviewResult, error) {
	parent, err := e.st.GetApp(ctx, req.App)
	if err != nil {
		return nil, err
	}
	if parent == nil || parent.Spec == nil {
		return nil, apierr.NotFound("app", req.App).WithHint("previews copy a deployed app; deploy it first")
	}
	if parent.PreviewOf != "" {
		return nil, apierr.New(400, "invalid_request", "%s is itself a preview; create the preview from %s", req.App, parent.PreviewOf)
	}
	suffix := req.Name
	if suffix == "" {
		suffix = req.Ref
	}
	if suffix == "" {
		return nil, apierr.New(400, "invalid_request", "give the preview a name, or a ref to name it after").WithField("name")
	}
	name := previewName(req.App, suffix)

	ttl := e.Previews.DefaultTTL
	if req.TTL != "" {
		d, err := time.ParseDuration(req.TTL)
		if err != nil || d < time.Minute {
			return nil, apierr.New(400, "invalid_request", "ttl %q must be a duration of at least 1m, e.g. 2h", req.TTL).WithField("ttl")
		}
		ttl = d
	}
	if ttl > e.Previews.MaxTTL {
		return nil, apierr.New(400, "invalid_request", "ttl %s is longer than this server allows (%s)", ttl, e.Previews.MaxTTL).
			WithField("ttl").WithFix(map[string]any{"ttl": e.Previews.MaxTTL.String()})
	}

	desired, err := e.previewSpec(parent, name, req)
	if err != nil {
		return nil, err
	}

	defer e.lockApp(name)()
	existing, err := e.st.GetApp(ctx, name)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.PreviewOf != parent.Name {
		return nil, apierr.New(409, "name_in_use", "an app named %s already exists and is not a preview of %s", name, parent.Name).
			WithField("name").WithHint("choose another preview name")
	}
	if existing == nil {
		if err := e.checkPreviewLimit(ctx); err != nil {
			return nil, err
		}
	}

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = fmt.Sprintf("preview of %s", parent.Name)
		if req.Ref != "" {
			reason += " at " + req.Ref
		}
	}
	p, err := e.plan(ctx, actor, PlanRequest{Spec: desired, previewOf: parent.Name})
	if err != nil {
		return nil, err
	}
	expires := e.Now().Add(ttl)
	res := &PreviewResult{App: name, PreviewOf: parent.Name, Created: existing == nil, ExpiresAt: expires, Plan: publicPlan(p)}

	var applyErr error
	if p.Action == "noop" {
		res.Message = "preview is up to date; expiry extended"
	} else {
		var ar *ApplyResult
		ar, applyErr = e.apply(ctx, actor, p.ID, reason)
		if ar != nil {
			res.Deployment, res.Plan = ar.Deployment, ar.Plan
		}
		res.Message = "preview deployed"
	}
	// Record preview metadata even if the deploy failed, so the reaper still
	// cleans the preview up.
	if app, err := e.st.GetApp(ctx, name); err == nil && app != nil {
		if err := e.st.SetPreview(ctx, name, parent.Name, expires); err != nil {
			return nil, err
		}
	}
	e.audit(ctx, actor, "preview.create", name, reason, map[string]any{
		"preview_of": parent.Name, "expires_at": expires.UTC().Format(time.RFC3339), "inherit_env": inheritEnv(parent, req)})
	if applyErr != nil {
		if ae, ok := apierr.As(applyErr); ok {
			ae.WithDetail("preview", name)
		}
		return nil, applyErr
	}
	if v, err := e.GetApp(ctx, name); err == nil {
		res.URLs = v.URLs
	}
	return res, nil
}

func inheritEnv(parent *store.App, req PreviewRequest) bool {
	if req.InheritEnv != nil {
		return *req.InheritEnv
	}
	return parent.Spec.Stage != spec.StageProduction
}

// previewSpec derives a preview's spec from its parent's current one.
func (e *Engine) previewSpec(parent *store.App, name string, req PreviewRequest) (*spec.App, error) {
	s := parent.Spec.Clone()
	s.Name = name
	s.Stage = spec.StagePreview
	// Two apps can't share a host port, and the preview gets its own
	// hostname instead of the parent's domains.
	s.HostPort = 0
	s.Domains = nil
	if e.Previews.Domain != "" && s.Port > 0 {
		s.Domains = []string{name + "." + e.Previews.Domain}
	}
	// Volumes keep their names, but volumes are per app, so the preview
	// gets fresh, empty ones and never touches the parent's data.

	if !inheritEnv(parent, req) {
		s.Env = nil
	}
	for k, v := range req.Env {
		if s.Env == nil {
			s.Env = map[string]string{}
		}
		s.Env[k] = v
	}

	switch {
	case req.Ref != "" && req.Image != "":
		return nil, apierr.New(400, "invalid_request", "give either ref or image, not both")
	case req.Ref != "" && s.Build == nil:
		return nil, apierr.New(400, "invalid_request", "%s runs a prebuilt image, so a preview takes an image, not a ref", parent.Name).
			WithField("ref").WithFix(map[string]any{"image": parent.Spec.Image})
	case req.Ref != "":
		s.Build.Ref, s.Build.Commit = req.Ref, ""
	case req.Image != "" && s.Build != nil:
		return nil, apierr.New(400, "invalid_request", "%s is built from Git, so a preview takes a ref, not an image", parent.Name).WithField("image")
	case req.Image != "":
		s.Image = req.Image
	case s.Build != nil:
		// Same ref as the parent, re-resolved to its latest commit.
		s.Build.Commit = ""
	}
	return s, nil
}

func (e *Engine) checkPreviewLimit(ctx context.Context) error {
	previews, err := e.listPreviews(ctx)
	if err != nil {
		return err
	}
	if len(previews) < e.Previews.Max {
		return nil
	}
	names := make([]string, 0, len(previews))
	for _, p := range previews {
		names = append(names, p.Name)
	}
	return apierr.New(409, "preview_limit", "this server allows %d previews at once, and all are in use", e.Previews.Max).
		WithDetail("previews", names).WithHint("delete a preview you no longer need (they are listed soonest-expiring first)")
}

func (e *Engine) listPreviews(ctx context.Context) ([]store.App, error) {
	apps, err := e.st.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	out := []store.App{}
	for _, a := range apps {
		if a.PreviewOf != "" {
			a.Spec = a.Spec.Redacted()
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ExpiresAt == nil || out[j].ExpiresAt == nil {
			return out[i].Name < out[j].Name
		}
		return out[i].ExpiresAt.Before(*out[j].ExpiresAt)
	})
	return out, nil
}

// ListPreviews lists previews, soonest-expiring first.
func (e *Engine) ListPreviews(ctx context.Context) ([]store.App, error) { return e.listPreviews(ctx) }

// DeletePreview deletes a preview app and its volumes.
func (e *Engine) DeletePreview(ctx context.Context, actor, name, reason string) (*ApplyResult, error) {
	defer e.lockApp(name)()
	app, err := e.st.GetApp(ctx, name)
	if err != nil {
		return nil, err
	}
	if app == nil {
		return nil, apierr.NotFound("app", name)
	}
	if app.PreviewOf == "" {
		return nil, apierr.New(400, "not_a_preview", "%s is not a preview; delete regular apps with a plan", name).
			WithHint("use plan with delete, then apply")
	}
	if strings.TrimSpace(reason) == "" {
		reason = "preview deleted"
	}
	p, err := e.plan(ctx, actor, PlanRequest{Delete: name})
	if err != nil {
		return nil, err
	}
	return e.apply(ctx, actor, p.ID, reason)
}

// ReapPreviews deletes previews whose time is up. The server calls it
// every minute.
func (e *Engine) ReapPreviews(ctx context.Context) (deleted []string, err error) {
	previews, err := e.listPreviews(ctx)
	if err != nil {
		return nil, err
	}
	now := e.Now()
	for _, p := range previews {
		if p.ExpiresAt == nil || now.Before(*p.ExpiresAt) {
			continue
		}
		if _, err := e.DeletePreview(ctx, "coldship", p.Name, "preview expired"); err != nil {
			return deleted, fmt.Errorf("deleting expired preview %s: %w", p.Name, err)
		}
		deleted = append(deleted, p.Name)
	}
	return deleted, nil
}
