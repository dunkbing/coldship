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
	// PreviewOf names the app this is a preview of; previews are deleted
	// automatically at ExpiresAt.
	PreviewOf string     `json:"preview_of,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
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
	// Restore is set for plans that restore a database from a snapshot.
	Restore  *PlanRestore `json:"restore,omitempty"`
	NextStep string       `json:"next_step"`
}

type PlanRestore struct {
	Database string `json:"database"`
	Snapshot string `json:"snapshot"`
}

// Deployment statuses.
const (
	DeployRunning    = "running"
	DeploySuperseded = "superseded"
	DeployFailed     = "failed"
	DeployDeleted    = "deleted"
	DeployInProgress = "deploying"
	DeployBuilding   = "building"
)

// Phases a deployment can fail in. A failed build never touches the
// running container; a failed deploy may have.
const (
	PhaseBuild  = "build"
	PhaseDeploy = "deploy"
)

type Deployment struct {
	ID        string    `json:"id"`
	App       string    `json:"app"`
	PlanID    string    `json:"plan_id"`
	Spec      *spec.App `json:"spec,omitempty"`
	Status    string    `json:"status"`
	Image     string    `json:"image,omitempty"`  // image that ran (built tag for git builds)
	Commit    string    `json:"commit,omitempty"` // git commit, for builds
	Error     string    `json:"error,omitempty"`
	ErrorCode string    `json:"error_code,omitempty"`
	FailedIn  string    `json:"failed_in,omitempty"` // build | deploy
	// KeptPrevious is true when the deployment failed without touching the
	// previous version, which kept running and serving traffic.
	KeptPrevious bool       `json:"kept_previous,omitempty"`
	Strategy     string     `json:"strategy,omitempty"` // zero-downtime | recreate`
	Actor        string     `json:"actor"`
	Reason       string     `json:"reason"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
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
CREATE TABLE IF NOT EXISTS deploy_logs (
  deployment TEXT PRIMARY KEY, lines TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS databases (
  app TEXT NOT NULL, name TEXT NOT NULL, engine TEXT NOT NULL, version TEXT NOT NULL, password TEXT NOT NULL,
  created_at TEXT NOT NULL, PRIMARY KEY (app, name));
CREATE TABLE IF NOT EXISTS snapshots (
  id TEXT PRIMARY KEY, app TEXT NOT NULL, database TEXT NOT NULL, body TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS snapshots_by_db ON snapshots(app, database, created_at);
CREATE TABLE IF NOT EXISTS sessions (
  hash TEXT PRIMARY KEY, token_name TEXT NOT NULL, scope TEXT NOT NULL, created_at TEXT NOT NULL, expires_at TEXT NOT NULL);
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
	// Columns added after the first release. SQLite has no ADD COLUMN IF
	// NOT EXISTS, so "duplicate column" means already migrated.
	for _, stmt := range []string{
		`ALTER TABLE apps ADD COLUMN preview_of TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE apps ADD COLUMN expires_at TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
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

// --- sessions (dashboard sign-in) ---

// CreateSession creates a browser session acting as the given token and
// returns its plaintext id once. Only a hash is stored.
func (s *Store) CreateSession(ctx context.Context, t *Token, ttl time.Duration) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	plain := hex.EncodeToString(b)
	now := time.Now()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, ts(now)); err != nil {
		return "", err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(hash, token_name, scope, created_at, expires_at) VALUES(?,?,?,?,?)`,
		hashToken(plain), t.Name, string(t.Scope), ts(now), ts(now.Add(ttl)))
	return plain, err
}

// LookupSession returns the token identity behind a live session, or nil.
func (s *Store) LookupSession(ctx context.Context, plain string) (*Token, error) {
	var t Token
	var scope, expires string
	err := s.db.QueryRowContext(ctx, `SELECT token_name, scope, expires_at FROM sessions WHERE hash = ?`, hashToken(plain)).
		Scan(&t.Name, &scope, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if exp, _ := time.Parse(tsFormat, expires); time.Now().After(exp) {
		return nil, nil
	}
	t.Scope = Scope(scope)
	return &t, nil
}

func (s *Store) DeleteSession(ctx context.Context, plain string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE hash = ?`, hashToken(plain))
	return err
}

// --- managed databases ---

// Database holds a managed database's generated credentials. The password
// is only ever injected into the app and the database container.
type Database struct {
	App       string
	Name      string
	Engine    string
	Version   string
	Password  string
	CreatedAt time.Time
}

