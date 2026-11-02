package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/store"
)

// Organizations group apps, data services, tokens and people. A person
// signs in with email and password and acts in one org at a time; tokens
// belong to one org. Invites are links an admin copies and shares: there
// is no email server.

const (
	userSessionTTL = 7 * 24 * time.Hour
	inviteTTL      = 7 * 24 * time.Hour
	resetTTL       = 24 * time.Hour
)

func (s *Server) routeOrgs() {
	s.mux.HandleFunc("GET /v1/auth", s.authInfo)
	s.mux.HandleFunc("POST /v1/links/lookup", s.lookupLink)
	s.mux.HandleFunc("POST /v1/links/accept", s.acceptLink)
	s.route("POST /v1/account", store.ScopeAdmin, s.createAccount)
	s.route("PATCH /v1/account", store.ScopeRead, s.updateAccount)
	s.route("POST /v1/account/password", store.ScopeRead, s.changePassword)
	s.route("GET /v1/orgs", store.ScopeRead, s.listOrgs)
	s.route("POST /v1/orgs", store.ScopeRead, s.createOrg)
	s.mux.HandleFunc("POST /v1/session/org", s.switchOrg)
	s.route("GET /v1/org", store.ScopeRead, s.getOrg)
	s.route("PATCH /v1/org", store.ScopeAdmin, s.renameOrg)
	s.mux.HandleFunc("DELETE /v1/org", s.deleteOrg)
	s.route("GET /v1/org/members", store.ScopeRead, s.listMembers)
	s.route("PATCH /v1/org/members/{id}", store.ScopeAdmin, s.setMemberRole)
	s.route("DELETE /v1/org/members/{id}", store.ScopeRead, s.removeMember)
	s.route("POST /v1/org/members/{id}/reset", store.ScopeAdmin, serverOnly(s.resetLink))
	s.route("GET /v1/org/invites", store.ScopeAdmin, s.listInvites)
	s.route("POST /v1/org/invites", store.ScopeAdmin, s.createInvite)
	s.route("DELETE /v1/org/invites/{id}", store.ScopeAdmin, s.revokeInvite)
	s.route("POST /v1/apps/{name}/move", store.ScopeAdmin, s.moveApp)
}

// serverOnly limits h to admins of the default org, who run the server.
func serverOnly(h handler) handler {
	return func(r *http.Request, p principal) (any, error) {
		if !p.serverAdmin() {
			return nil, apierr.New(403, "server_only", "only admins of the default organization can manage the server")
		}
		return h(r, p)
	}
}

func (s *Server) record(ctx context.Context, p principal, action, target string, detail any) {
	_ = s.st.AddAudit(context.WithoutCancel(ctx), store.AuditEntry{Actor: p.name, Action: action, Target: target, Org: p.org, Detail: jsonOf(detail)})
}

func jsonOf(v any) []byte {
	if v == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return b
}

// --- validation ---

func checkEmail(email string) (string, error) {
	email = strings.TrimSpace(email)
	a, err := mail.ParseAddress(email)
	if err != nil || a.Address != email || len(email) > 254 {
		return "", apierr.Invalid("email", "%q isn't an email address", email)
	}
	return email, nil
}

func checkPassword(pw string) error {
	if len(pw) < 8 {
		return apierr.Invalid("password", "use a password of at least 8 characters")
	}
	if len(pw) > 256 {
		return apierr.Invalid("password", "use a password of at most 256 characters")
	}
	return nil
}

func checkName(field, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return "", apierr.Invalid(field, "give a %s of 1 to 80 characters", field)
	}
	return name, nil
}

func checkRole(role string) (store.Scope, error) {
	r, err := store.ParseScope(role)
	if err != nil {
		return "", apierr.Invalid("role", "role %q must be admin, deploy or read", role)
	}
	return r, nil
}

func needUser(p principal) error {
	if p.userID == "" {
		return apierr.New(403, "user_required", "this needs a person signed in with email and password, not a token")
	}
	return nil
}

// --- sign-in rate limit ---

// loginLimiter slows password guessing: after 5 failures for an email or
// an address in 15 minutes, sign-in is refused for the rest of the window.
type loginLimiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

const (
	loginWindow   = 15 * time.Minute
	loginMaxFails = 5
)

func (l *loginLimiter) recent(key string, now time.Time) []time.Time {
	var keep []time.Time
	for _, t := range l.fails[key] {
		if now.Sub(t) < loginWindow {
			keep = append(keep, t)
		}
	}
	return keep
}

