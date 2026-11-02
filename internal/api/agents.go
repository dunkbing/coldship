package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dunkbing/coldship/internal/agents"
	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/store"
)

// SetAgents enables the AI agents endpoints.
func (s *Server) SetAgents(m *agents.Manager) { s.agents = m }

func (s *Server) routeAgents() {
	s.route("GET /v1/agents", store.ScopeRead, s.agentsOnly(s.listAgents))
	s.route("PUT /v1/agents/{agent}", store.ScopeAdmin, s.agentsOnly(s.saveAgent))
	s.route("DELETE /v1/agents/{agent}", store.ScopeAdmin, s.agentsOnly(s.deleteAgent))
	s.route("POST /v1/agents/{agent}/sessions", store.ScopeDeploy, s.agentsOnly(s.startAgentSession))
	s.route("GET /v1/agent-sessions", store.ScopeRead, s.agentsOnly(s.listAgentSessions))
	s.route("GET /v1/agent-sessions/{id}", store.ScopeRead, s.agentsOnly(s.getAgentSession))
	s.route("POST /v1/agent-sessions/{id}/prompt", store.ScopeDeploy, s.agentsOnly(s.promptAgent))
	s.route("POST /v1/agent-sessions/{id}/permissions/{perm}", store.ScopeDeploy, s.agentsOnly(s.answerAgent))
	s.route("POST /v1/agent-sessions/{id}/cancel", store.ScopeDeploy, s.agentsOnly(s.cancelAgent))
	s.route("DELETE /v1/agent-sessions/{id}", store.ScopeDeploy, s.agentsOnly(s.stopAgentSession))
	s.route("POST /v1/agent-sessions/{id}/resume", store.ScopeDeploy, s.agentsOnly(s.resumeAgentSession))
	s.route("POST /v1/agent-sessions/{id}/settings", store.ScopeDeploy, s.agentsOnly(s.configureAgent))
	s.route("DELETE /v1/agent-sessions/{id}/transcript", store.ScopeDeploy, s.agentsOnly(s.forgetAgentSession))
	s.mux.HandleFunc("GET /v1/agent-sessions/{id}/stream", s.streamAgentSession)
}

// agentsOnly keeps agents for people and full tokens: not tokens limited
// to some apps, and not agents' own tokens (no agent starts another).
func (s *Server) agentsOnly(h handler) handler {
	return func(r *http.Request, p principal) (any, error) {
		if err := s.agentAccess(p); err != nil {
			return nil, err
		}
		return h(r, p)
	}
}

func (s *Server) agentAccess(p principal) error {
	if s.agents == nil {
		return apierr.New(409, "agents_unavailable", "AI agents aren't enabled on this server")
	}
	if p.limited() || strings.HasPrefix(p.name, agents.TokenPrefix) {
		return apierr.New(403, "agents_not_allowed", "%s can't use AI agents: that needs a person, or a token for every app", p.name)
	}
	return nil
}

type AgentsView struct {
	Presets []agents.Preset    `json:"presets"`
	Agents  []agents.AgentView `json:"agents"`
}

func (s *Server) listAgents(r *http.Request, p principal) (any, error) {
	list, err := s.agents.List(r.Context(), p.org)
	return AgentsView{Presets: agents.Presets, Agents: list}, err
}

func (s *Server) saveAgent(r *http.Request, p principal) (any, error) {
	var req agents.ConfigRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return s.agents.Save(r.Context(), p.org, p.name, r.PathValue("agent"), req)
}

func (s *Server) deleteAgent(r *http.Request, p principal) (any, error) {
	name := r.PathValue("agent")
	if err := s.agents.Delete(r.Context(), p.org, p.name, name); err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "deleted": true}, nil
}

type StartSessionRequest struct {
	// Prompt is sent as the first message once the agent is ready.
	Prompt string `json:"prompt,omitempty"`
}

func (s *Server) startAgentSession(r *http.Request, p principal) (any, error) {
	var req StartSessionRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if len(req.Prompt) > 20000 {
		return nil, apierr.Invalid("prompt", "the prompt is too long")
	}
	return s.agents.StartSession(r.Context(), agents.StartRequest{Org: p.org, Agent: r.PathValue("agent"), Actor: p.name, Caller: p.scope, Prompt: req.Prompt})
}

func (s *Server) resumeAgentSession(r *http.Request, p principal) (any, error) {
	return s.agents.Resume(r.Context(), p.org, r.PathValue("id"), p.name, p.scope)
}

func (s *Server) configureAgent(r *http.Request, p principal) (any, error) {
	ss, err := s.agents.Session(p.org, r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	var req agents.SettingRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := ss.Configure(p.name, req); err != nil {
		return nil, err
	}
	return ss.View(false), nil
}

func (s *Server) forgetAgentSession(r *http.Request, p principal) (any, error) {
	id := r.PathValue("id")
	if err := s.agents.Forget(r.Context(), p.org, id, p.name); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "deleted": true}, nil
}

func (s *Server) listAgentSessions(_ *http.Request, p principal) (any, error) {
	return s.agents.Sessions(p.org), nil
}

func (s *Server) getAgentSession(r *http.Request, p principal) (any, error) {
	ss, err := s.agents.Session(p.org, r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	return ss.View(true), nil
}

type PromptRequest struct {
	Text string `json:"text"`
}

func (s *Server) promptAgent(r *http.Request, p principal) (any, error) {
	ss, err := s.agents.Session(p.org, r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	var req PromptRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := ss.Prompt(p.name, req.Text); err != nil {
		return nil, err
	}
	return map[string]bool{"sent": true}, nil
}

type AnswerRequest struct {
	Option string `json:"option"`
}

func (s *Server) answerAgent(r *http.Request, p principal) (any, error) {
	ss, err := s.agents.Session(p.org, r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	var req AnswerRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := ss.Answer(p.name, r.PathValue("perm"), req.Option); err != nil {
		return nil, err
	}
	return map[string]bool{"answered": true}, nil
}

func (s *Server) cancelAgent(r *http.Request, p principal) (any, error) {
	ss, err := s.agents.Session(p.org, r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	ss.Cancel()
	return map[string]bool{"cancelled": true}, nil
}

func (s *Server) stopAgentSession(r *http.Request, p principal) (any, error) {
	ss, err := s.agents.Session(p.org, r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	ss.Stop(p.name)
	return map[string]bool{"ended": true}, nil
}

// streamAgentSession sends a session as server-sent events: a snapshot,
// then each change, as JSON.
func (s *Server) streamAgentSession(w http.ResponseWriter, r *http.Request) {
	p, err := s.auth(r)
	if err == nil {
		err = s.agentAccess(p)
	}
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	ss, err := s.agents.Session(p.org, r.PathValue("id"))
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	events, stop := ss.Subscribe()
	defer stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.closing:
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
		case e, ok := <-events:
			if !ok {
				return // too slow: the client reconnects and gets a snapshot
			}
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}