func (s *Store) GetDatabase(ctx context.Context, app, name string) (*Database, error) {
	var d Database
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT app, name, engine, version, password, created_at FROM databases WHERE app = ? AND name = ?`, app, name).
		Scan(&d.App, &d.Name, &d.Engine, &d.Version, &d.Password, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.CreatedAt, _ = time.Parse(tsFormat, created)
	return &d, nil
}

func (s *Store) SaveDatabase(ctx context.Context, d *Database) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO databases(app, name, engine, version, password, created_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(app, name) DO UPDATE SET engine=excluded.engine, version=excluded.version`,
		d.App, d.Name, d.Engine, d.Version, d.Password, ts(d.CreatedAt))
	return err
}

func (s *Store) DeleteDatabase(ctx context.Context, app, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM databases WHERE app = ? AND name = ?`, app, name)
	return err
}

type Snapshot struct {
	ID        string    `json:"id"`
	App       string    `json:"app"`
	Database  string    `json:"database"`
	Engine    string    `json:"engine"`
	Version   string    `json:"version"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Reason    string    `json:"reason"`
	Actor     string    `json:"actor"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) SaveSnapshot(ctx context.Context, sn *Snapshot) error {
	b, err := json.Marshal(sn)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO snapshots(id, app, database, body, created_at) VALUES(?,?,?,?,?)`,
		sn.ID, sn.App, sn.Database, string(b), ts(sn.CreatedAt))
	return err
}

func (s *Store) GetSnapshot(ctx context.Context, id string) (*Snapshot, error) {
	var body string
	err := s.db.QueryRowContext(ctx, `SELECT body FROM snapshots WHERE id = ?`, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sn Snapshot
	return &sn, json.Unmarshal([]byte(body), &sn)
}

// ListSnapshots returns an app's snapshots, newest first. An empty database
// name lists all of the app's databases.
func (s *Store) ListSnapshots(ctx context.Context, app, database string) ([]Snapshot, error) {
	q, args := `SELECT body FROM snapshots WHERE app = ?`, []any{app}
	if database != "" {
		q += ` AND database = ?`
		args = append(args, database)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at DESC, rowid DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Snapshot{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var sn Snapshot
		if err := json.Unmarshal([]byte(body), &sn); err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSnapshot(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM snapshots WHERE id = ?`, id)
	return err
}

// --- deployment logs (build output, startup, health checks) ---

func (s *Store) SaveDeployLog(ctx context.Context, deployment string, lines []string) error {
	b, err := json.Marshal(lines)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO deploy_logs(deployment, lines) VALUES(?,?)
		ON CONFLICT(deployment) DO UPDATE SET lines=excluded.lines`, deployment, string(b))
	return err
}

// GetDeployLog returns a deployment's log, or nil if it has none.
func (s *Store) GetDeployLog(ctx context.Context, deployment string) ([]string, error) {
	var body string
	err := s.db.QueryRowContext(ctx, `SELECT lines FROM deploy_logs WHERE deployment = ?`, deployment).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lines []string
	return lines, json.Unmarshal([]byte(body), &lines)
}

// --- apps ---

func (s *Store) GetApp(ctx context.Context, name string) (*App, error) {
	row := s.db.QueryRowContext(ctx, `SELECT name, spec, current_deployment, preview_of, expires_at, created_at, updated_at FROM apps WHERE name = ?`, name)
	a, err := scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

func (s *Store) ListApps(ctx context.Context) ([]App, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, spec, current_deployment, preview_of, expires_at, created_at, updated_at FROM apps ORDER BY name`)
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
	var created, updated, expires string
	if err := r.Scan(&a.Name, &specJSON, &a.CurrentDeployment, &a.PreviewOf, &expires, &created, &updated); err != nil {
		return nil, err
	}
	if expires != "" {
		if t, err := time.Parse(tsFormat, expires); err == nil {
			a.ExpiresAt = &t
		}
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
	expires := ""
	if a.ExpiresAt != nil {
		expires = ts(*a.ExpiresAt)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO apps(name, spec, current_deployment, preview_of, expires_at, created_at, updated_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET spec=excluded.spec, current_deployment=excluded.current_deployment,
		preview_of=excluded.preview_of, expires_at=excluded.expires_at, updated_at=excluded.updated_at`,
		a.Name, specJSON, a.CurrentDeployment, a.PreviewOf, expires, ts(a.CreatedAt), ts(a.UpdatedAt))
	return err
}

// SetPreview marks an app as a preview of parent that expires at expires.
func (s *Store) SetPreview(ctx context.Context, name, parent string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE apps SET preview_of = ?, expires_at = ? WHERE name = ?`, parent, ts(expires), name)
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

// ListPlans returns plans newest first, optionally filtered by status.
func (s *Store) ListPlans(ctx context.Context, status string, limit int) ([]Plan, error) {
	q, args := `SELECT body FROM plans`, []any{}
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC, rowid DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Plan{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var p Plan
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
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