func (l *loginLimiter) blocked(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, k := range keys {
		if len(l.recent(k, now)) >= loginMaxFails {
			return true
		}
	}
	return false
}

func (l *loginLimiter) fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fails == nil {
		l.fails = map[string][]time.Time{}
	}
	now := time.Now()
	if len(l.fails) > 10000 {
		l.fails = map[string][]time.Time{} // bound memory under a flood
	}
	for _, k := range keys {
		l.fails[k] = append(l.recent(k, now), now)
	}
}

func (l *loginLimiter) clear(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}

func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

func (s *Server) passwordSignIn(w http.ResponseWriter, r *http.Request, req SessionRequest) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	keys := []string{"email:" + email, "ip:" + clientIP(r)}
	if s.logins.blocked(keys...) {
		writeErr(w, s.log, apierr.New(429, "too_many_attempts", "too many failed sign-ins; wait 15 minutes and try again"))
		return
	}
	u, err := s.st.Authenticate(r.Context(), email, req.Password)
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	if u == nil {
		s.logins.fail(keys...)
		writeErr(w, s.log, apierr.New(401, "unauthenticated", "wrong email or password"))
		return
	}
	s.logins.clear("email:" + email)
	orgs, err := s.st.UserOrgs(r.Context(), u.ID)
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	if len(orgs) == 0 {
		writeErr(w, s.log, apierr.New(403, "no_organization", "your account isn't in any organization any more").
			WithHint("ask an admin for an invite link"))
		return
	}
	s.startUserSession(w, r, u, orgs[0].ID)
}

// startUserSession signs u in to org, sets the cookie and replies with who
// they are.
func (s *Server) startUserSession(w http.ResponseWriter, r *http.Request, u *store.User, org string) {
	id, err := s.st.CreateUserSession(r.Context(), u.ID, org, userSessionTTL)
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	role, err := s.st.MemberRole(r.Context(), org, u.ID)
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	p := principal{name: u.Email, scope: role, org: org, userID: u.ID}
	s.record(r.Context(), p, "session.create", u.Email, nil)
	s.setSession(w, r, id, userSessionTTL)
	who, err := s.who(r.Context(), p)
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, who)
}

// authInfo tells the sign-in page whether any accounts exist yet.
func (s *Server) authInfo(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.CountUsers(r.Context())
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"has_users": n > 0})
}

// --- account ---

type AccountRequest struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// createAccount turns an admin token's dashboard session into a person's
// account, an admin of the token's org. It's how the first account is
// made: the installer only creates a token.
func (s *Server) createAccount(r *http.Request, p principal) (any, error) {
	if p.userID != "" {
		return nil, apierr.New(409, "already_user", "you're already signed in as %s", p.name)
	}
	var req AccountRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	email, err := checkEmail(req.Email)
	if err != nil {
		return nil, err
	}
	name, err := checkName("name", req.Name)
	if err != nil {
		return nil, err
	}
	if err := checkPassword(req.Password); err != nil {
		return nil, err
	}
	u, err := s.st.CreateUser(r.Context(), email, name, req.Password)
	if err != nil {
		return nil, err
	}
	if err := s.st.AddMember(r.Context(), p.org, u.ID, store.ScopeAdmin); err != nil {
		return nil, err
	}
	s.record(r.Context(), p, "member.add", email, map[string]any{"role": store.ScopeAdmin})
	return u, nil
}

type UpdateAccountRequest struct {
	Name string `json:"name"`
}

func (s *Server) updateAccount(r *http.Request, p principal) (any, error) {
	if err := needUser(p); err != nil {
		return nil, err
	}
	var req UpdateAccountRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	name, err := checkName("name", req.Name)
	if err != nil {
		return nil, err
	}
	if err := s.st.RenameUser(r.Context(), p.userID, name); err != nil {
		return nil, err
	}
	return s.st.GetUser(r.Context(), p.userID)
}

type PasswordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// changePassword sets a new password and signs out the user's other
// sessions.
func (s *Server) changePassword(r *http.Request, p principal) (any, error) {
	if err := needUser(p); err != nil {
		return nil, err
	}
	var req PasswordRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := checkPassword(req.New); err != nil {
		return nil, err
	}
	u, err := s.st.Authenticate(r.Context(), p.name, req.Current)
	if err != nil {
		return nil, err
	}
	if u == nil || u.ID != p.userID {
		return nil, apierr.Invalid("current", "the current password is wrong")
	}
	keep := ""
	if c, err := r.Cookie(SessionCookie); err == nil {
		keep = c.Value
	}
	if err := s.st.SetPassword(r.Context(), p.userID, req.New, keep); err != nil {
		return nil, err
	}
	s.record(r.Context(), p, "account.password", p.name, nil)
	return map[string]bool{"changed": true}, nil
}

