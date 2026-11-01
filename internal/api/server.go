// Package api serves the engine over a JSON HTTP API with bearer-token auth.
// The CLI and MCP server are both clients of this API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/engine"
	"github.com/dunkbing/coldship/internal/store"
	"github.com/dunkbing/coldship/internal/update"
	"github.com/dunkbing/coldship/internal/webhook"
)

type principal struct {
	name  string
	scope store.Scope
	apps  []string // empty: every app
}

func (p principal) limited() bool { return len(p.apps) > 0 }

// allowApp returns an error unless p may use app, or app is a preview of an
// app p may use.
func (s *Server) allowApp(ctx context.Context, p principal, app string) error {
	if store.MatchApps(p.apps, app) {
		return nil
	}
	if a, err := s.st.GetApp(ctx, app); err == nil && a != nil && a.PreviewOf != "" && store.MatchApps(p.apps, a.PreviewOf) {
		return nil
	}
	return apierr.New(403, "app_not_allowed", "token %q can't use app %q: it is limited to %s", p.name, app, strings.Join(p.apps, ", ")).
		WithDetail("allowed_apps", p.apps).
		WithHint("work on one of those apps, or ask an admin for a token that covers this one")
}

// visible reports whether a limited principal may see app (with parent
// previewOf, if it is a preview).
func (p principal) visible(app, previewOf string) bool {
	return store.MatchApps(p.apps, app) || (previewOf != "" && store.MatchApps(p.apps, previewOf))
}

type Server struct {
	eng *engine.Engine
	st  *store.Store
	mux *http.ServeMux
	log *slog.Logger
	upd *update.Checker
	// publicURL is how Git hosts reach this server, for webhook URLs.
	publicURL string
	rotateKey func(context.Context) (map[string]any, error)
}

// SetKeyRotation enables rotating the secret key through the API.
func (s *Server) SetKeyRotation(f func(context.Context) (map[string]any, error)) { s.rotateKey = f }

// SetPublicURL sets the externally reachable base URL (for webhook URLs).
func (s *Server) SetPublicURL(u string) { s.publicURL = strings.TrimRight(u, "/") }

// SetUpdater enables the /v1/system update endpoints.
func (s *Server) SetUpdater(c *update.Checker) { s.upd = c }

