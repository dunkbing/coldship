package api

import (
	"net/http"

	"github.com/dunkbing/coldship/internal/store"
)

func (s *Server) routeJobs() {
	s.route("GET /v1/apps/{name}/jobs", store.ScopeRead, s.listJobs)
	s.route("POST /v1/apps/{name}/jobs/{job}/run", store.ScopeDeploy, s.runJob)
	s.route("GET /v1/apps/{name}/job-runs", store.ScopeRead, s.jobRuns)
	s.route("GET /v1/apps/{name}/job-runs/{id}", store.ScopeRead, s.jobRun)
}

func (s *Server) listJobs(r *http.Request, _ principal) (any, error) {
	return s.eng.Jobs(r.Context(), r.PathValue("name"))
}

func (s *Server) runJob(r *http.Request, p principal) (any, error) {
	return s.eng.RunJob(r.Context(), p.name, r.PathValue("name"), r.PathValue("job"))
}

func (s *Server) jobRuns(r *http.Request, _ principal) (any, error) {
	return s.eng.JobRuns(r.Context(), r.PathValue("name"), r.URL.Query().Get("job"))
}

func (s *Server) jobRun(r *http.Request, _ principal) (any, error) {
	return s.eng.JobRun(r.Context(), r.PathValue("name"), r.PathValue("id"))
}