// --- orgs ---

func (s *Server) listOrgs(r *http.Request, p principal) (any, error) {
	if p.userID == "" {
		o, err := s.st.GetOrg(r.Context(), p.org)
		if err != nil || o == nil {
			return []store.Org{}, err
		}
		o.Role = p.scope
		return []store.Org{*o}, nil
	}
	return s.st.UserOrgs(r.Context(), p.userID)
}

type OrgRequest struct {
	Name string `json:"name"`
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(name string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 30 {
		s = strings.TrimRight(s[:30], "-")
	}
	if s == "" {
		s = "org"
	}
	return s
}

// createOrg creates an org with the caller as its admin, and switches to
// it. Admins of the default org (who run the server) may create orgs.
func (s *Server) createOrg(r *http.Request, p principal) (any, error) {
	if err := needUser(p); err != nil {
		return nil, err
	}
	if role, err := s.st.MemberRole(r.Context(), store.DefaultOrg, p.userID); err != nil {
		return nil, err
	} else if role != store.ScopeAdmin {
		return nil, apierr.New(403, "server_only", "only admins of the default organization can create organizations")
	}
	var req OrgRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	name, err := checkName("name", req.Name)
	if err != nil {
		return nil, err
	}
	base := slugify(name)
	var o *store.Org
	for i := 1; i <= 50; i++ {
		id := base
		if i > 1 {
			id = base + "-" + itoa(i)
		}
		if o, err = s.st.CreateOrg(r.Context(), id, name); err == nil {
			break
		}
		if ae, ok := apierr.As(err); !ok || ae.Code != "org_exists" {
			return nil, err
		}
	}
	if o == nil {
		return nil, apierr.New(409, "org_exists", "pick another name")
	}
	if err := s.st.AddMember(r.Context(), o.ID, p.userID, store.ScopeAdmin); err != nil {
		return nil, err
	}
	s.record(r.Context(), principal{name: p.name, org: o.ID}, "org.create", o.ID, map[string]any{"name": name})
	if c, err := r.Cookie(SessionCookie); err == nil {
		if err := s.st.SetSessionOrg(r.Context(), c.Value, o.ID); err != nil {
			return nil, err
		}
	}
	o.Role = store.ScopeAdmin
	return o, nil
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return digits[i : i+1]
	}
	return itoa(i/10) + digits[i%10:i%10+1]
}

type SwitchOrgRequest struct {
	Org string `json:"org"`
}

// switchOrg moves the signed-in user's session to another of their orgs.
func (s *Server) switchOrg(w http.ResponseWriter, r *http.Request) {
	p, err := s.auth(r)
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	c, cerr := r.Cookie(SessionCookie)
	if p.userID == "" || cerr != nil {
		writeErr(w, s.log, needUser(principal{}))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	var req SwitchOrgRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, s.log, err)
		return
	}
	role, err := s.st.MemberRole(r.Context(), req.Org, p.userID)
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	if role == "" {
		writeErr(w, s.log, apierr.NotFound("organization", req.Org))
		return
	}
	if err := s.st.SetSessionOrg(r.Context(), c.Value, req.Org); err != nil {
		writeErr(w, s.log, err)
		return
	}
	who, err := s.who(r.Context(), principal{name: p.name, scope: role, org: req.Org, userID: p.userID})
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, who)
}

type OrgView struct {
	store.Org
	Counts store.OrgCounts `json:"counts"`
}

func (s *Server) getOrg(r *http.Request, p principal) (any, error) {
	o, err := s.st.GetOrg(r.Context(), p.org)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, apierr.NotFound("organization", p.org)
	}
	o.Role = p.scope
	c, err := s.st.CountOrg(r.Context(), p.org)
	return OrgView{Org: *o, Counts: c}, err
}

func (s *Server) renameOrg(r *http.Request, p principal) (any, error) {
	var req OrgRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	name, err := checkName("name", req.Name)
	if err != nil {
		return nil, err
	}
	if err := s.st.RenameOrg(r.Context(), p.org, name); err != nil {
		return nil, err
	}
	s.record(r.Context(), p, "org.rename", p.org, map[string]any{"name": name})
	return s.getOrg(r, p)
}

