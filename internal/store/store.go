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
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/planner"
	"github.com/dunkbing/coldship/internal/secrets"
	"github.com/dunkbing/coldship/internal/spec"
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
	KeptPrevious bool   `json:"kept_previous,omitempty"`
	Strategy     string `json:"strategy,omitempty"` // zero-downtime | recreate
	// Detected summarizes the project detection when Coldship generated the
	// Dockerfile, e.g. "node 22 · pnpm · pnpm start".
	Detected   string     `json:"detected,omitempty"`
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

// Store keeps Coldship's state. Columns that can hold secrets (app specs with
// env values, plans and deployments that embed specs, database passwords,
// webhook secrets, deploy logs) are encrypted with the keyring; nothing
// is ever written to them in plaintext.
type Store struct {
	db *sql.DB
	kr atomic.Pointer[secrets.Keyring]
	// mu orders encrypting writes against key rotation: a write holds it
	// shared from sealing a value until it is stored, and rotation holds it
	// exclusively, so no value sealed with a superseded keyring lands after
	// the rotation has re-encrypted everything.
	mu sync.RWMutex
	// ScrubWarning is set when removing old plaintext copies failed after
	// encryption. It is retried on the next start.
	ScrubWarning error
}

// ErrKeyMismatch means the configured key cannot read this database.
var ErrKeyMismatch = errors.New("the secret key does not match this database")

// sealedColumns lists every encrypted column, how to find its rows, and the
// additional data each value is bound to.
var sealedColumns = []struct {
	table, col string
	keys       []string
	aad        func(k []string) string
}{
	{"apps", "spec", []string{"name"}, func(k []string) string { return "apps.spec:" + k[0] }},
	{"plans", "body", []string{"id"}, func(k []string) string { return "plans.body:" + k[0] }},
	{"deployments", "body", []string{"id"}, func(k []string) string { return "deployments.body:" + k[0] }},
	{"databases", "password", []string{"app", "name"}, func(k []string) string { return "databases.password:" + k[0] + "/" + k[1] }},
	{"webhooks", "secret", []string{"app"}, func(k []string) string { return "webhooks.secret:" + k[0] }},
	{"deploy_logs", "lines", []string{"deployment"}, func(k []string) string { return "deploy_logs.lines:" + k[0] }},
}

func (s *Store) seal(plain []byte, aad string) (string, error) { return s.kr.Load().Seal(plain, aad) }

