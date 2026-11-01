// Package store persists platform state in SQLite: tokens, apps, plans,
// deployments and the audit log. SQLite keeps single-node installs to one
// file; the schema is plain SQL so moving to Postgres later is mechanical.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"hangar/internal/apierr"
	"hangar/internal/planner"
	"hangar/internal/spec"
)

type Scope string

const (
	ScopeRead   Scope = "read"
	ScopeDeploy Scope = "deploy"
	ScopeAdmin  Scope = "admin"
)

var scopeRank = map[Scope]int{ScopeRead: 1, ScopeDeploy: 2, ScopeAdmin: 3}

func (s Scope) Allows(need Scope) bool { return scopeRank[s] >= scopeRank[need] }

func ParseScope(s string) (Scope, error) {
	if _, ok := scopeRank[Scope(s)]; !ok {
		return "", apierr.New(400, "invalid_scope", "scope %q must be one of read, deploy, admin", s)
	}
	return Scope(s), nil
}

type Token struct {
	Name      string    `json:"name"`
	Scope     Scope     `json:"scope"`
	CreatedAt time.Time `json:"created_at"`
}

type App struct {
	Name              string    `json:"name"`
	Spec              *spec.App `json:"spec,omitempty"` // last successfully deployed spec
	CurrentDeployment string    `json:"current_deployment,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Plan statuses.
const (
	PlanPending = "pending"
	PlanApplied = "applied"
	PlanExpired = "expired"
)

type Plan struct {
	ID               string           `json:"id"`
	App              string           `json:"app"`
	Action           string           `json:"action"` // create|update|delete|redeploy|noop
	Desired          *spec.App        `json:"desired,omitempty"`
	BaseDeployment   string           `json:"base_deployment,omitempty"`
	Changes          []planner.Change `json:"changes"`
	Destructive      bool             `json:"destructive"`
	Downtime         bool             `json:"downtime"`
	RequiresApproval bool             `json:"requires_approval"`
	ApprovedBy       string           `json:"approved_by,omitempty"`
	Status           string           `json:"status"`
	CreatedBy        string           `json:"created_by"`
	CreatedAt        time.Time        `json:"created_at"`
	ExpiresAt        time.Time        `json:"expires_at"`
	AppliedAt        *time.Time       `json:"applied_at,omitempty"`
	Deployment       string           `json:"deployment,omitempty"`
	NextStep         string           `json:"next_step"`
}

// Deployment statuses.
const (
	DeployRunning    = "running"
	DeploySuperseded = "superseded"
	DeployFailed     = "failed"
	DeployDeleted    = "deleted"
	DeployInProgress = "deploying"
)

type Deployment struct {
	ID         string     `json:"id"`
	App        string     `json:"app"`
	PlanID     string     `json:"plan_id"`
	Spec       *spec.App  `json:"spec,omitempty"`
	Status     string     `json:"status"`
	Error      string     `json:"error,omitempty"`
	Actor      string     `json:"actor"`
	Reason     string     `json:"reason"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type AuditEntry struct {
	ID     int64           `json:"id"`
	Time   time.Time       `json:"time"`
	Actor  string          `json:"actor"`
	Action string          `json:"action"`
	Target string          `json:"target"`
	Reason string          `json:"reason,omitempty"`
	Detail json.RawMessage `json:"detail,omitempty"`
}

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS tokens (
  name TEXT PRIMARY KEY, hash TEXT NOT NULL UNIQUE, scope TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS apps (
  name TEXT PRIMARY KEY, spec TEXT, current_deployment TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS plans (
  id TEXT PRIMARY KEY, app TEXT NOT NULL, status TEXT NOT NULL, body TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS deployments (
  id TEXT PRIMARY KEY, app TEXT NOT NULL, status TEXT NOT NULL, body TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS deployments_by_app ON deployments(app, created_at);
CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL,
  target TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', detail TEXT);
`

// Fixed-width UTC timestamps so text ordering equals time ordering.
const tsFormat = "2006-01-02T15:04:05.000000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsFormat) }

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// --- tokens ---

func hashToken(t string) string { h := sha256.Sum256([]byte(t)); return hex.EncodeToString(h[:]) }

// NewID returns a short random id with a readable prefix, e.g. plan_1a2b3c4d5e6f.
func NewID(prefix string) string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b)
}

// CreateToken stores a new token and returns its plaintext once.
func (s *Store) CreateToken(ctx context.Context, name string, scope Scope) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	plain := "hgr_" + hex.EncodeToString(b)
	_, err := s.db.ExecContext(ctx, `INSERT INTO tokens(name, hash, scope, created_at) VALUES(?,?,?,?)`,
		name, hashToken(plain), string(scope), ts(time.Now()))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return "", apierr.New(409, "token_exists", "a token named %q already exists", name)
		}
		return "", err
	}
	return plain, nil
}

func (s *Store) LookupToken(ctx context.Context, plain string) (*Token, error) {
	var t Token
	var scope, created string
	err := s.db.QueryRowContext(ctx, `SELECT name, scope, created_at FROM tokens WHERE hash = ?`, hashToken(plain)).
		Scan(&t.Name, &scope, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Scope = Scope(scope)
	t.CreatedAt, _ = time.Parse(tsFormat, created)
	return &t, nil
}

func (s *Store) CountTokens(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM tokens`).Scan(&n)
	return n, err
}

// --- apps ---

func (s *Store) GetApp(ctx context.Context, name string) (*App, error) {
	row := s.db.QueryRowContext(ctx, `SELECT name, spec, current_deployment, created_at, updated_at FROM apps WHERE name = ?`, name)
	a, err := scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

func (s *Store) ListApps(ctx context.Context) ([]App, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, spec, current_deployment, created_at, updated_at FROM apps ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []App{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanApp(r scanner) (*App, error) {
	var a App
	var specJSON sql.NullString
	var created, updated string
	if err := r.Scan(&a.Name, &specJSON, &a.CurrentDeployment, &created, &updated); err != nil {
		return nil, err
	}
	if specJSON.Valid {
		a.Spec = &spec.App{}
		if err := json.Unmarshal([]byte(specJSON.String), a.Spec); err != nil {
			return nil, err
		}
	}
	a.CreatedAt, _ = time.Parse(tsFormat, created)
	a.UpdatedAt, _ = time.Parse(tsFormat, updated)
	return &a, nil
}

func (s *Store) UpsertApp(ctx context.Context, a *App) error {
	var specJSON sql.NullString
	if a.Spec != nil {
		b, err := json.Marshal(a.Spec)
		if err != nil {
			return err
		}
		specJSON = sql.NullString{String: string(b), Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO apps(name, spec, current_deployment, created_at, updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET spec=excluded.spec, current_deployment=excluded.current_deployment, updated_at=excluded.updated_at`,
		a.Name, specJSON, a.CurrentDeployment, ts(a.CreatedAt), ts(a.UpdatedAt))
	return err
}

func (s *Store) DeleteApp(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM apps WHERE name = ?`, name)
	return err
}

// --- plans and deployments (stored as JSON documents) ---

func (s *Store) SavePlan(ctx context.Context, p *Plan) error {
	return s.saveDoc(ctx, "plans", p.ID, p.App, p.Status, p.CreatedAt, p)
}

func (s *Store) GetPlan(ctx context.Context, id string) (*Plan, error) {
	var p Plan
	ok, err := s.getDoc(ctx, "plans", id, &p)
	if !ok || err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) SaveDeployment(ctx context.Context, d *Deployment) error {
	return s.saveDoc(ctx, "deployments", d.ID, d.App, d.Status, d.CreatedAt, d)
}

func (s *Store) GetDeployment(ctx context.Context, id string) (*Deployment, error) {
	var d Deployment
	ok, err := s.getDoc(ctx, "deployments", id, &d)
	if !ok || err != nil {
		return nil, err
	}
	return &d, nil
}

// ListDeployments returns an app's deployments, newest first.
func (s *Store) ListDeployments(ctx context.Context, app string, limit int) ([]Deployment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM deployments WHERE app = ? ORDER BY created_at DESC, rowid DESC LIMIT ?`, app, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Deployment{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var d Deployment
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) saveDoc(ctx context.Context, table, id, app, status string, created time.Time, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO `+table+`(id, app, status, body, created_at) VALUES(?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status, body=excluded.body`, id, app, status, string(b), ts(created))
	return err
}

func (s *Store) getDoc(ctx context.Context, table, id string, v any) (bool, error) {
	var body string
	err := s.db.QueryRowContext(ctx, `SELECT body FROM `+table+` WHERE id = ?`, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(body), v)
}

// --- audit ---

func (s *Store) AddAudit(ctx context.Context, e AuditEntry) error {
	var detail sql.NullString
	if len(e.Detail) > 0 {
		detail = sql.NullString{String: string(e.Detail), Valid: true}
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit(ts, actor, action, target, reason, detail) VALUES(?,?,?,?,?,?)`,
		ts(e.Time), e.Actor, e.Action, e.Target, e.Reason, detail)
	return err
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, actor, action, target, reason, detail FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var t string
		var detail sql.NullString
		if err := rows.Scan(&e.ID, &t, &e.Actor, &e.Action, &e.Target, &e.Reason, &detail); err != nil {
			return nil, err
		}
		e.Time, _ = time.Parse(tsFormat, t)
		if detail.Valid {
			e.Detail = json.RawMessage(detail.String)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