// New builds the API server. ui, if non-nil, serves the web dashboard for
// every path outside /v1/ and /healthz.
func New(eng *engine.Engine, st *store.Store, log *slog.Logger, ui http.Handler) *Server {
	s := &Server{eng: eng, st: st, mux: http.NewServeMux(), log: log}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	s.mux.HandleFunc("POST /v1/sessions", s.createSession)
	s.mux.HandleFunc("DELETE /v1/sessions", s.deleteSession)
	s.route("GET /v1/whoami", store.ScopeRead, s.whoami)
	s.route("GET /v1/apps", store.ScopeRead, s.listApps)
	s.route("GET /v1/apps/{name}", store.ScopeRead, s.getApp)
	s.route("GET /v1/apps/{name}/logs", store.ScopeRead, s.logs)
	s.route("GET /v1/apps/{name}/diagnose", store.ScopeRead, s.diagnose)
	s.route("GET /v1/apps/{name}/deployments", store.ScopeRead, s.deployments)
	s.route("POST /v1/apps/{name}/rollback", store.ScopeDeploy, s.rollback)
	s.route("GET /v1/deployments/{id}/log", store.ScopeRead, s.deployLog)
	s.mux.HandleFunc("GET /v1/deployments/{id}/log/stream", s.streamDeployLog)
	s.mux.HandleFunc("POST /hooks/git/{app}", s.gitHook)
	s.route("GET /v1/apps/{name}/webhook", store.ScopeRead, s.webhookStatus)
	s.route("POST /v1/apps/{name}/webhook", store.ScopeAdmin, s.enableWebhook)
	s.route("DELETE /v1/apps/{name}/webhook", store.ScopeAdmin, s.disableWebhook)
	s.route("GET /v1/apps/{name}/databases", store.ScopeRead, s.databases)
	s.route("GET /v1/apps/{name}/snapshots", store.ScopeRead, s.snapshots)
	s.route("POST /v1/apps/{name}/databases/{db}/snapshots", store.ScopeDeploy, s.createSnapshot)
	s.route("POST /v1/build/detect", store.ScopeDeploy, s.detectBuild)
	s.route("POST /v1/previews", store.ScopeDeploy, s.createPreview)
	s.route("GET /v1/previews", store.ScopeRead, s.listPreviews)
	s.route("DELETE /v1/previews/{name}", store.ScopeDeploy, s.deletePreview)
	s.route("POST /v1/plans", store.ScopeDeploy, s.createPlan)
	s.route("GET /v1/plans", store.ScopeRead, s.listPlans)
	s.route("GET /v1/plans/{id}", store.ScopeRead, s.getPlan)
	s.route("POST /v1/plans/{id}/apply", store.ScopeDeploy, s.applyPlan)
	s.route("POST /v1/plans/{id}/approve", store.ScopeAdmin, s.approvePlan)
	s.route("GET /v1/audit", store.ScopeRead, s.audit)
	s.route("GET /v1/system", store.ScopeRead, s.system)
	s.route("GET /v1/system/secrets", store.ScopeAdmin, s.secretsStatus)
	s.route("GET /v1/system/status", store.ScopeRead, s.serverStatus)
	s.route("POST /v1/system/secrets/rotate", store.ScopeAdmin, s.rotateSecrets)
	s.route("POST /v1/system/check", store.ScopeAdmin, s.systemCheck)
	s.route("POST /v1/system/upgrade", store.ScopeAdmin, s.systemUpgrade)
	s.route("GET /v1/data", store.ScopeRead, s.listData)
	s.route("POST /v1/data", store.ScopeDeploy, s.createData)
	s.route("GET /v1/data/{name}", store.ScopeRead, s.getData)
	s.route("DELETE /v1/data/{name}", store.ScopeAdmin, s.deleteData)
	s.route("POST /v1/data/{name}/connection", store.ScopeAdmin, s.dataConnection)
	s.route("GET /v1/data/{name}/snapshots", store.ScopeRead, s.dataSnapshots)
	s.route("POST /v1/data/{name}/snapshots", store.ScopeDeploy, s.snapshotData)
	s.route("POST /v1/data/{name}/restore", store.ScopeAdmin, s.restoreData)
	s.route("POST /v1/tokens", store.ScopeAdmin, s.createToken)
	s.route("GET /v1/tokens", store.ScopeAdmin, s.listTokens)
	s.route("DELETE /v1/tokens/{name}", store.ScopeAdmin, s.revokeToken)
	notFound := func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, s.log, apierr.New(404, "route_not_found", "no route for %s %s", r.Method, r.URL.Path))
	}
	s.mux.HandleFunc("/v1/", notFound)
	if ui != nil {
		s.mux.Handle("/", ui)
	} else {
		s.mux.HandleFunc("/", notFound)
	}
	return s
}

// bodyReadTimeout bounds how long a client may take to send a request body,
// so slow clients can't hold connections open. It only covers reading: a
// deploy can run long after its body arrived.
const bodyReadTimeout = 30 * time.Second

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Not every ResponseWriter supports deadlines (httptest recorders);
	// ignore the error there.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(bodyReadTimeout))
	s.mux.ServeHTTP(w, r)
}

type handler func(r *http.Request, p principal) (any, error)