// deleteOrg deletes the caller's org once it has no apps or data services.
// The caller's session moves to another of their orgs first.
func (s *Server) deleteOrg(w http.ResponseWriter, r *http.Request) {
	p, err := s.auth(r)
	if err != nil {
		writeErr(w, s.log, err)
		return
	}
	ctx := r.Context()
	fail := func(err error) { writeErr(w, s.log, err) }
	if err := needUser(p); err != nil {
		fail(err)
		return
	}
	if p.scope != store.ScopeAdmin {
		fail(apierr.New(403, "insufficient_scope", "only the organization's admins can delete it"))
		return
	}
	if p.org == store.DefaultOrg {
		fail(apierr.New(409, "default_org", "the default organization can't be deleted"))
		return
	}
	c, err := s.st.CountOrg(ctx, p.org)
	if err != nil {
		fail(err)
		return
	}
	if c.Apps > 0 || c.Data > 0 {
		fail(apierr.New(409, "org_not_empty", "the organization still has %d apps and %d data services; delete or move them first", c.Apps, c.Data))
		return
	}
	orgs, err := s.st.UserOrgs(ctx, p.userID)
	if err != nil {
		fail(err)
		return
	}
	next := ""
	for _, o := range orgs {
		if o.ID != p.org {
			next = o.ID
			break
		}
	}
	if ck, err := r.Cookie(SessionCookie); err == nil && next != "" {
		if err := s.st.SetSessionOrg(ctx, ck.Value, next); err != nil {
			fail(err)
			return
		}
	}
	if err := s.st.DeleteOrg(ctx, p.org); err != nil {
		fail(err)
		return
	}
	s.record(ctx, principal{name: p.name, org: store.DefaultOrg}, "org.delete", p.org, nil)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": p.org, "switched_to": next})
}

// --- members ---

func (s *Server) listMembers(r *http.Request, p principal) (any, error) {
	return s.st.ListMembers(r.Context(), p.org)
}

type RoleRequest struct {
	Role string `json:"role"`
}

func (s *Server) member(ctx context.Context, org, id string) (*store.Member, error) {
	ms, err := s.st.ListMembers(ctx, org)
	if err != nil {
		return nil, err
	}
	for i := range ms {
		if ms[i].ID == id {
			return &ms[i], nil
		}
	}
	return nil, apierr.NotFound("member", id)
}

// lastAdmin refuses to take away the org's only admin.
func (s *Server) lastAdmin(ctx context.Context, org string, m *store.Member) error {
	if m.Role != store.ScopeAdmin {
		return nil
	}
	members, tokens, err := s.st.CountAdmins(ctx, org)
	if err != nil {
		return err
	}
	if members+tokens <= 1 {
		return apierr.New(409, "last_admin", "%s is the organization's only admin; make someone else an admin first", m.Email)
	}
	return nil
}