// open decrypts a stored value. Plaintext (from before encryption, or
// written by an older binary) is accepted and gets encrypted on the next
// start; ciphertext that doesn't decrypt is an error, never returned as is.
func (s *Store) open(v, aad string) ([]byte, error) {
	if !secrets.Sealed(v) {
		return []byte(v), nil
	}
	b, err := s.kr.Load().Open(v, aad)
	if err != nil {
		return nil, apierr.New(500, "secrets_unreadable", "%v (%s)", err, aad)
	}
	return b, nil
}

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
CREATE TABLE IF NOT EXISTS webhooks (
  app TEXT PRIMARY KEY, secret TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS webhook_deliveries (
  id TEXT PRIMARY KEY, app TEXT NOT NULL, delivery TEXT NOT NULL DEFAULT '', body TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS deliveries_by_app ON webhook_deliveries(app, created_at);
CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sessions (
  hash TEXT PRIMARY KEY, token_name TEXT NOT NULL, scope TEXT NOT NULL, created_at TEXT NOT NULL, expires_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL,
  target TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', detail TEXT);
`

// Fixed-width UTC timestamps so text ordering equals time ordering.
const tsFormat = "2006-01-02T15:04:05.000000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsFormat) }

// Open opens the state database with the keyring that encrypts its
// secrets. It refuses a keyring that can't read the database, and encrypts
// any plaintext secrets left from before encryption existed.
func Open(path string, kr *secrets.Keyring) (*Store, error) {
	if kr == nil {
		return nil, errors.New("a secret keyring is required")
	}
	// secure_delete zeroes freed pages, so replaced plaintext doesn't linger
	// in the file.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=secure_delete(1)"
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
	st := &Store{db: db}
	st.kr.Store(kr)
	needMarker, err := st.checkKey()
	if err != nil {
		db.Close()
		return nil, err
	}
	// Encrypt leftover plaintext and, for a new database, the key check, in
	// one transaction: either both happen or neither.
	if _, err := st.reseal(context.Background(), false, needMarker); err != nil {
		db.Close()
		return nil, fmt.Errorf("encrypting stored secrets: %w", err)
	}
	// The data is encrypted now; a failed scrub only leaves old copies in
	// free pages. Warn, and try again on the next start.
	var pending string
	if db.QueryRow(`SELECT v FROM meta WHERE k = 'scrub_pending'`).Scan(&pending) == nil {
		if err := st.scrub(); err != nil {
			st.ScrubWarning = err
		} else {
			db.Exec(`DELETE FROM meta WHERE k = 'scrub_pending'`)
		}
	}
	return st, nil
}

const keyCheckAAD = "meta:key_check"

// checkKey verifies the keyring against the key-check value sealed when the
// database was first encrypted. It reports whether that value still needs
// creating, which it refuses if the database holds encrypted values: their
// key would be unknown.
func (s *Store) checkKey() (needMarker bool, err error) {
	kr := s.kr.Load()
	var v string
	err = s.db.QueryRow(`SELECT v FROM meta WHERE k = 'key_check'`).Scan(&v)
	if err == nil {
		if _, err := kr.Open(v, keyCheckAAD); err != nil {
			return false, fmt.Errorf("%w: this database was encrypted with key %s, but the configured keys are %s",
				ErrKeyMismatch, secrets.KeyID(v), strings.Join(kr.IDs(), ", "))
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	for _, c := range sealedColumns {
		var sample string
		e := s.db.QueryRow(`SELECT ` + c.col + ` FROM ` + c.table + ` WHERE substr(` + c.col + `, 1, 5) = 'css1:' LIMIT 1`).Scan(&sample)
		if e == nil {
			return false, fmt.Errorf("%w: the database holds values encrypted with key %s, but its key check is missing",
				ErrKeyMismatch, secrets.KeyID(sample))
		}
	}
	return true, nil
}

// reseal encrypts plaintext values (all=false) or re-encrypts every value
// with the current key (all=true), in one transaction.
func (s *Store) reseal(ctx context.Context, all, createMarker bool) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	kr := s.kr.Load()
	n := 0
	if createMarker {
		sealed, err := kr.Seal([]byte("coldship"), keyCheckAAD)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(k, v) VALUES('key_check', ?)`, sealed); err != nil {
			return 0, err
		}
	}
	for _, c := range sealedColumns {
		cols := strings.Join(c.keys, ", ")
		rows, err := tx.QueryContext(ctx, `SELECT `+cols+`, `+c.col+` FROM `+c.table+` WHERE `+c.col+` IS NOT NULL`)
		if err != nil {
			return 0, err
		}
		type row struct {
			keys []string
			val  string
		}
		var todo []row
		for rows.Next() {
			keys := make([]string, len(c.keys))
			dest := make([]any, 0, len(keys)+1)
			for i := range keys {
				dest = append(dest, &keys[i])
			}
			var val string
			if err := rows.Scan(append(dest, &val)...); err != nil {
				rows.Close()
				return 0, err
			}
			if all || !secrets.Sealed(val) {
				todo = append(todo, row{keys, val})
			}
		}
		rows.Close()
		where := make([]string, len(c.keys))
		for i, k := range c.keys {
			where[i] = k + " = ?"
		}
		for _, r := range todo {
			aad := c.aad(r.keys)
			plain := []byte(r.val)
			if secrets.Sealed(r.val) {
				if plain, err = kr.Open(r.val, aad); err != nil {
					return 0, fmt.Errorf("%s.%s: %w", c.table, c.col, err)
				}
			}
			sealed, err := kr.Seal(plain, aad)
			if err != nil {
				return 0, err
			}
			args := []any{sealed}
			for _, k := range r.keys {
				args = append(args, k)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE `+c.table+` SET `+c.col+` = ? WHERE `+strings.Join(where, " AND "), args...); err != nil {
				return 0, err
			}
			n++
		}
	}
	if all {
		var v string
		if err := tx.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = 'key_check'`).Scan(&v); err == nil {
			plain, err := kr.Open(v, "meta:key_check")
			if err != nil {
				return 0, err
			}
			sealed, err := kr.Seal(plain, "meta:key_check")
			if err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE meta SET v = ? WHERE k = 'key_check'`, sealed); err != nil {
				return 0, err
			}
		}
	}
	if n > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(k, v) VALUES('scrub_pending', '1') ON CONFLICT(k) DO NOTHING`); err != nil {
			return 0, err
		}
	}
	return n, tx.Commit()
}

// scrub removes old copies of rewritten values: it checkpoints and
// truncates the WAL and vacuums the database file.
func (s *Store) scrub() error {
	for _, stmt := range []string{`PRAGMA wal_checkpoint(TRUNCATE)`, `VACUUM`, `PRAGMA wal_checkpoint(TRUNCATE)`} {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("scrubbing old plaintext (%s): %w", stmt, err)
		}
	}
	return nil
}

