package store

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/dunkbing/coldship/internal/apierr"
)

// DefaultOrg holds everything from before organizations existed. Its admins
// run the server itself (upgrades, secrets, server status).
const DefaultOrg = "default"

// OrgOr returns org, or the default org for "" (records from before orgs).
func OrgOr(org string) string {
	if org == "" {
		return DefaultOrg
	}
	return org
}

type orgKey struct{}

// WithOrg records the org a request acts in.
func WithOrg(ctx context.Context, org string) context.Context {
	return context.WithValue(ctx, orgKey{}, org)
}

// OrgFrom returns the org recorded with WithOrg, or "".
func OrgFrom(ctx context.Context) string {
	o, _ := ctx.Value(orgKey{}).(string)
	return o
}

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Org struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	// Role is the caller's role in the org, when listing a user's orgs.
	Role Scope `json:"role,omitempty"`
}

type Member struct {
	User
	Role     Scope     `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
}

// Link kinds: an invitation to an org, or a password reset.
const (
	LinkInvite = "invite"
	LinkReset  = "reset"
)

// Link is a single-use invite or password-reset link. Only its hash is
// stored.
type Link struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Org       string    `json:"org,omitempty"`
	Role      Scope     `json:"role,omitempty"`
	UserID    string    `json:"user_id,omitempty"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// --- passwords ---

// argon2id with OWASP's minimum settings: 19 MiB, 2 passes, 1 thread.
const (
	argonTime   = 2
	argonMemory = 19 * 1024
	argonKeyLen = 32
)

func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, 1, argonKeyLen)
	b64 := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("argon2id$v=19$m=%d,t=%d,p=1$%s$%s", argonMemory, argonTime, b64(salt), b64(key)), nil
}

func checkPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[3])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[4])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is checked against for unknown emails, so a sign-in takes as
// long whether or not the account exists.
var dummyHash, _ = hashPassword("coldship-dummy-password")

// --- users ---

func (s *Store) CreateUser(ctx context.Context, email, name, password string) (*User, error) {
	h, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	u := &User{ID: NewID("usr"), Email: strings.TrimSpace(email), Name: strings.TrimSpace(name), CreatedAt: time.Now()}
	_, err = s.db.ExecContext(ctx, `INSERT INTO users(id, email, name, password, created_at) VALUES(?,?,?,?,?)`,
		u.ID, u.Email, u.Name, h, ts(u.CreatedAt))
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return nil, apierr.New(409, "email_taken", "an account with %s already exists", u.Email).
			WithField("email").WithHint("sign in with that account instead")
	}
	return u, err
}

// Authenticate returns the user with this email and password, or nil.
func (s *Store) Authenticate(ctx context.Context, email, password string) (*User, error) {
	var u User
	var h, created string
	err := s.db.QueryRowContext(ctx, `SELECT id, email, name, password, created_at FROM users WHERE email = ?`, strings.TrimSpace(email)).
		Scan(&u.ID, &u.Email, &u.Name, &h, &created)
	if errors.Is(err, sql.ErrNoRows) {
		checkPassword(dummyHash, password)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !checkPassword(h, password) {
		return nil, nil
	}
	u.CreatedAt, _ = time.Parse(tsFormat, created)
	return &u, nil
}

// EmailTaken reports whether an account uses email.
func (s *Store) EmailTaken(ctx context.Context, email string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE email = ?`, strings.TrimSpace(email)).Scan(&n)
	return n > 0, err
}

func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	var u User
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id, email, name, created_at FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Email, &u.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	u.CreatedAt, _ = time.Parse(tsFormat, created)
	return &u, err
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// SetPassword changes a user's password and signs out their sessions,
// except keepSession (a session id, or "").
func (s *Store) SetPassword(ctx context.Context, userID, password, keepSession string) error {
	h, err := hashPassword(password)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET password = ? WHERE id = ?`, h, userID); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND hash != ?`, userID, hashToken(keepSession))
	return err
}

func (s *Store) RenameUser(ctx context.Context, userID, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET name = ? WHERE id = ?`, strings.TrimSpace(name), userID)
	return err
}

// --- orgs ---

