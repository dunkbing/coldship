package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// AgentConfig is an AI agent an org connected: which one, its provider
// keys (encrypted), and what its sessions may do.
type AgentConfig struct {
	Org     string
	Name    string
	Kind    string // claude | codex | gemini | custom
	Command string // custom agents: the command that speaks ACP on stdio
	// Env holds the provider keys, like ANTHROPIC_API_KEY.
	Env       map[string]string
	Scope     Scope
	Apps      []string
	CreatedBy string
	CreatedAt time.Time
}

func (s *Store) SaveAgent(ctx context.Context, a *AgentConfig) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := json.Marshal(a.Env)
	if err != nil {
		return err
	}
	env, err := s.seal(b, "agents.env:"+a.Org+"/"+a.Name)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO agents(org, name, kind, command, env, scope, apps, created_by, created_at) VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(org, name) DO UPDATE SET kind=excluded.kind, command=excluded.command, env=excluded.env, scope=excluded.scope, apps=excluded.apps`,
		a.Org, a.Name, a.Kind, a.Command, env, string(a.Scope), joinApps(a.Apps), a.CreatedBy, ts(a.CreatedAt))
	return err
}

const agentCols = `org, name, kind, command, env, scope, apps, created_by, created_at`

func (s *Store) scanAgent(r scanner) (*AgentConfig, error) {
	var a AgentConfig
	var env, scope, apps, created string
	if err := r.Scan(&a.Org, &a.Name, &a.Kind, &a.Command, &env, &scope, &apps, &a.CreatedBy, &created); err != nil {
		return nil, err
	}
	plain, err := s.open(env, "agents.env:"+a.Org+"/"+a.Name)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(plain, &a.Env); err != nil {
		return nil, err
	}
	a.Scope, a.Apps = Scope(scope), splitApps(apps)
	a.CreatedAt, _ = time.Parse(tsFormat, created)
	return &a, nil
}

func (s *Store) GetAgent(ctx context.Context, org, name string) (*AgentConfig, error) {
	a, err := s.scanAgent(s.db.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE org = ? AND name = ?`, org, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

func (s *Store) ListAgents(ctx context.Context, org string) ([]AgentConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentCols+` FROM agents WHERE org = ? ORDER BY name`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentConfig{}
	for rows.Next() {
		a, err := s.scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAgent(ctx context.Context, org, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE org = ? AND name = ?`, org, name)
	return err
}

// DeleteTokensWithPrefix removes tokens (and their sessions) whose names
// start with prefix, like the agent session tokens left by a crash.
func (s *Store) DeleteTokensWithPrefix(ctx context.Context, prefix string) error {
	if strings.ContainsAny(prefix, "%_") || prefix == "" {
		return errors.New("invalid token prefix")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_name LIKE ? || '%'`, prefix); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE name LIKE ? || '%'`, prefix)
	return err
}