// RotateKey switches to next (which must also hold the old key), then
// re-encrypts every secret with its current key and scrubs old copies.
func (s *Store) RotateKey(ctx context.Context, next *secrets.Keyring) (int, error) {
	s.mu.Lock()
	s.kr.Store(next)
	n, err := s.reseal(ctx, true, false)
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if err := s.scrub(); err != nil {
		s.ScrubWarning = err
	} else {
		s.db.ExecContext(ctx, `DELETE FROM meta WHERE k = 'scrub_pending'`)
	}
	return n, nil
}

// KeyIDsInUse counts stored values by the key that encrypted them. Before
// a key is dropped, it must not appear here.
func (s *Store) KeyIDsInUse(ctx context.Context) (map[string]int, error) {
	ids := map[string]int{}
	cols := append([]struct{ table, col string }{{"meta", "v"}}, []struct{ table, col string }{}...)
	for _, c := range sealedColumns {
		cols = append(cols, struct{ table, col string }{c.table, c.col})
	}
	for _, c := range cols {
		rows, err := s.db.QueryContext(ctx, `SELECT `+c.col+` FROM `+c.table+` WHERE substr(`+c.col+`, 1, 5) = 'css1:'`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return nil, err
			}
			ids[secrets.KeyID(v)]++
		}
		rows.Close()
	}
	return ids, nil
}