func (s *Server) route(pattern string, need store.Scope, h handler) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		p, err := s.auth(r)
		if err != nil {
			writeErr(w, s.log, err)
			return
		}
		if !p.scope.Allows(need) {
			writeErr(w, s.log, apierr.New(403, "insufficient_scope", "token %q has scope %s; this action needs %s", p.name, p.scope, need))
			return
		}
		if p.limited() && strings.Contains(pattern, "/v1/data/{name}") && !store.MatchApps(p.apps, r.PathValue("name")) {
			writeErr(w, s.log, dataNotAllowed(p, r.PathValue("name")))
			return
		}
		if p.limited() && (strings.Contains(pattern, "/v1/apps/{name}") || strings.Contains(pattern, "/v1/previews/{name}")) {
			if err := s.allowApp(r.Context(), p, r.PathValue("name")); err != nil {
				writeErr(w, s.log, err)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		out, err := h(r, p)
		if err != nil {
			writeErr(w, s.log, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// SessionCookie holds the dashboard's session id. It is HttpOnly, so page
// scripts (and any XSS) cannot read it.
const SessionCookie = "coldship_session"

// CSRFHeader must accompany cookie-authenticated requests that change state.
// Browsers only send custom headers cross-origin after a CORS preflight, which
// this server never allows, so a forged cross-site request cannot carry it.
const CSRFHeader = "X-Coldship-Request"

const sessionTTL = 12 * time.Hour

func (s *Server) auth(r *http.Request) (principal, error) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		c, err := r.Cookie(SessionCookie)
		if err != nil || c.Value == "" {
			return principal{}, apierr.New(401, "unauthenticated", "missing bearer token").
				WithHint("set COLDSHIP_TOKEN or pass --token")
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get(CSRFHeader) == "" {
			return principal{}, apierr.New(403, "csrf_check_failed", "cookie-authenticated requests that change state need the %s header", CSRFHeader)
		}
		t, err := s.st.LookupSession(r.Context(), c.Value)
		if err != nil {
			return principal{}, err
		}
		if t == nil {
			return principal{}, apierr.New(401, "unauthenticated", "session expired or signed out")
		}
		return principal{name: t.Name, scope: t.Scope, apps: t.Apps}, nil
	}
	t, err := s.st.LookupToken(r.Context(), tok)
	if err != nil {
		return principal{}, err
	}
	if t == nil {
		return principal{}, apierr.New(401, "unauthenticated", "unknown token")
	}
	return principal{name: t.Name, scope: t.Scope, apps: t.Apps}, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, log *slog.Logger, err error) {
	ae := apierr.From(err)
	if ae.Code == "internal" {
		log.Error("internal error", "err", err)
	}
	writeJSON(w, ae.Status, map[string]any{"error": ae})
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return apierr.New(400, "invalid_request", "invalid JSON body: %v", err)
	}
	if dec.More() {
		return apierr.New(400, "invalid_request", "invalid JSON body: unexpected data after the JSON value")
	}
	return nil
}

func intQuery(r *http.Request, key string, def, max int) (int, error) {
	s := r.URL.Query().Get(key)
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > max {
		return 0, apierr.New(400, "invalid_request", "%s must be an integer between 1 and %d", key, max).WithField(key)
	}
	return n, nil
}

// --- handlers ---

type WhoAmI struct {
	Name  string      `json:"name"`
	Scope store.Scope `json:"scope"`
	// Apps lists the apps the token is limited to; empty means all.
	Apps []string `json:"apps,omitempty"`
}

func (s *Server) whoami(_ *http.Request, p principal) (any, error) {
	return WhoAmI{Name: p.name, Scope: p.scope, Apps: p.apps}, nil
}

func (s *Server) listPlans(r *http.Request, p principal) (any, error) {
	limit, err := intQuery(r, "limit", 50, 500)
	if err != nil {
		return nil, err
	}
	plans, err := s.eng.ListPlans(r.Context(), r.URL.Query().Get("status"), limit)
	if err != nil || !p.limited() {
		return plans, err
	}
	out := plans[:0:0]
	for _, pl := range plans {
		if s.allowApp(r.Context(), p, pl.App) == nil {
			out = append(out, pl)
		}
	}
	return out, nil
}

// allowPlan checks that p may use the plan's app.
func (s *Server) allowPlan(r *http.Request, p principal) error {
	if !p.limited() {
		return nil
	}
	pl, err := s.eng.GetPlan(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return s.allowApp(r.Context(), p, pl.App)
}

type SessionRequest struct {
	Token string `json:"token"`
}

// createSession exchanges an API token for an HttpOnly session cookie, so the
// dashboard never keeps the token itself.
func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	// A cross-site form can't set this header, so another site can't sign
	// the browser in to an attacker's token (login CSRF).
	if r.Header.Get(CSRFHeader) == "" {
		writeErr(w, s.log, apierr.New(403, "csrf_check_failed", "sign-in requests need the %s header", CSRFHeader))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	var req SessionRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, s.log, err)
		return
	}
	t, err := s.st.LookupToken(r.Context(), strings.TrimSpace(req.Token))
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	if t == nil {
		writeErr(w, s.log, apierr.New(401, "unauthenticated", "unknown token"))
		return
	}
	id, err := s.st.CreateSession(r.Context(), t, sessionTTL)
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	_ = s.st.AddAudit(r.Context(), store.AuditEntry{Actor: t.Name, Action: "session.create", Target: t.Name})
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: id, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
	writeJSON(w, http.StatusOK, WhoAmI{Name: t.Name, Scope: t.Scope})
}

// deleteSession signs the dashboard out: the session is deleted server-side
// and the cookie cleared.
func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	if _, err := s.auth(r); err != nil {
		writeErr(w, s.log, err)
		return
	}
	if c, err := r.Cookie(SessionCookie); err == nil {
		if err := s.st.DeleteSession(r.Context(), c.Value); err != nil {
			writeErr(w, s.log, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"signed_out": true})
}

// gitHook receives pushes from Git hosts. It is authenticated only by the
// per-app webhook secret (HMAC signature or GitLab token), never by tokens.
func (s *Server) gitHook(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	secret, err := s.eng.WebhookSecret(r.Context(), app)
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	if secret == "" {
		writeErr(w, s.log, apierr.New(404, "webhook_not_enabled", "no webhook is enabled for %q", app))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
	if err != nil {
		writeErr(w, s.log, apierr.New(413, "payload_too_large", "webhook payloads are limited to 5 MB"))
		return
	}
	push, err := webhook.Parse(r.Header, body, secret)
	if err != nil {
		if errors.Is(err, webhook.ErrNoSignature) || errors.Is(err, webhook.ErrBadSignature) {
			s.log.Warn("rejected webhook", "app", app, "err", err, "remote", r.RemoteAddr)
			writeErr(w, s.log, apierr.New(401, "bad_signature", "%v", err))
			return
		}
		writeErr(w, s.log, apierr.New(400, "invalid_request", "%v", err))
		return
	}
	if seen, err := s.eng.SeenDelivery(r.Context(), app, push.Delivery); err == nil && seen {
		writeJSON(w, http.StatusOK, map[string]string{"result": "duplicate", "detail": "this delivery was already handled"})
		return
	}
	d, err := s.eng.HandlePush(r.Context(), app, push)
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	status := http.StatusOK
	if d.Result == "queued" {
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]string{"result": d.Result, "detail": d.Detail, "delivery": d.ID})
}

func (s *Server) hookURL(r *http.Request, app string) string {
	base := s.publicURL
	if base == "" {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return base + "/hooks/git/" + app
}

type WebhookInfo struct {
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"`
	*engine.WebhookStatus
}

func (s *Server) webhookStatus(r *http.Request, _ principal) (any, error) {
	st, err := s.eng.WebhookStatus(r.Context(), r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	return WebhookInfo{URL: s.hookURL(r, st.App), WebhookStatus: st}, nil
}

type EnableWebhookRequest struct {
	Rotate bool `json:"rotate,omitempty"`
}

func (s *Server) enableWebhook(r *http.Request, p principal) (any, error) {
	var req EnableWebhookRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	name := r.PathValue("name")
	secret, err := s.eng.EnableWebhook(r.Context(), p.name, name, req.Rotate)
	if err != nil {
		return nil, err
	}
	st, err := s.eng.WebhookStatus(r.Context(), name)
	if err != nil {
		return nil, err
	}
	return WebhookInfo{URL: s.hookURL(r, name), Secret: secret, WebhookStatus: st}, nil
}

func (s *Server) disableWebhook(r *http.Request, p principal) (any, error) {
	return map[string]bool{"disabled": true}, s.eng.DisableWebhook(r.Context(), p.name, r.PathValue("name"))
}

func (s *Server) databases(r *http.Request, _ principal) (any, error) {
	return s.eng.Databases(r.Context(), r.PathValue("name"))
}

func (s *Server) snapshots(r *http.Request, _ principal) (any, error) {
	return s.eng.Snapshots(r.Context(), r.PathValue("name"), r.URL.Query().Get("database"))
}

type SnapshotRequest struct {
	Reason string `json:"reason,omitempty"`
}

func (s *Server) createSnapshot(r *http.Request, p principal) (any, error) {
	var req SnapshotRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return s.eng.Snapshot(r.Context(), p.name, r.PathValue("name"), r.PathValue("db"), req.Reason)
}

func (s *Server) deployLog(r *http.Request, p principal) (any, error) {
	l, err := s.eng.DeployLog(r.Context(), r.PathValue("id"))
	if err == nil && p.limited() {
		if err := s.allowApp(r.Context(), p, l.App); err != nil {
			return nil, err
		}
	}
	return l, err
}

func (s *Server) listApps(r *http.Request, p principal) (any, error) {
	apps, err := s.eng.ListApps(r.Context())
	if err != nil || !p.limited() {
		return apps, err
	}
	return visibleApps(p, apps), nil
}

func visibleApps(p principal, apps []store.App) []store.App {
	out := apps[:0:0]
	for _, a := range apps {
		if p.visible(a.Name, a.PreviewOf) {
			out = append(out, a)
		}
	}
	return out
}

func (s *Server) getApp(r *http.Request, _ principal) (any, error) {
	return s.eng.GetApp(r.Context(), r.PathValue("name"))
}

func (s *Server) logs(r *http.Request, _ principal) (any, error) {
	tail, err := intQuery(r, "tail", 100, 5000)
	if err != nil {
		return nil, err
	}
	return s.eng.Logs(r.Context(), r.PathValue("name"), tail, r.URL.Query().Get("since"))
}

func (s *Server) diagnose(r *http.Request, _ principal) (any, error) {
	return s.eng.Diagnose(r.Context(), r.PathValue("name"))
}

func (s *Server) deployments(r *http.Request, _ principal) (any, error) {
	limit, err := intQuery(r, "limit", 20, 200)
	if err != nil {
		return nil, err
	}
	return s.eng.Deployments(r.Context(), r.PathValue("name"), limit)
}

type RollbackRequest struct {
	Reason       string `json:"reason"`
	ToDeployment string `json:"to_deployment,omitempty"`
}

func (s *Server) rollback(r *http.Request, p principal) (any, error) {
	var req RollbackRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return s.eng.Rollback(r.Context(), p.name, r.PathValue("name"), req.ToDeployment, req.Reason)
}

func (s *Server) detectBuild(r *http.Request, p principal) (any, error) {
	var req engine.DetectRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if p.limited() {
		app := req.App
		if req.Spec != nil {
			app = req.Spec.Name
		}
		if err := s.allowApp(r.Context(), p, app); err != nil {
			return nil, err
		}
	}
	return s.eng.DetectBuild(r.Context(), req)
}

func (s *Server) createPreview(r *http.Request, p principal) (any, error) {
	var req engine.PreviewRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	req.AdminCaller = p.scope.Allows(store.ScopeAdmin)
	if p.limited() {
		if err := s.allowApp(r.Context(), p, req.App); err != nil {
			return nil, err
		}
	}
	return s.eng.CreatePreview(r.Context(), p.name, req)
}

func (s *Server) listPreviews(r *http.Request, p principal) (any, error) {
	apps, err := s.eng.ListPreviews(r.Context())
	if err != nil || !p.limited() {
		return apps, err
	}
	return visibleApps(p, apps), nil
}

func (s *Server) deletePreview(r *http.Request, p principal) (any, error) {
	return s.eng.DeletePreview(r.Context(), p.name, r.PathValue("name"), r.URL.Query().Get("reason"))
}

func (s *Server) createPlan(r *http.Request, p principal) (any, error) {
	var req engine.PlanRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if p.limited() {
		app := req.Delete
		switch {
		case req.Spec != nil:
			app = req.Spec.Name
		case req.Patch != nil:
			app = req.Patch.Name
		case req.Restore != nil:
			app = req.Restore.App
		}
		if err := s.allowApp(r.Context(), p, app); err != nil {
			return nil, err
		}
	}
	return s.eng.Plan(r.Context(), p.name, req)
}

func (s *Server) getPlan(r *http.Request, p principal) (any, error) {
	if err := s.allowPlan(r, p); err != nil {
		return nil, err
	}
	return s.eng.GetPlan(r.Context(), r.PathValue("id"))
}

type ApplyRequest struct {
	Reason string `json:"reason"`
}

func (s *Server) applyPlan(r *http.Request, p principal) (any, error) {
	var req ApplyRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.allowPlan(r, p); err != nil {
		return nil, err
	}
	return s.eng.Apply(r.Context(), p.name, r.PathValue("id"), req.Reason)
}

func (s *Server) approvePlan(r *http.Request, p principal) (any, error) {
	return s.eng.Approve(r.Context(), p.name, r.PathValue("id"))
}

func (s *Server) audit(r *http.Request, p principal) (any, error) {
	limit, err := intQuery(r, "limit", 50, 1000)
	if err != nil {
		return nil, err
	}
	entries, err := s.eng.Audit(r.Context(), limit)
	if err != nil || !p.limited() {
		return entries, err
	}
	// A limited token sees its own actions and those on its apps.
	out := entries[:0:0]
	for _, e := range entries {
		if e.Actor == p.name || s.allowApp(r.Context(), p, e.Target) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}

// SystemView is the server's version and update status, plus what it runs
// on.
type SystemView struct {
	update.Status
	Host engine.HostInfo `json:"host"`
}

func (s *Server) system(r *http.Request, _ principal) (any, error) {
	st := update.Status{Version: "unknown", DisabledReason: "updates are not configured on this server", State: "idle"}
	if s.upd != nil {
		st = s.upd.Status()
	}
	return SystemView{Status: st, Host: s.eng.Host(r.Context())}, nil
}

func (s *Server) secretsStatus(r *http.Request, _ principal) (any, error) {
	return s.st.SecretsStatus(r.Context())
}

func (s *Server) rotateSecrets(r *http.Request, p principal) (any, error) {
	if s.rotateKey == nil {
		return nil, apierr.New(409, "rotation_unavailable", "key rotation is not available on this server")
	}
	res, err := s.rotateKey(r.Context())
	if err != nil {
		return nil, err
	}
	_ = s.st.AddAudit(r.Context(), store.AuditEntry{Actor: p.name, Action: "secrets.rotate", Target: fmt.Sprint(res["key_id"])})
	return res, nil
}

func (s *Server) systemCheck(r *http.Request, _ principal) (any, error) {
	if s.upd == nil {
		return nil, apierr.New(409, "updates_disabled", "updates are not configured on this server")
	}
	if err := s.upd.Check(r.Context()); err != nil {
		return nil, apierr.New(502, "update_check_failed", "%v", err)
	}
	return s.upd.Status(), nil
}

// systemUpgrade starts an upgrade in the background: the server waits for
// running deployments, installs the new binary and restarts itself.
func (s *Server) systemUpgrade(_ *http.Request, p principal) (any, error) {
	if s.upd == nil {
		return nil, apierr.New(409, "updates_disabled", "updates are not configured on this server")
	}
	st := s.upd.Status()
	if !st.Enabled {
		return nil, apierr.New(409, "updates_disabled", "updates are disabled: %s", st.DisabledReason)
	}
	if !st.UpdateAvailable {
		return nil, apierr.New(409, "no_update_available", "already running the latest version (%s)", st.Version).
			WithHint("check again with POST /v1/system/check (coldship upgrade --check)")
	}
	if st.State != "idle" {
		return nil, apierr.New(409, "upgrade_in_progress", "an upgrade is already %s", st.State)
	}
	_ = s.st.AddAudit(context.Background(), store.AuditEntry{Actor: p.name, Action: "system.upgrade", Target: st.Latest,
		Detail: []byte(`{"from":"` + st.Version + `"}`)})
	go func() {
		if err := s.upd.Upgrade(context.Background(), true); err != nil {
			s.log.Error("upgrade failed", "err", err)
		}
	}()
	time.Sleep(50 * time.Millisecond)
	return s.upd.Status(), nil
}

type TokenRequest struct {
	Name  string   `json:"name"`
	Scope string   `json:"scope"`
	Apps  []string `json:"apps,omitempty"`
}

type TokenResponse struct {
	Name  string   `json:"name"`
	Scope string   `json:"scope"`
	Apps  []string `json:"apps,omitempty"`
	Token string   `json:"token"`
}

func (s *Server) createToken(r *http.Request, p principal) (any, error) {
	var req TokenRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	scope, err := store.ParseScope(req.Scope)
	if err != nil {
		return nil, err
	}
	tok, err := s.eng.CreateToken(r.Context(), p.name, req.Name, scope, req.Apps)
	if err != nil {
		return nil, err
	}
	return TokenResponse{Name: req.Name, Scope: string(scope), Apps: req.Apps, Token: tok}, nil
}

func (s *Server) listTokens(r *http.Request, _ principal) (any, error) {
	return s.eng.ListTokens(r.Context())
}

func (s *Server) revokeToken(r *http.Request, p principal) (any, error) {
	name := r.PathValue("name")
	if err := s.eng.RevokeToken(r.Context(), p.name, name, r.URL.Query().Get("reason")); err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "revoked": true}, nil
}

// --- data services ---

func dataNotAllowed(p principal, name string) error {
	return apierr.New(403, "app_not_allowed", "token %q can't use data service %q: it is limited to %s", p.name, name, strings.Join(p.apps, ", ")).
		WithDetail("allowed_apps", p.apps)
}

func (s *Server) listData(r *http.Request, p principal) (any, error) {
	list, err := s.eng.ListData(r.Context())
	if err != nil || !p.limited() {
		return list, err
	}
	out := list[:0:0]
	for _, d := range list {
		if (d.App == "" && store.MatchApps(p.apps, d.Name)) || (d.App != "" && s.allowApp(r.Context(), p, d.App) == nil) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *Server) createData(r *http.Request, p principal) (any, error) {
	var req engine.DataRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if p.limited() && !store.MatchApps(p.apps, req.Name) {
		return nil, dataNotAllowed(p, req.Name)
	}
	return s.eng.CreateData(r.Context(), p.name, req)
}

func (s *Server) getData(r *http.Request, _ principal) (any, error) {
	return s.eng.GetData(r.Context(), r.PathValue("name"))
}

func (s *Server) deleteData(r *http.Request, p principal) (any, error) {
	name := r.PathValue("name")
	if err := s.eng.DeleteData(r.Context(), p.name, name, r.URL.Query().Get("reason")); err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "deleted": true}, nil
}

func (s *Server) dataConnection(r *http.Request, p principal) (any, error) {
	return s.eng.DataConnection(r.Context(), p.name, r.PathValue("name"))
}

func (s *Server) dataSnapshots(r *http.Request, _ principal) (any, error) {
	return s.eng.DataSnapshots(r.Context(), r.PathValue("name"))
}

type DataSnapshotRequest struct {
	Reason string `json:"reason,omitempty"`
}

func (s *Server) snapshotData(r *http.Request, p principal) (any, error) {
	var req DataSnapshotRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return s.eng.SnapshotData(r.Context(), p.name, r.PathValue("name"), req.Reason)
}

type DataRestoreRequest struct {
	Snapshot string `json:"snapshot"`
	Reason   string `json:"reason"`
}

func (s *Server) restoreData(r *http.Request, p principal) (any, error) {
	var req DataRestoreRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Reason) == "" {
		return nil, apierr.New(400, "reason_required", "restoring replaces all data; give a reason")
	}
	msg, err := s.eng.RestoreData(r.Context(), p.name, r.PathValue("name"), req.Snapshot, req.Reason)
	if err != nil {
		return nil, err
	}
	return map[string]any{"message": msg}, nil
}

// streamDeployLog follows a deployment's log as server-sent events: one
// "data:" event per line, then an "end" event with the deployment's status
// when it finishes. The lines come from the build and the app: untrusted.
func (s *Server) streamDeployLog(w http.ResponseWriter, r *http.Request) {
	p, err := s.auth(r)
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	id := r.PathValue("id")
	app, lines, next, stop, err := s.eng.FollowDeployLog(r.Context(), id)
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	defer stop()
	if p.limited() {
		if err := s.allowApp(r.Context(), p, app); err != nil {
			writeErr(w, s.log, err)
			return
		}
	}
	// The stream outlives the server's read deadline for request bodies
	// (it has none to read): clear it, or the connection drops after 30s.
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(line string) {
		fmt.Fprintf(w, "data: %s\n\n", line)
	}
	for _, l := range lines {
		send(l)
	}
	if flusher != nil {
		flusher.Flush()
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
		case l, ok := <-next:
			if !ok {
				status := ""
				if d, err := s.st.GetDeployment(r.Context(), id); err == nil && d != nil {
					status = d.Status
				}
				fmt.Fprintf(w, "event: end\ndata: %s\n\n", status)
				if flusher != nil {
					flusher.Flush()
				}
				return
			}
			send(l)
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// serverStatus is the whole server's status: tokens limited to some apps
// don't see it.
func (s *Server) serverStatus(r *http.Request, p principal) (any, error) {
	if p.limited() {
		return nil, apierr.New(403, "app_not_allowed", "token %q is limited to some apps and can't see the whole server", p.name)
	}
	st := s.eng.Status(r.Context())
	return struct {
		engine.ServerStatus
		Version string `json:"version"`
	}{st, s.version()}, nil
}

func (s *Server) version() string {
	if s.upd == nil {
		return "unknown"
	}
	return s.upd.Status().Version
}
