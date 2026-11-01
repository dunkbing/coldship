// Package api serves the engine over a JSON HTTP API with bearer-token auth.
// The CLI and MCP server are both clients of this API.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"hangar/internal/apierr"
	"hangar/internal/engine"
	"hangar/internal/store"
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
}

// New builds the API server. ui, if non-nil, serves the web dashboard for
// every path outside /v1/ and /healthz.
func New(eng *engine.Engine, st *store.Store, log *slog.Logger, ui http.Handler) *Server {
	s := &Server{eng: eng, st: st, mux: http.NewServeMux(), log: log}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	s.route("GET /v1/whoami", store.ScopeRead, s.whoami)
	s.route("GET /v1/apps", store.ScopeRead, s.listApps)
	s.route("GET /v1/apps/{name}", store.ScopeRead, s.getApp)
	s.route("GET /v1/apps/{name}/logs", store.ScopeRead, s.logs)
	s.route("GET /v1/apps/{name}/diagnose", store.ScopeRead, s.diagnose)
	s.route("GET /v1/apps/{name}/deployments", store.ScopeRead, s.deployments)
	s.route("POST /v1/apps/{name}/rollback", store.ScopeDeploy, s.rollback)
	s.route("POST /v1/plans", store.ScopeDeploy, s.createPlan)
	s.route("GET /v1/plans", store.ScopeRead, s.listPlans)
	s.route("GET /v1/plans/{id}", store.ScopeRead, s.getPlan)
	s.route("POST /v1/plans/{id}/apply", store.ScopeDeploy, s.applyPlan)
	s.route("POST /v1/plans/{id}/approve", store.ScopeAdmin, s.approvePlan)
	s.route("GET /v1/audit", store.ScopeRead, s.audit)
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

func (s *Server) auth(r *http.Request) (principal, error) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return principal{}, apierr.New(401, "unauthenticated", "missing bearer token").
			WithHint("set HANGAR_TOKEN or pass --token")
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