// DecryptAll opens the database at path and writes every secret back as
// plaintext (for downgrading to a version without encryption). The server
// must not be running.
func DecryptAll(ctx context.Context, path string, kr *secrets.Keyring) (int, error) {
	st, err := Open(path, kr)
	if err != nil {
		return 0, err
	}
	defer st.Close()
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, c := range sealedColumns {
		cols := strings.Join(c.keys, ", ")
		rows, err := tx.QueryContext(ctx, `SELECT `+cols+`, `+c.col+` FROM `+c.table+` WHERE substr(`+c.col+`, 1, 5) = 'css1:'`)
		if err != nil {
			return 0, err
		}
		type row struct {
			keys []string
			val  string
		}
		var todo []row
		for rows.Next() {
			keys := make([]string, len(c.keys))
			dest := make([]any, 0, len(keys)+1)
			for i := range keys {
				dest = append(dest, &keys[i])
			}
			var val string
			if err := rows.Scan(append(dest, &val)...); err != nil {
				rows.Close()
				return 0, err
			}
			todo = append(todo, row{keys, val})
		}
		rows.Close()
		where := make([]string, len(c.keys))
		for i, k := range c.keys {
			where[i] = k + " = ?"
		}
		for _, r := range todo {
			plain, err := kr.Open(r.val, c.aad(r.keys))
			if err != nil {
				return 0, err
			}
			args := []any{string(plain)}
			for _, k := range r.keys {
				args = append(args, k)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE `+c.table+` SET `+c.col+` = ? WHERE `+strings.Join(where, " AND "), args...); err != nil {
				return 0, err
			}
			n++
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE k = 'key_check'`); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// SetKeyring replaces the keyring (after rotation, with one holding only
// the new key).
func (s *Store) SetKeyring(kr *secrets.Keyring) { s.kr.Store(kr) }

// SecretsStatus describes encryption without revealing key material.
type SecretsStatus struct {
	KeyID     string         `json:"key_id"`
	Keys      int            `json:"keys"`
	Source    string         `json:"source"`
	Encrypted map[string]int `json:"encrypted"`
	Plaintext int            `json:"plaintext"`
}

func (s *Store) SecretsStatus(ctx context.Context) (*SecretsStatus, error) {
	kr := s.kr.Load()
	st := &SecretsStatus{KeyID: kr.CurrentID(), Keys: len(kr.IDs()), Source: kr.Source, Encrypted: map[string]int{}}
	for _, c := range sealedColumns {
		var enc, plain int
		if err := s.db.QueryRowContext(ctx, `SELECT
			coalesce(sum(CASE WHEN substr(`+c.col+`, 1, 5) = 'css1:' THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN substr(`+c.col+`, 1, 5) = 'css1:' THEN 0 ELSE 1 END), 0)
			FROM `+c.table+` WHERE `+c.col+` IS NOT NULL`).Scan(&enc, &plain); err != nil {
			return nil, err
		}
		st.Encrypted[c.table+"."+c.col] = enc
		st.Plaintext += plain
	}
	return st, nil
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
	plain := "cst_" + hex.EncodeToString(b)
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

// ListTokens lists tokens (never their values), oldest first.
func (s *Store) ListTokens(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, scope, created_at FROM tokens ORDER BY created_at, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		var t Token
		var scope, created string
		if err := rows.Scan(&t.Name, &scope, &created); err != nil {
			return nil, err
		}
		t.Scope = Scope(scope)
		t.CreatedAt, _ = time.Parse(tsFormat, created)
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteToken deletes a token and every dashboard session signed in with
// it. It reports whether the token existed.
func (s *Store) DeleteToken(ctx context.Context, name string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM tokens WHERE name = ?`, name)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE token_name = ?`, name); err != nil {
		return false, err
	}
	return n > 0, tx.Commit()
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
	// A session lives only as long as its token.
	err := s.db.QueryRowContext(ctx, `SELECT s.token_name, t.scope, s.expires_at FROM sessions s
		JOIN tokens t ON t.name = s.token_name WHERE s.hash = ?`, hashToken(plain)).
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
	pw, err := s.open(d.Password, "databases.password:"+app+"/"+name)
	if err != nil {
		return nil, err
	}
	d.Password = string(pw)
	d.CreatedAt, _ = time.Parse(tsFormat, created)
	return &d, nil
}

func (s *Store) SaveDatabase(ctx context.Context, d *Database) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pw, err := s.seal([]byte(d.Password), "databases.password:"+d.App+"/"+d.Name)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO databases(app, name, engine, version, password, created_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(app, name) DO UPDATE SET engine=excluded.engine, version=excluded.version`,
		d.App, d.Name, d.Engine, d.Version, pw, ts(d.CreatedAt))
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
	// Manual snapshots were asked for through the API. They are pruned
	// separately, so asking for many can't push out automatic ones.
	Manual bool `json:"manual,omitempty"`
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

// --- git webhooks ---

type Webhook struct {
	App       string
	Secret    string
	CreatedAt time.Time
}

func (s *Store) GetWebhook(ctx context.Context, app string) (*Webhook, error) {
	var w Webhook
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT app, secret, created_at FROM webhooks WHERE app = ?`, app).Scan(&w.App, &w.Secret, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	secret, err := s.open(w.Secret, "webhooks.secret:"+app)
	if err != nil {
		return nil, err
	}
	w.Secret = string(secret)
	w.CreatedAt, _ = time.Parse(tsFormat, created)
	return &w, nil
}

func (s *Store) SaveWebhook(ctx context.Context, w *Webhook) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	secret, err := s.seal([]byte(w.Secret), "webhooks.secret:"+w.App)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO webhooks(app, secret, created_at) VALUES(?,?,?)
		ON CONFLICT(app) DO UPDATE SET secret=excluded.secret, created_at=excluded.created_at`, w.App, secret, ts(w.CreatedAt))
	return err
}

func (s *Store) DeleteWebhook(ctx context.Context, app string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM webhooks WHERE app = ?`, app)
	return err
}

// Delivery is one received webhook request and what Coldship did with it.
type Delivery struct {
	ID        string    `json:"id"`
	App       string    `json:"app"`
	Delivery  string    `json:"delivery,omitempty"` // provider's delivery id
	Provider  string    `json:"provider"`
	Event     string    `json:"event"`
	Ref       string    `json:"ref,omitempty"`
	Commit    string    `json:"commit,omitempty"`
	Pusher    string    `json:"pusher,omitempty"`
	Message   string    `json:"message,omitempty"` // commit message: untrusted
	Result    string    `json:"result"`            // ignored | planned | deploying | deployed | preview | preview_deleted | failed | queued
	Detail    string    `json:"detail,omitempty"`
	Plan      string    `json:"plan,omitempty"`
	Preview   string    `json:"preview,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) SaveDelivery(ctx context.Context, d *Delivery) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO webhook_deliveries(id, app, delivery, body, created_at) VALUES(?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET body=excluded.body`, d.ID, d.App, d.Delivery, string(b), ts(d.CreatedAt))
	return err
}

// SeenDelivery reports whether a provider delivery id was already received
// for app (providers redeliver on timeouts).
func (s *Store) SeenDelivery(ctx context.Context, app, delivery string) (bool, error) {
	if delivery == "" {
		return false, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM webhook_deliveries WHERE app = ? AND delivery = ?`, app, delivery).Scan(&n)
	return n > 0, err
}

// ListDeliveries returns an app's newest deliveries and prunes old ones.
func (s *Store) ListDeliveries(ctx context.Context, app string, limit int) ([]Delivery, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM webhook_deliveries WHERE app = ? ORDER BY created_at DESC, rowid DESC LIMIT ?`, app, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var d Delivery
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PruneDeliveries keeps the newest keep deliveries of an app.
func (s *Store) PruneDeliveries(ctx context.Context, app string, keep int) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM webhook_deliveries WHERE app = ? AND id NOT IN (
		SELECT id FROM webhook_deliveries WHERE app = ? ORDER BY created_at DESC, rowid DESC LIMIT ?)`, app, app, keep)
	return err
}

// --- deployment logs (build output, startup, health checks) ---

func (s *Store) SaveDeployLog(ctx context.Context, deployment string, lines []string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := json.Marshal(lines)
	if err != nil {
		return err
	}
	sealed, err := s.seal(b, "deploy_logs.lines:"+deployment)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO deploy_logs(deployment, lines) VALUES(?,?)
		ON CONFLICT(deployment) DO UPDATE SET lines=excluded.lines`, deployment, sealed)
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
	plain, err := s.open(body, "deploy_logs.lines:"+deployment)
	if err != nil {
		return nil, err
	}
	var lines []string
	return lines, json.Unmarshal(plain, &lines)
}

// --- apps ---

func (s *Store) GetApp(ctx context.Context, name string) (*App, error) {
	row := s.db.QueryRowContext(ctx, `SELECT name, spec, current_deployment, preview_of, expires_at, created_at, updated_at FROM apps WHERE name = ?`, name)
	a, err := s.scanApp(row)
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
		a, err := s.scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(...any) error }

func (s *Store) scanApp(r scanner) (*App, error) {
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
		plain, err := s.open(specJSON.String, "apps.spec:"+a.Name)
		if err != nil {
			return nil, err
		}
		a.Spec = &spec.App{}
		if err := json.Unmarshal(plain, a.Spec); err != nil {
			return nil, err
		}
	}
	a.CreatedAt, _ = time.Parse(tsFormat, created)
	a.UpdatedAt, _ = time.Parse(tsFormat, updated)
	return &a, nil
}