func (s *Store) CreateOrg(ctx context.Context, id, name string) (*Org, error) {
	o := &Org{ID: id, Name: strings.TrimSpace(name), CreatedAt: time.Now()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO orgs(id, name, created_at) VALUES(?,?,?)`, o.ID, o.Name, ts(o.CreatedAt))
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return nil, apierr.New(409, "org_exists", "an organization with id %s already exists", id)
	}
	return o, err
}

func (s *Store) GetOrg(ctx context.Context, id string) (*Org, error) {
	var o Org
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id, name, created_at FROM orgs WHERE id = ?`, id).Scan(&o.ID, &o.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	o.CreatedAt, _ = time.Parse(tsFormat, created)
	return &o, err
}

// UserOrgs lists the orgs a user belongs to, with their role, by name.
func (s *Store) UserOrgs(ctx context.Context, userID string) ([]Org, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT o.id, o.name, o.created_at, m.role FROM orgs o JOIN members m ON m.org = o.id
		WHERE m.user_id = ? ORDER BY o.id != ?, o.name`, userID, DefaultOrg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Org{}
	for rows.Next() {
		var o Org
		var created, role string
		if err := rows.Scan(&o.ID, &o.Name, &created, &role); err != nil {
			return nil, err
		}
		o.CreatedAt, _ = time.Parse(tsFormat, created)
		o.Role = Scope(role)
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) RenameOrg(ctx context.Context, id, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE orgs SET name = ? WHERE id = ?`, strings.TrimSpace(name), id)
	return err
}

// OrgCounts counts what an org holds.
type OrgCounts struct {
	Apps    int `json:"apps"`
	Data    int `json:"data"`
	Members int `json:"members"`
	Tokens  int `json:"tokens"`
}

func (s *Store) CountOrg(ctx context.Context, org string) (OrgCounts, error) {
	var c OrgCounts
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM apps WHERE org = ?1),
		(SELECT count(*) FROM databases WHERE app = '' AND org = ?1),
		(SELECT count(*) FROM members WHERE org = ?1),
		(SELECT count(*) FROM tokens WHERE org = ?1)`, org).Scan(&c.Apps, &c.Data, &c.Members, &c.Tokens)
	return c, err
}

// DeleteOrg deletes an org with no apps or data services left, with its
// members, tokens, links, sessions and integrations.
func (s *Store) DeleteOrg(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM sessions WHERE org = ?1`,
		`DELETE FROM members WHERE org = ?1`,
		`DELETE FROM tokens WHERE org = ?1`,
		`DELETE FROM links WHERE org = ?1`,
		`DELETE FROM integrations WHERE name IN ('github:' || ?1, 'backup:' || ?1)`,
		`DELETE FROM agents WHERE org = ?1`,
		`DELETE FROM channels WHERE org = ?1`,
		`DELETE FROM orgs WHERE id = ?1`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// --- members ---

func (s *Store) AddMember(ctx context.Context, org, userID string, role Scope) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO members(org, user_id, role, created_at) VALUES(?,?,?,?)
		ON CONFLICT(org, user_id) DO NOTHING`, org, userID, string(role), ts(time.Now()))
	return err
}

// MemberRole returns a user's role in org, or "" if not a member.
func (s *Store) MemberRole(ctx context.Context, org, userID string) (Scope, error) {
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT role FROM members WHERE org = ? AND user_id = ?`, org, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return Scope(role), err
}

func (s *Store) ListMembers(ctx context.Context, org string) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.id, u.email, u.name, u.created_at, m.role, m.created_at FROM members m
		JOIN users u ON u.id = m.user_id WHERE m.org = ? ORDER BY m.created_at, u.email`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		var created, role, joined string
		if err := rows.Scan(&m.ID, &m.Email, &m.Name, &created, &role, &joined); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse(tsFormat, created)
		m.JoinedAt, _ = time.Parse(tsFormat, joined)
		m.Role = Scope(role)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) SetMemberRole(ctx context.Context, org, userID string, role Scope) error {
	_, err := s.db.ExecContext(ctx, `UPDATE members SET role = ? WHERE org = ? AND user_id = ?`, string(role), org, userID)
	return err
}

// RemoveMember removes a user from an org and ends their sessions in it.
func (s *Store) RemoveMember(ctx context.Context, org, userID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM members WHERE org = ? AND user_id = ?`, org, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE org = ? AND user_id = ?`, org, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// CountAdmins counts an org's admins: admin members and admin tokens.
func (s *Store) CountAdmins(ctx context.Context, org string) (members, tokens int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM members WHERE org = ?1 AND role = 'admin'),
		(SELECT count(*) FROM tokens WHERE org = ?1 AND scope = 'admin')`, org).Scan(&members, &tokens)
	return
}

