package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AgentSessionRecord is a saved agent session: what the list shows, plus
// Data (the transcript and the agent's settings, as JSON), which is
// encrypted because it can quote apps' logs and config.
type AgentSessionRecord struct {
	ID        string
	Org       string
	Agent     string
	Kind      string
	Scope     Scope
	StartedBy string
	Title     string
	Status    string
	Error     string
	AgentInfo string
	Alert     string
	AlertApp  string
	ACPID     string // the agent's own session id, for loading it again
	CreatedAt time.Time
	UpdatedAt time.Time
	Data      []byte // nil when listed
}

func (s *Store) SaveAgentSession(ctx context.Context, r *AgentSessionRecord) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := s.seal(r.Data, "agent_sessions.data:"+r.ID)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO agent_sessions(id, org, agent, kind, scope, started_by, title, status, error, agent_info, alert, alert_app, acp_id, created_at, updated_at, data)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET scope=excluded.scope, title=excluded.title, status=excluded.status, error=excluded.error,
		agent_info=excluded.agent_info, acp_id=excluded.acp_id, updated_at=excluded.updated_at, data=excluded.data`,
		r.ID, r.Org, r.Agent, r.Kind, string(r.Scope), r.StartedBy, r.Title, r.Status, r.Error, r.AgentInfo, r.Alert, r.AlertApp, r.ACPID,
		ts(r.CreatedAt), ts(r.UpdatedAt), data)
	return err
}

const agentSessionCols = `id, org, agent, kind, scope, started_by, title, status, error, agent_info, alert, alert_app, acp_id, created_at, updated_at`

func scanAgentSession(r scanner, data *string) (*AgentSessionRecord, error) {
	var a AgentSessionRecord
	var scope, created, updated string
	dest := []any{&a.ID, &a.Org, &a.Agent, &a.Kind, &scope, &a.StartedBy, &a.Title, &a.Status, &a.Error, &a.AgentInfo, &a.Alert, &a.AlertApp, &a.ACPID, &created, &updated}
	if data != nil {
		dest = append(dest, data)
	}
	if err := r.Scan(dest...); err != nil {
		return nil, err
	}
	a.Scope = Scope(scope)
	a.CreatedAt, _ = time.Parse(tsFormat, created)
	a.UpdatedAt, _ = time.Parse(tsFormat, updated)
	return &a, nil
}

// GetAgentSession returns an org's saved session with its data, or nil.
func (s *Store) GetAgentSession(ctx context.Context, org, id string) (*AgentSessionRecord, error) {
	var data string
	a, err := scanAgentSession(s.db.QueryRowContext(ctx, `SELECT `+agentSessionCols+`, data FROM agent_sessions WHERE id = ? AND org = ?`, id, org), &data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if a.Data, err = s.open(data, "agent_sessions.data:"+a.ID); err != nil {
		return nil, err
	}
	return a, nil
}

// ListAgentSessions lists an org's saved sessions, newest first, without
// their data.
func (s *Store) ListAgentSessions(ctx context.Context, org string, limit int) ([]AgentSessionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentSessionCols+` FROM agent_sessions WHERE org = ? ORDER BY created_at DESC, rowid DESC LIMIT ?`, org, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentSessionRecord
	for rows.Next() {
		a, err := scanAgentSession(rows, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// PruneAgentSessions keeps an org's newest keep sessions.
func (s *Store) PruneAgentSessions(ctx context.Context, org string, keep int) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agent_sessions WHERE org = ? AND id NOT IN
		(SELECT id FROM agent_sessions WHERE org = ? ORDER BY created_at DESC, rowid DESC LIMIT ?)`, org, org, keep)
	return err
}

// EndAgentSessions marks sessions still running (from before a restart)
// as ended.
func (s *Store) EndAgentSessions(ctx context.Context, why string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_sessions SET status = 'ended', error = ? WHERE status NOT IN ('ended', 'failed')`, why)
	return err
}

func (s *Store) DeleteAgentSession(ctx context.Context, org, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agent_sessions WHERE id = ? AND org = ?`, id, org)
	return err
}