func (s *Server) setMemberRole(r *http.Request, p principal) (any, error) {
	var req RoleRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	role, err := checkRole(req.Role)
	if err != nil {
		return nil, err
	}
	m, err := s.member(r.Context(), p.org, r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	if role != store.ScopeAdmin {
		if err := s.lastAdmin(r.Context(), p.org, m); err != nil {
			return nil, err
		}
	}
	if err := s.st.SetMemberRole(r.Context(), p.org, m.ID, role); err != nil {
		return nil, err
	}
	s.record(r.Context(), p, "member.role", m.Email, map[string]any{"from": m.Role, "to": role})
	m.Role = role
	return m, nil
}

// removeMember removes someone from the org: admins remove anyone, anyone
// may leave.
func (s *Server) removeMember(r *http.Request, p principal) (any, error) {
	id := r.PathValue("id")
	if id != p.userID && p.scope != store.ScopeAdmin {
		return nil, apierr.New(403, "insufficient_scope", "only admins can remove other members")
	}
	m, err := s.member(r.Context(), p.org, id)
	if err != nil {
		return nil, err
	}
	if err := s.lastAdmin(r.Context(), p.org, m); err != nil {
		return nil, err
	}
	if err := s.st.RemoveMember(r.Context(), p.org, m.ID); err != nil {
		return nil, err
	}
	s.record(r.Context(), p, "member.remove", m.Email, nil)
	return map[string]any{"removed": m.Email}, nil
}

// --- links ---

type LinkView struct {
	store.Link
	URL string `json:"url,omitempty"`
}

func (s *Server) baseURL(r *http.Request) string {
	if s.publicURL != "" {
		return s.publicURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// The secret goes after #, which browsers never send to a server, so it
// stays out of access logs and Referer headers.
func (s *Server) linkURL(r *http.Request, kind, secret string) string {
	return s.baseURL(r) + "/" + kind + "#" + secret
}

func (s *Server) listInvites(r *http.Request, p principal) (any, error) {
	links, err := s.st.ListLinks(r.Context(), p.org, store.LinkInvite)
	if err != nil {
		return nil, err
	}
	out := make([]LinkView, len(links))
	for i, l := range links {
		out[i] = LinkView{Link: l}
	}
	return out, nil
}

func (s *Server) createInvite(r *http.Request, p principal) (any, error) {
	var req RoleRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	role, err := checkRole(req.Role)
	if err != nil {
		return nil, err
	}
	l := &store.Link{Kind: store.LinkInvite, Org: p.org, Role: role, CreatedBy: p.name}
	secret, err := s.st.CreateLink(r.Context(), l, inviteTTL)
	if err != nil {
		return nil, err
	}
	s.record(r.Context(), p, "invite.create", l.ID, map[string]any{"role": role})
	return LinkView{Link: *l, URL: s.linkURL(r, "invite", secret)}, nil
}

func (s *Server) revokeInvite(r *http.Request, p principal) (any, error) {
	id := r.PathValue("id")
	ok, err := s.st.DeleteLink(r.Context(), p.org, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apierr.NotFound("invite", id)
	}
	s.record(r.Context(), p, "invite.revoke", id, nil)
	return map[string]any{"revoked": id}, nil
}

// resetLink makes a password-reset link for a member. Only server admins
// can: the link takes over the account, in every org it belongs to.
func (s *Server) resetLink(r *http.Request, p principal) (any, error) {
	m, err := s.member(r.Context(), p.org, r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	l := &store.Link{Kind: store.LinkReset, UserID: m.ID, CreatedBy: p.name}
	secret, err := s.st.CreateLink(r.Context(), l, resetTTL)
	if err != nil {
		return nil, err
	}
	s.record(r.Context(), p, "account.reset_link", m.Email, nil)
	return LinkView{Link: *l, URL: s.linkURL(r, "reset", secret)}, nil
}

type LinkRequest struct {
	Secret   string `json:"secret"`
	Email    string `json:"email,omitempty"`
	Name     string `json:"name,omitempty"`
	Password string `json:"password,omitempty"`
}

type LinkInfo struct {
	Kind      string      `json:"kind"`
	Org       string      `json:"org,omitempty"`
	OrgName   string      `json:"org_name,omitempty"`
	Role      store.Scope `json:"role,omitempty"`
	CreatedBy string      `json:"created_by,omitempty"`
	Email     string      `json:"email,omitempty"` // the account a reset is for
	ExpiresAt time.Time   `json:"expires_at"`
}

func (s *Server) readLink(w http.ResponseWriter, r *http.Request) (*LinkRequest, *store.Link, bool) {
	if r.Header.Get(CSRFHeader) == "" {
		writeErr(w, s.log, apierr.New(403, "csrf_check_failed", "requests need the %s header", CSRFHeader))
		return nil, nil, false
	}
	if s.logins.blocked("ip:" + clientIP(r)) {
		writeErr(w, s.log, apierr.New(429, "too_many_attempts", "too many failed attempts; wait 15 minutes and try again"))
		return nil, nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	var req LinkRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, s.log, err)
		return nil, nil, false
	}
	l, err := s.st.LookupLink(r.Context(), strings.TrimSpace(req.Secret))
	if err != nil {
		writeErr(w, s.log, err)
		return nil, nil, false
	}
	if l == nil {
		s.logins.fail("ip:" + clientIP(r))
		writeErr(w, s.log, apierr.New(404, "link_invalid", "this link is invalid, was already used, or has expired").
			WithHint("ask an admin for a new one"))
		return nil, nil, false
	}
	return &req, l, true
}

func (s *Server) lookupLink(w http.ResponseWriter, r *http.Request) {
	_, l, ok := s.readLink(w, r)
	if !ok {
		return
	}
	info := LinkInfo{Kind: l.Kind, Org: l.Org, Role: l.Role, CreatedBy: l.CreatedBy, ExpiresAt: l.ExpiresAt}
	if o, err := s.st.GetOrg(r.Context(), l.Org); err == nil && o != nil {
		info.OrgName = o.Name
	}
	if l.Kind == store.LinkReset {
		if u, err := s.st.GetUser(r.Context(), l.UserID); err == nil && u != nil {
			info.Email = u.Email
		}
	}
	writeJSON(w, http.StatusOK, info)
}

// acceptLink uses an invite or reset link. An invite adds the signed-in
// user to the org, or creates their account; a reset sets a new password.
// Either way the browser ends up signed in.
func (s *Server) acceptLink(w http.ResponseWriter, r *http.Request) {
	req, l, ok := s.readLink(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	fail := func(err error) { writeErr(w, s.log, err) }
	var u *store.User
	var err error
	org := l.Org
	switch l.Kind {
	case store.LinkReset:
		if err := checkPassword(req.Password); err != nil {
			fail(err)
			return
		}
		if u, err = s.st.GetUser(ctx, l.UserID); err != nil || u == nil {
			fail(apierr.New(404, "link_invalid", "the account no longer exists"))
			return
		}
	case store.LinkInvite:
		if p, err := s.auth(r); err == nil && p.userID != "" {
			u, err = s.st.GetUser(ctx, p.userID)
			if err != nil {
				fail(err)
				return
			}
		} else {
			email, err := checkEmail(req.Email)
			if err != nil {
				fail(err)
				return
			}
			name, err := checkName("name", req.Name)
			if err != nil {
				fail(err)
				return
			}
			if err := checkPassword(req.Password); err != nil {
				fail(err)
				return
			}
			req.Email, req.Name = email, name
		}
	default:
		fail(apierr.New(404, "link_invalid", "unknown link"))
		return
	}
	// Claim the link before acting on it, so it works only once.
	if used, err := s.st.UseLink(ctx, strings.TrimSpace(req.Secret)); err != nil || !used {
		if err == nil {
			err = apierr.New(404, "link_invalid", "this link was already used")
		}
		fail(err)
		return
	}
	if l.Kind == store.LinkReset {
		if err := s.st.SetPassword(ctx, u.ID, req.Password, ""); err != nil {
			fail(err)
			return
		}
		orgs, err := s.st.UserOrgs(ctx, u.ID)
		if err != nil || len(orgs) == 0 {
			fail(apierr.New(403, "no_organization", "your password is set, but your account isn't in any organization"))
			return
		}
		_ = s.st.AddAudit(ctx, store.AuditEntry{Actor: u.Email, Action: "account.reset", Target: u.Email, Org: orgs[0].ID})
		s.startUserSession(w, r, u, orgs[0].ID)
		return
	}
	if u == nil {
		if u, err = s.st.CreateUser(ctx, req.Email, req.Name, req.Password); err != nil {
			fail(err)
			return
		}
	}
	if role, err := s.st.MemberRole(ctx, org, u.ID); err != nil {
		fail(err)
		return
	} else if role == "" {
		if err := s.st.AddMember(ctx, org, u.ID, l.Role); err != nil {
			fail(err)
			return
		}
		_ = s.st.AddAudit(ctx, store.AuditEntry{Actor: u.Email, Action: "member.join", Target: u.Email, Org: org,
			Detail: jsonOf(map[string]any{"role": l.Role, "invited_by": l.CreatedBy})})
	}
	if ck, err := r.Cookie(SessionCookie); err == nil {
		if p, err := s.auth(r); err == nil && p.userID == u.ID {
			if err := s.st.SetSessionOrg(ctx, ck.Value, org); err != nil {
				fail(err)
				return
			}
			role, _ := s.st.MemberRole(ctx, org, u.ID)
			who, err := s.who(ctx, principal{name: u.Email, scope: role, org: org, userID: u.ID})
			if err != nil {
				fail(err)
				return
			}
			writeJSON(w, http.StatusOK, who)
			return
		}
	}
	s.startUserSession(w, r, u, org)
}

// --- moving apps ---

type MoveRequest struct {
	Org string `json:"org"`
}

// moveApp moves an app to another org the signed-in user is an admin of.
func (s *Server) moveApp(r *http.Request, p principal) (any, error) {
	if err := needUser(p); err != nil {
		return nil, err
	}
	var req MoveRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	role, err := s.st.MemberRole(r.Context(), req.Org, p.userID)
	if err != nil {
		return nil, err
	}
	if role == "" {
		return nil, apierr.NotFound("organization", req.Org)
	}
	if role != store.ScopeAdmin {
		return nil, apierr.New(403, "insufficient_scope", "you need to be an admin of %s to move apps into it", req.Org)
	}
	return s.eng.MoveApp(r.Context(), p.name, r.PathValue("name"), req.Org)
}
