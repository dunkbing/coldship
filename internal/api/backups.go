package api

import (
	"net/http"
	"strings"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/offsite"
	"github.com/dunkbing/coldship/internal/store"
)

func (s *Server) routeBackups() {
	s.route("GET /v1/backups", store.ScopeRead, unlimited(s.backupStatus))
	s.route("PUT /v1/backups", store.ScopeAdmin, unlimited(s.saveBackup))
	s.route("DELETE /v1/backups", store.ScopeAdmin, unlimited(s.deleteBackup))
	s.route("POST /v1/backups/test", store.ScopeAdmin, unlimited(s.testBackup))
	s.route("POST /v1/backups/sync", store.ScopeAdmin, unlimited(s.syncBackups))
	s.route("GET /v1/backups/list", store.ScopeRead, unlimited(s.listBackups))
	s.route("POST /v1/apps/{name}/databases/{db}/import", store.ScopeAdmin, unlimited(s.importBackup))
}

// unlimited keeps tokens limited to some apps out: a bucket holds every
// database of the org.
func unlimited(h handler) handler {
	return func(r *http.Request, p principal) (any, error) {
		if p.limited() {
			return nil, apierr.New(403, "app_not_allowed", "token %q is limited to some apps and can't use the org's backups", p.name)
		}
		return h(r, p)
	}
}

func (s *Server) backupStatus(r *http.Request, _ principal) (any, error) {
	return s.eng.BackupStatus(r.Context())
}

func (s *Server) saveBackup(r *http.Request, p principal) (any, error) {
	var cfg offsite.Config
	if err := decode(r, &cfg); err != nil {
		return nil, err
	}
	return s.eng.SaveBackup(r.Context(), p.name, cfg)
}

func (s *Server) deleteBackup(r *http.Request, p principal) (any, error) {
	if err := s.eng.DeleteBackup(r.Context(), p.name); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true}, nil
}

func (s *Server) testBackup(r *http.Request, _ principal) (any, error) {
	var cfg offsite.Config
	if err := decode(r, &cfg); err != nil {
		return nil, err
	}
	if err := s.eng.TestBackup(r.Context(), cfg); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (s *Server) syncBackups(r *http.Request, _ principal) (any, error) {
	s.eng.SyncBackups(r.Context(), store.OrgOr(store.OrgFrom(r.Context())))
	st, err := s.eng.BackupStatus(r.Context())
	if err != nil {
		return nil, err
	}
	if st.LastError != "" {
		return nil, apierr.New(502, "backup_failed", "%s", st.LastError)
	}
	return st, nil
}

func (s *Server) listBackups(r *http.Request, p principal) (any, error) {
	app, db := r.URL.Query().Get("app"), r.URL.Query().Get("database")
	if app != "" {
		if err := s.inOrg(r.Context(), p, app); err != nil {
			return nil, err
		}
	} else if db != "" {
		if err := s.dataInOrg(r.Context(), p, db); err != nil {
			return nil, err
		}
	}
	return s.eng.Backups(r.Context(), app, db)
}

type importRequest struct {
	Backup string `json:"backup"`
}

func (s *Server) importBackup(r *http.Request, p principal) (any, error) {
	var req importRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Backup) == "" {
		return nil, apierr.New(400, "invalid_request", "give the backup's key").WithField("backup")
	}
	return s.eng.ImportAppBackup(r.Context(), p.name, r.PathValue("name"), r.PathValue("db"), req.Backup)
}