// --- user sessions ---

// CreateUserSession signs a user in to org and returns the session id once.
func (s *Store) CreateUserSession(ctx context.Context, userID, org string, ttl time.Duration) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	plain := hex.EncodeToString(b)
	now := time.Now()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, ts(now)); err != nil {
		return "", err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(hash, token_name, scope, user_id, org, created_at, expires_at) VALUES(?,'','',?,?,?,?)`,
		hashToken(plain), userID, org, ts(now), ts(now.Add(ttl)))
	return plain, err
}

// SetSessionOrg switches a user session to another org.
func (s *Store) SetSessionOrg(ctx context.Context, plain, org string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET org = ? WHERE hash = ? AND user_id != ''`, org, hashToken(plain))
	return err
}

// --- links ---

// CreateLink stores a single-use link and returns its secret once.
func (s *Store) CreateLink(ctx context.Context, l *Link, ttl time.Duration) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	plain := hex.EncodeToString(b)
	l.ID, l.CreatedAt = NewID(l.Kind), time.Now()
	l.ExpiresAt = l.CreatedAt.Add(ttl)
	_, err := s.db.ExecContext(ctx, `INSERT INTO links(id, hash, kind, org, role, user_id, created_by, created_at, expires_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		l.ID, hashToken(plain), l.Kind, l.Org, string(l.Role), l.UserID, l.CreatedBy, ts(l.CreatedAt), ts(l.ExpiresAt))
	return plain, err
}

const linkCols = `id, kind, org, role, user_id, created_by, created_at, expires_at`

func scanLink(r scanner) (*Link, error) {
	var l Link
	var role, created, expires string
	if err := r.Scan(&l.ID, &l.Kind, &l.Org, &role, &l.UserID, &l.CreatedBy, &created, &expires); err != nil {
		return nil, err
	}
	l.Role = Scope(role)
	l.CreatedAt, _ = time.Parse(tsFormat, created)
	l.ExpiresAt, _ = time.Parse(tsFormat, expires)
	return &l, nil
}

// LookupLink returns an unused, unexpired link, or nil.
func (s *Store) LookupLink(ctx context.Context, plain string) (*Link, error) {
	l, err := scanLink(s.db.QueryRowContext(ctx, `SELECT `+linkCols+` FROM links WHERE hash = ? AND used_at = '' AND expires_at > ?`,
		hashToken(plain), ts(time.Now())))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return l, err
}

// UseLink marks a link used. It reports false if it was already used or
// has expired, so a link works once even when used twice at the same time.
func (s *Store) UseLink(ctx context.Context, plain string) (bool, error) {
	now := ts(time.Now())
	res, err := s.db.ExecContext(ctx, `UPDATE links SET used_at = ? WHERE hash = ? AND used_at = '' AND expires_at > ?`, now, hashToken(plain), now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ListLinks lists an org's pending links of a kind, newest first.
func (s *Store) ListLinks(ctx context.Context, org, kind string) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+linkCols+` FROM links WHERE org = ? AND kind = ? AND used_at = '' AND expires_at > ?
		ORDER BY created_at DESC`, org, kind, ts(time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// DeleteLink revokes a link of org; it reports whether one was deleted.
func (s *Store) DeleteLink(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM links WHERE org = ? AND id = ?`, org, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// --- moving apps ---

// MoveApp moves an app and its previews to another org.
func (s *Store) MoveApp(ctx context.Context, name, org string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE apps SET org = ? WHERE name = ? OR preview_of = ?`, org, name, name)
	return err
}
