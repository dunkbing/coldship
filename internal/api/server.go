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

	"hangar/internal/apierr"
	"hangar/internal/engine"
	"hangar/internal/store"
	"hangar/internal/update"
	"hangar/internal/webhook"
)

type principal struct {
	name  string
	scope store.Scope
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
	s.route("POST /v1/system/secrets/rotate", store.ScopeAdmin, s.rotateSecrets)
	s.route("POST /v1/system/check", store.ScopeAdmin, s.systemCheck)
	s.route("POST /v1/system/upgrade", store.ScopeAdmin, s.systemUpgrade)
	s.mux.HandleFunc("GET /v1/system/manifest", s.systemManifest)
	s.route("POST /v1/tokens", store.ScopeAdmin, s.createToken)
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

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

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
const SessionCookie = "hangar_session"

// CSRFHeader must accompany cookie-authenticated requests that change state.
// Browsers only send custom headers cross-origin after a CORS preflight, which
// this server never allows, so a forged cross-site request cannot carry it.
const CSRFHeader = "X-Hangar-Request"

const sessionTTL = 12 * time.Hour

func (s *Server) auth(r *http.Request) (principal, error) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		c, err := r.Cookie(SessionCookie)
		if err != nil || c.Value == "" {
			return principal{}, apierr.New(401, "unauthenticated", "missing bearer token").
				WithHint("set HANGAR_TOKEN or pass --token")
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
		return principal{name: t.Name, scope: t.Scope}, nil
	}
	t, err := s.st.LookupToken(r.Context(), tok)
	if err != nil {
		return principal{}, err
	}
	if t == nil {
		return principal{}, apierr.New(401, "unauthenticated", "unknown token")
	}
	return principal{name: t.Name, scope: t.Scope}, nil
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
}

func (s *Server) whoami(_ *http.Request, p principal) (any, error) {
	return WhoAmI{Name: p.name, Scope: p.scope}, nil
}

func (s *Server) listPlans(r *http.Request, _ principal) (any, error) {
	limit, err := intQuery(r, "limit", 50, 500)
	if err != nil {
		return nil, err
	}
	return s.eng.ListPlans(r.Context(), r.URL.Query().Get("status"), limit)
}

type SessionRequest struct {
	Token string `json:"token"`
}

// createSession exchanges an API token for an HttpOnly session cookie, so the
// dashboard never keeps the token itself.
func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) deployLog(r *http.Request, _ principal) (any, error) {
	return s.eng.DeployLog(r.Context(), r.PathValue("id"))
}

func (s *Server) listApps(r *http.Request, _ principal) (any, error) {
	return s.eng.ListApps(r.Context())
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

func (s *Server) detectBuild(r *http.Request, _ principal) (any, error) {
	var req engine.DetectRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return s.eng.DetectBuild(r.Context(), req)
}

func (s *Server) createPreview(r *http.Request, p principal) (any, error) {
	var req engine.PreviewRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return s.eng.CreatePreview(r.Context(), p.name, req)
}

func (s *Server) listPreviews(r *http.Request, _ principal) (any, error) {
	return s.eng.ListPreviews(r.Context())
}

func (s *Server) deletePreview(r *http.Request, p principal) (any, error) {
	return s.eng.DeletePreview(r.Context(), p.name, r.PathValue("name"), r.URL.Query().Get("reason"))
}

func (s *Server) createPlan(r *http.Request, p principal) (any, error) {
	var req engine.PlanRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return s.eng.Plan(r.Context(), p.name, req)
}

func (s *Server) getPlan(r *http.Request, _ principal) (any, error) {
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
	return s.eng.Apply(r.Context(), p.name, r.PathValue("id"), req.Reason)
}

func (s *Server) approvePlan(r *http.Request, p principal) (any, error) {
	return s.eng.Approve(r.Context(), p.name, r.PathValue("id"))
}

func (s *Server) audit(r *http.Request, _ principal) (any, error) {
	limit, err := intQuery(r, "limit", 50, 1000)
	if err != nil {
		return nil, err
	}
	return s.eng.Audit(r.Context(), limit)
}

func (s *Server) system(*http.Request, principal) (any, error) {
	if s.upd == nil {
		return update.Status{Version: "unknown", DisabledReason: "updates are not configured on this server", State: "idle"}, nil
	}
	return s.upd.Status(), nil
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
			WithHint("check again with POST /v1/system/check (hangar upgrade --check)")
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

// systemManifest returns the latest signed release manifest, so remote CLIs
// can verify and install it themselves (hangar self-update). Any token may
// read it; it contains nothing secret.
func (s *Server) systemManifest(w http.ResponseWriter, r *http.Request) {
	if _, err := s.auth(r); err != nil {
		writeErr(w, s.log, err)
		return
	}
	var pub *update.Published
	if s.upd != nil {
		pub = s.upd.Published()
	}
	if pub == nil {
		writeErr(w, s.log, apierr.New(404, "no_manifest", "this server has not fetched a release manifest yet"))
		return
	}
	writeJSON(w, http.StatusOK, pub)
}

type TokenRequest struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
}

type TokenResponse struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
	Token string `json:"token"`
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
	tok, err := s.eng.CreateToken(r.Context(), p.name, req.Name, scope)
	if err != nil {
		return nil, err
	}
	return TokenResponse{Name: req.Name, Scope: string(scope), Token: tok}, nil
}
