package api

import (
	"net/http"

	"github.com/dunkbing/coldship/internal/engine"
	"github.com/dunkbing/coldship/internal/store"
)

func (s *Server) routeChannels() {
	s.route("GET /v1/channels", store.ScopeRead, unlimited(s.listChannels))
	s.route("GET /v1/channels/events", store.ScopeRead, unlimited(s.channelEvents))
	s.route("POST /v1/channels", store.ScopeAdmin, unlimited(s.createChannel))
	s.route("PATCH /v1/channels/{id}", store.ScopeAdmin, unlimited(s.updateChannel))
	s.route("DELETE /v1/channels/{id}", store.ScopeAdmin, unlimited(s.deleteChannel))
	s.route("POST /v1/channels/{id}/test", store.ScopeAdmin, unlimited(s.testChannel))
}

func (s *Server) listChannels(r *http.Request, _ principal) (any, error) {
	return s.eng.ListChannels(r.Context())
}

// channelEvents lists the events this org's channels can get.
func (s *Server) channelEvents(_ *http.Request, p principal) (any, error) {
	out := []engine.EventKind{}
	for _, k := range engine.EventKinds {
		if !k.Server || p.org == store.DefaultOrg {
			out = append(out, k)
		}
	}
	return out, nil
}

func (s *Server) createChannel(r *http.Request, p principal) (any, error) {
	var req engine.ChannelRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return s.eng.SaveChannel(r.Context(), p.name, "", req)
}

func (s *Server) updateChannel(r *http.Request, p principal) (any, error) {
	var req engine.ChannelRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return s.eng.SaveChannel(r.Context(), p.name, r.PathValue("id"), req)
}

func (s *Server) deleteChannel(r *http.Request, p principal) (any, error) {
	if err := s.eng.DeleteChannel(r.Context(), p.name, r.PathValue("id")); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true}, nil
}

func (s *Server) testChannel(r *http.Request, _ principal) (any, error) {
	return s.eng.TestChannel(r.Context(), r.PathValue("id"))
}