func (s *Store) UpsertApp(ctx context.Context, a *App) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var specJSON sql.NullString
	if a.Spec != nil {
		b, err := json.Marshal(a.Spec)
		if err != nil {
			return err
		}
		sealed, err := s.seal(b, "apps.spec:"+a.Name)
		if err != nil {
			return err
		}
		specJSON = sql.NullString{String: sealed, Valid: true}
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
	q, args := `SELECT id, body FROM plans`, []any{}
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
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, err
		}
		plain, err := s.open(body, "plans.body:"+id)
		if err != nil {
			return nil, err
		}
		var p Plan
		if err := json.Unmarshal(plain, &p); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, body FROM deployments WHERE app = ? ORDER BY created_at DESC, rowid DESC LIMIT ?`, app, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Deployment{}
	for rows.Next() {
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, err
		}
		plain, err := s.open(body, "deployments.body:"+id)
		if err != nil {
			return nil, err
		}
		var d Deployment
		if err := json.Unmarshal(plain, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) saveDoc(ctx context.Context, table, id, app, status string, created time.Time, v any) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	sealed, err := s.seal(b, table+".body:"+id)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO `+table+`(id, app, status, body, created_at) VALUES(?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status, body=excluded.body`, id, app, status, sealed, ts(created))
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
	plain, err := s.open(body, table+".body:"+id)
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(plain, v)
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
