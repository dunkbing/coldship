// Package agents runs AI agents (Claude Code, Codex, any ACP agent) for an
// org and lets people chat with them in the dashboard. Each session runs
// the agent in its own container, talks to it over the Agent Client
// Protocol, and gives it Coldship's MCP tools with a short-lived token
// limited to the agent's role and apps. The agent gets no file system or
// terminal from Coldship; tool calls that need approval wait for a person.
package agents

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dunkbing/coldship/internal/acp"
	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/store"
)

// Preset is a known agent and how to start it.
type Preset struct {
	Kind  string   `json:"kind"`
	Name  string   `json:"name"`
	Cmd   []string `json:"cmd"`
	Keys  []string `json:"keys"` // provider key variables, the first required
	Notes string   `json:"notes,omitempty"`
}

// Presets pin adapter versions: they run third-party code on the server.
var Presets = []Preset{
	{Kind: "claude", Name: "Claude Code", Cmd: []string{"npx", "-y", "@agentclientprotocol/claude-agent-acp@0.93.0"}, Keys: []string{"ANTHROPIC_API_KEY"}},
	{Kind: "codex", Name: "Codex", Cmd: []string{"npx", "-y", "@agentclientprotocol/codex-acp@2.2.2"}, Keys: []string{"OPENAI_API_KEY", "CODEX_API_KEY"}},
	{Kind: "gemini", Name: "Gemini CLI", Cmd: []string{"npx", "-y", "@google/gemini-cli@0.64.0", "--acp"}, Keys: []string{"GEMINI_API_KEY"},
		Notes: "experimental"},
}

func preset(kind string) *Preset {
	for i := range Presets {
		if Presets[i].Kind == kind {
			return &Presets[i]
		}
	}
	return nil
}

// TokenPrefix names the tokens agent sessions get; they're revoked when
// the session ends, and swept at start.
const TokenPrefix = "agent-session-"

const (
	image       = "node:22-bookworm-slim"
	workDir     = "/work"
	socketMount = "/run/coldship"
	memoryLimit = 2 << 30
)

// Launcher starts an agent's container.
type Launcher interface {
	RunAttached(ctx context.Context, s runtime.AttachSpec, log io.Writer) (*runtime.Attached, error)
	RemoveLabeled(ctx context.Context, label string) error
}

// Manager holds an org's agent configs and the running sessions.
type Manager struct {
	st     *store.Store
	launch Launcher
	log    *slog.Logger
	// SocketDir is the host directory with Coldship's API socket, mounted
	// into agent containers; Binary is the coldship binary copied in.
	SocketDir string
	Binary    string
	Version   string
	// MaxSessions bounds running sessions on the server; IdleTimeout ends
	// a session nobody has prompted for that long.
	MaxSessions int
	IdleTimeout time.Duration
	// Image runs agents (default node:22-bookworm-slim).
	Image string
	// Alerts: at most MaxAlertSessions run at once (leaving room for people),
	// one per agent, event and app every AlertCooldown. A permission request
	// nobody answers in PermissionTimeout is cancelled. Notify sends reports.
	MaxAlertSessions  int
	AlertCooldown     time.Duration
	AlertIdle         time.Duration
	PermissionTimeout time.Duration
	Notify            func(Alert)

	alerts    chan Alert
	lastAlert map[string]time.Time

	mu       sync.Mutex
	sessions map[string]*Session
	order    []string
}

func New(st *store.Store, l Launcher, log *slog.Logger) *Manager {
	return &Manager{st: st, launch: l, log: log, MaxSessions: 4, IdleTimeout: 30 * time.Minute, Image: image, sessions: map[string]*Session{},
		MaxAlertSessions: 2, AlertCooldown: 30 * time.Minute, AlertIdle: 15 * time.Minute, PermissionTimeout: 15 * time.Minute,
		alerts: make(chan Alert, alertQueue), lastAlert: map[string]time.Time{}}
}

// Start removes leftovers of a previous run (containers and tokens) and
// ends idle sessions until ctx is done.
func (m *Manager) Start(ctx context.Context) {
	if err := m.st.DeleteTokensWithPrefix(ctx, TokenPrefix); err != nil {
		m.log.Warn("removing old agent tokens failed", "err", err)
	}
	if m.launch != nil {
		if err := m.launch.RemoveLabeled(ctx, runtime.AgentLabel); err != nil {
			m.log.Debug("removing old agent containers failed", "err", err)
		}
	}
	go m.runAlerts(ctx)
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				m.StopAll()
				return
			case <-t.C:
				m.reap()
			}
		}
	}()
}

func (m *Manager) reap() {
	m.mu.Lock()
	var idle []*Session
	for _, s := range m.sessions {
		s.mu.Lock()
		limit := m.IdleTimeout
		if s.alert != nil {
			limit = m.AlertIdle
		}
		if s.live() && s.Status != StatusWorking && time.Since(s.lastActive) > limit {
			idle = append(idle, s)
		}
		s.mu.Unlock()
	}
	m.mu.Unlock()
	for _, s := range idle {
		limit := m.IdleTimeout
		if s.alert != nil {
			limit = m.AlertIdle
		}
		s.stop("ended after " + limit.String() + " without prompts")
	}
}

// StopAll ends every session (on shutdown).
func (m *Manager) StopAll() {
	m.mu.Lock()
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	for _, s := range all {
		s.stop("the server is shutting down")
	}
}

// --- configs ---

// AgentView is a config without its secrets.
type AgentView struct {
	Name      string      `json:"name"`
	Kind      string      `json:"kind"`
	Command   string      `json:"command,omitempty"`
	Keys      []string    `json:"keys"` // names of the variables set
	Scope     store.Scope `json:"scope"`
	Apps      []string    `json:"apps,omitempty"`
	Triggers  []string    `json:"triggers"`
	AlertFix  bool        `json:"alert_fix"`
	CreatedBy string      `json:"created_by"`
	CreatedAt time.Time   `json:"created_at"`
	Running   int         `json:"running"`
}

func (m *Manager) view(a store.AgentConfig) AgentView {
	v := AgentView{Name: a.Name, Kind: a.Kind, Command: a.Command, Scope: a.Scope, Apps: a.Apps, CreatedBy: a.CreatedBy, CreatedAt: a.CreatedAt, Keys: []string{},
		Triggers: a.Triggers, AlertFix: a.AlertFix}
	if v.Triggers == nil {
		v.Triggers = []string{}
	}
	for k := range a.Env {
		v.Keys = append(v.Keys, k)
	}
	sort.Strings(v.Keys)
	m.mu.Lock()
	for _, s := range m.sessions {
		if s.Org == a.Org && s.Agent == a.Name && s.isLive() {
			v.Running++
		}
	}
	m.mu.Unlock()
	return v
}

func (m *Manager) List(ctx context.Context, org string) ([]AgentView, error) {
	list, err := m.st.ListAgents(ctx, org)
	if err != nil {
		return nil, err
	}
	out := make([]AgentView, len(list))
	for i, a := range list {
		out[i] = m.view(a)
	}
	return out, nil
}

// ConfigRequest creates or changes an agent. Env values that are "" keep
// the stored value.
type ConfigRequest struct {
	Kind    string            `json:"kind"`
	Command string            `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Scope   string            `json:"scope,omitempty"`
	Apps    []string          `json:"apps,omitempty"`
	// Triggers and AlertFix are kept when left out.
	Triggers *[]string `json:"triggers,omitempty"`
	AlertFix *bool     `json:"alert_fix,omitempty"`
}

var (
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	envNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	patternRe = regexp.MustCompile(`^[a-z*][a-z0-9*-]{0,39}$`)
)

func (m *Manager) Save(ctx context.Context, org, actor, name string, req ConfigRequest) (*AgentView, error) {
	if !nameRe.MatchString(name) {
		return nil, apierr.Invalid("name", "agent name %q must be lowercase letters, digits and dashes", name)
	}
	p := preset(req.Kind)
	if p == nil && req.Kind != "custom" {
		return nil, apierr.Invalid("kind", "kind %q must be claude, codex, gemini or custom", req.Kind)
	}
	if req.Kind == "custom" && strings.TrimSpace(req.Command) == "" {
		return nil, apierr.Invalid("command", "a custom agent needs the command that starts it in ACP mode")
	}
	if req.Kind != "custom" {
		req.Command = ""
	}
	scope := store.ScopeDeploy
	if req.Scope != "" {
		scope = store.Scope(req.Scope)
	}
	// Approvals stay with people: an agent is at most a deployer.
	if scope != store.ScopeRead && scope != store.ScopeDeploy {
		return nil, apierr.Invalid("scope", "an agent's role is read or deploy; approving production changes stays with people")
	}
	for _, a := range req.Apps {
		if !patternRe.MatchString(a) || a == "*" {
			return nil, apierr.Invalid("apps", "app pattern %q must be an app name, optionally with * (like agent-*)", a)
		}
	}
	old, err := m.st.GetAgent(ctx, org, name)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	if old != nil {
		env = old.Env
	}
	for k, v := range req.Env {
		if !envNameRe.MatchString(k) {
			return nil, apierr.Invalid("env", "%q isn't a variable name (use A-Z, 0-9 and _)", k)
		}
		if v != "" {
			env[k] = strings.TrimSpace(v)
		}
	}
	if p != nil {
		ok := false
		for _, k := range p.Keys {
			if env[k] != "" {
				ok = true
			}
		}
		if !ok {
			return nil, apierr.Invalid("env", "%s needs %s", p.Name, strings.Join(p.Keys, " or ")).WithField("env")
		}
	}
	a := &store.AgentConfig{Org: org, Name: name, Kind: req.Kind, Command: strings.TrimSpace(req.Command), Env: env, Scope: scope, Apps: req.Apps,
		CreatedBy: actor, CreatedAt: time.Now()}
	if old != nil {
		a.CreatedBy, a.CreatedAt, a.Triggers, a.AlertFix = old.CreatedBy, old.CreatedAt, old.Triggers, old.AlertFix
	}
	if req.Triggers != nil {
		a.Triggers = nil
		for _, k := range *req.Triggers {
			if !slices.Contains(AlertKinds, k) {
				return nil, apierr.Invalid("triggers", "%q isn't an alert agents respond to (%s)", k, strings.Join(AlertKinds, ", "))
			}
			if k == "disk.low" && org != store.DefaultOrg {
				return nil, apierr.Invalid("triggers", "disk alerts are about the server; only the default organization gets them")
			}
			if !slices.Contains(a.Triggers, k) {
				a.Triggers = append(a.Triggers, k)
			}
		}
	}
	if req.AlertFix != nil {
		a.AlertFix = *req.AlertFix
	}
	if err := m.st.SaveAgent(ctx, a); err != nil {
		return nil, err
	}
	m.audit(ctx, org, actor, "agent.save", name, map[string]any{"kind": a.Kind, "scope": scope, "apps": a.Apps, "triggers": a.Triggers, "alert_fix": a.AlertFix})
	v := m.view(*a)
	return &v, nil
}

func (m *Manager) Delete(ctx context.Context, org, actor, name string) error {
	a, err := m.st.GetAgent(ctx, org, name)
	if err != nil {
		return err
	}
	if a == nil {
		return apierr.NotFound("agent", name)
	}
	m.mu.Lock()
	var running []*Session
	for _, s := range m.sessions {
		if s.Org == org && s.Agent == name {
			running = append(running, s)
		}
	}
	m.mu.Unlock()
	for _, s := range running {
		s.stop("the agent was removed")
	}
	if err := m.st.DeleteAgent(ctx, org, name); err != nil {
		return err
	}
	m.audit(ctx, org, actor, "agent.delete", name, nil)
	return nil
}

func (m *Manager) audit(ctx context.Context, org, actor, action, target string, detail any) {
	var raw json.RawMessage
	if detail != nil {
		raw, _ = json.Marshal(detail)
	}
	_ = m.st.AddAudit(context.WithoutCancel(ctx), store.AuditEntry{Actor: actor, Action: action, Target: target, Org: org, Detail: raw})
}

// --- sessions ---

// Session statuses.
const (
	StatusStarting = "starting"
	StatusReady    = "ready"
	StatusWorking  = "working"
	StatusEnded    = "ended"
	StatusFailed   = "failed"
)

// Item is one entry of a session's transcript. Everything in it came from
// the agent or the model: show it as text, never as HTML.
type Item struct {
	Key        string          `json:"key"`
	Kind       string          `json:"kind"` // user | agent | thought | tool | plan | permission | log | notice
	Text       string          `json:"text,omitempty"`
	Tool       *ToolView       `json:"tool,omitempty"`
	Plan       []acp.PlanEntry `json:"plan,omitempty"`
	Permission *PermissionView `json:"permission,omitempty"`
	By         string          `json:"by,omitempty"` // who sent a user message
	Time       time.Time       `json:"time"`
	messageID  string
}

type ToolView struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Kind   string `json:"kind,omitempty"`
	Status string `json:"status"`
	Input  string `json:"input,omitempty"`
	Output string `json:"output,omitempty"`
}

type PermissionView struct {
	ID       string                 `json:"id"`
	Title    string                 `json:"title"`
	Input    string                 `json:"input,omitempty"`
	Options  []acp.PermissionOption `json:"options"`
	Status   string                 `json:"status"` // pending | answered | cancelled
	Chosen   string                 `json:"chosen,omitempty"`
	Answered string                 `json:"answered_by,omitempty"`
}

// Event is what the dashboard's stream receives.
type Event struct {
	Type   string       `json:"type"` // snapshot | item | status
	Item   *Item        `json:"item,omitempty"`
	Status string       `json:"status,omitempty"`
	Error  string       `json:"error,omitempty"`
	View   *SessionView `json:"session,omitempty"`
}

type SessionView struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent"`
	Kind      string    `json:"kind"`
	Scope     string    `json:"scope"`
	StartedBy string    `json:"started_by"`
	CreatedAt time.Time `json:"created_at"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	AgentInfo string    `json:"agent_info,omitempty"`
	Alert     string    `json:"alert,omitempty"` // the event that started it
	AlertApp  string    `json:"alert_app,omitempty"`
	Items     []*Item   `json:"items,omitempty"`
}

type Session struct {
	ID, Org, Agent, Kind, StartedBy string
	Scope                           store.Scope
	CreatedAt                       time.Time

	m          *Manager
	mu         sync.Mutex
	Status     string
	Error      string
	agentInfo  string
	items      []*Item
	byKey      map[string]*Item
	seq        int
	subs       map[chan Event]struct{}
	conn       *acp.Conn
	acpID      string
	proc       *runtime.Attached
	token      string
	perms      map[string]chan acp.PermissionOutcome
	lastActive time.Time
	ctx        context.Context
	cancel     context.CancelFunc

	// Sessions started by an alert: the first prompt, and what was sent back.
	alert       *Alert
	alertKey    string
	prompt      string
	reported    bool
	askedPeople bool
}

// live reports whether the session runs; hold s.mu (or use isLive).
func (s *Session) live() bool { return s.Status != StatusEnded && s.Status != StatusFailed }

func (s *Session) isLive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live()
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// StartRequest is who starts a session and with what rights.
type StartRequest struct {
	Org    string
	Agent  string
	Actor  string
	Caller store.Scope // the person's role; the agent gets no more
	Prompt string      // sent once the session is ready

	alert    *Alert
	alertKey string
}

// StartSession launches the agent and connects to it. It returns at once;
// the session becomes ready (or failed) in the background.
func (m *Manager) StartSession(ctx context.Context, r StartRequest) (*SessionView, error) {
	a, err := m.st.GetAgent(ctx, r.Org, r.Agent)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, apierr.NotFound("agent", r.Agent)
	}
	if m.launch == nil {
		return nil, apierr.New(409, "agents_unavailable", "agents need a container engine, which this server doesn't have")
	}
	scope := a.Scope
	if !r.Caller.Allows(scope) {
		scope = r.Caller
	}
	m.mu.Lock()
	running := 0
	for _, s := range m.sessions {
		if s.isLive() {
			running++
		}
	}
	if running >= m.MaxSessions {
		m.mu.Unlock()
		return nil, apierr.New(429, "too_many_sessions", "%d agent sessions are running, the most this server allows; end one first", running)
	}
	sctx, cancel := context.WithCancel(context.Background())
	s := &Session{ID: newID(), Org: r.Org, Agent: a.Name, Kind: a.Kind, StartedBy: r.Actor, Scope: scope, CreatedAt: time.Now(),
		m: m, Status: StatusStarting, byKey: map[string]*Item{}, subs: map[chan Event]struct{}{}, perms: map[string]chan acp.PermissionOutcome{},
		lastActive: time.Now(), ctx: sctx, cancel: cancel, alert: r.alert, alertKey: r.alertKey, prompt: strings.TrimSpace(r.Prompt)}
	m.sessions[s.ID] = s
	m.order = append(m.order, s.ID)
	m.prune()
	m.mu.Unlock()
	detail := map[string]any{"session": s.ID, "scope": scope}
	if r.alert != nil {
		detail["alert"], detail["app"] = r.alert.Kind, r.alert.App
	}
	m.audit(ctx, r.Org, r.Actor, "agent.session.start", a.Name, detail)
	go s.run(*a)
	return s.View(true), nil
}

// prune forgets the oldest ended sessions beyond 30. Holds m.mu.
func (m *Manager) prune() {
	for len(m.order) > 30 {
		dropped := false
		for i, id := range m.order {
			s := m.sessions[id]
			s.mu.Lock()
			live := s.live()
			s.mu.Unlock()
			if !live {
				delete(m.sessions, id)
				m.order = append(m.order[:i], m.order[i+1:]...)
				dropped = true
				break
			}
		}
		if !dropped {
			return
		}
	}
}

// Session returns an org's session.
func (m *Manager) Session(org, id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil || s.Org != org {
		return nil, apierr.NotFound("agent_session", id)
	}
	return s, nil
}

// Sessions lists an org's sessions, newest first.
func (m *Manager) Sessions(org string) []*SessionView {
	m.mu.Lock()
	var list []*Session
	for i := len(m.order) - 1; i >= 0; i-- {
		if s := m.sessions[m.order[i]]; s.Org == org {
			list = append(list, s)
		}
	}
	m.mu.Unlock()
	out := make([]*SessionView, 0, len(list))
	for _, s := range list {
		out = append(out, s.View(false))
	}
	return out
}

func (s *Session) View(items bool) *SessionView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.view(items)
}

// view is View with s.mu held.
func (s *Session) view(items bool) *SessionView {
	v := &SessionView{ID: s.ID, Agent: s.Agent, Kind: s.Kind, Scope: string(s.Scope), StartedBy: s.StartedBy, CreatedAt: s.CreatedAt,
		Status: s.Status, Error: s.Error, AgentInfo: s.agentInfo}
	if s.alert != nil {
		v.Alert, v.AlertApp = s.alert.Kind, s.alert.App
	}
	if items {
		v.Items = make([]*Item, len(s.items))
		for i, it := range s.items {
			c := *it
			v.Items[i] = &c
		}
	}
	return v
}

// Subscribe streams the session: a snapshot, then changes. Call the
// returned func to stop.
func (s *Session) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 512)
	// Snapshot and register together, so no change falls in between.
	s.mu.Lock()
	ch <- Event{Type: "snapshot", View: s.view(true)}
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
}

// broadcast sends e to subscribers. A subscriber too slow to keep up is
// dropped (its stream ends, and the dashboard reconnects). Holds s.mu.
func (s *Session) broadcast(e Event) {
	for ch := range s.subs {
		select {
		case ch <- e:
		default:
			delete(s.subs, ch)
			close(ch)
		}
	}
}

func (s *Session) setStatus(status, errText string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.live() {
		return
	}
	s.Status, s.Error = status, errText
	s.broadcast(Event{Type: "status", Status: status, Error: errText})
}

// upsert adds or replaces an item. Holds s.mu.
func (s *Session) upsert(it *Item) {
	if it.Time.IsZero() {
		it.Time = time.Now()
	}
	if old, ok := s.byKey[it.Key]; ok {
		*old = *it
	} else {
		s.items = append(s.items, it)
		s.byKey[it.Key] = it
	}
	c := *it
	s.broadcast(Event{Type: "item", Item: &c})
}

func (s *Session) add(kind, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	s.upsert(&Item{Key: fmt.Sprintf("%s:%d", kind, s.seq), Kind: kind, Text: text})
}

const (
	maxText   = 200 << 10
	maxOutput = 16 << 10
)

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "\n… (cut)"
	}
	return s
}

// run starts the container, connects and keeps the session going until the
// agent exits or the session is stopped.
func (s *Session) run(a store.AgentConfig) {
	m := s.m
	fail := func(err error) {
		s.add("notice", err.Error())
		s.setStatus(StatusFailed, err.Error())
		s.cleanup()
		s.report("The agent didn't start: " + err.Error())
	}
	scopeApps := a.Apps
	tokName := TokenPrefix + s.ID
	tok, err := m.st.CreateToken(s.ctx, s.Org, tokName, s.Scope, scopeApps)
	if err != nil {
		fail(fmt.Errorf("creating the agent's token: %w", err))
		return
	}
	s.mu.Lock()
	s.token = tokName
	s.mu.Unlock()

	cmd := []string{"sh", "-c", a.Command}
	if p := preset(a.Kind); p != nil {
		cmd = p.Cmd
	}
	env := []string{"NO_UPDATE_NOTIFIER=1", "NPM_CONFIG_UPDATE_NOTIFIER=false", "NPM_CONFIG_FUND=false"}
	for k, v := range a.Env {
		env = append(env, k+"="+v)
	}
	status := &lineLog{s: s, key: "log:start"}
	fmt.Fprintf(status, "starting %s in a container (%s)…\n", a.Name, m.Image)
	proc, err := m.launch.RunAttached(s.ctx, runtime.AttachSpec{
		Name:       "coldship-agent-" + s.ID,
		Image:      m.Image,
		Cmd:        cmd,
		Env:        env,
		Labels:     map[string]string{runtime.AgentLabel: a.Name, "coldship.org": s.Org},
		Binds:      []string{runtime.AgentVolumePrefix + s.Org + "-" + a.Name + ":/root", m.SocketDir + ":" + socketMount + ":z"},
		WorkingDir: workDir,
		Memory:     memoryLimit,
		Files:      []runtime.CopyIn{{From: m.Binary, Dir: "/usr/local/bin", Name: "coldship", Mode: 0o755}},
	}, status)
	if err != nil {
		fail(err)
		return
	}
	s.mu.Lock()
	s.proc = proc
	s.mu.Unlock()
	go status.copy(proc.Stderr)
	conn := acp.NewConn(proc.Stdout, proc.Stdin, s.handle)
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
	go func() {
		<-conn.Done()
		err := proc.Wait()
		s.mu.Lock()
		live := s.live()
		s.mu.Unlock()
		if live {
			msg := "the agent exited"
			if err != nil {
				msg = err.Error()
			}
			s.add("notice", msg)
			s.setStatus(StatusEnded, "")
		}
		s.cleanup()
	}()

	// npx may download the adapter first: give it time.
	ictx, cancel := context.WithTimeout(s.ctx, 10*time.Minute)
	defer cancel()
	var init acp.InitializeResult
	if err := conn.Call(ictx, "initialize", acp.InitializeParams{ProtocolVersion: acp.ProtocolVersion,
		ClientInfo: acp.Implementation{Name: "coldship", Title: "Coldship", Version: m.Version}}, &init); err != nil {
		fail(fmt.Errorf("the agent didn't start: %w", err))
		return
	}
	if init.AgentInfo != nil {
		s.mu.Lock()
		s.agentInfo = strings.TrimSpace(init.AgentInfo.Name + " " + init.AgentInfo.Version)
		s.mu.Unlock()
	}
	if len(init.AuthMethods) > 0 {
		ids := make([]string, len(init.AuthMethods))
		for i, am := range init.AuthMethods {
			ids[i] = am.ID
		}
		m.log.Info("agent auth methods", "agent", a.Name, "methods", strings.Join(ids, ","))
	}
	params := acp.NewSessionParams{Cwd: workDir, MCPServers: []acp.MCPServer{{
		Name: "coldship", Command: "/usr/local/bin/coldship", Args: []string{"mcp"},
		Env: []acp.EnvVar{{Name: "COLDSHIP_URL", Value: "unix://" + socketMount + "/api.sock"}, {Name: "COLDSHIP_TOKEN", Value: tok}},
	}}}
	var ns acp.NewSessionResult
	err = conn.Call(ictx, "session/new", params, &ns)
	var re *acp.RPCError
	if errors.As(err, &re) && re.Code == acp.CodeAuthRequired {
		if method := authMethod(init.AuthMethods, a.Env); method != "" {
			if err = conn.Call(ictx, "authenticate", map[string]string{"methodId": method}, nil); err == nil {
				err = conn.Call(ictx, "session/new", params, &ns)
			}
		}
		if err != nil {
			err = fmt.Errorf("the agent wants to sign in (%v): check its API key", err)
		}
	}
	if err != nil {
		fail(fmt.Errorf("starting a session failed: %w", err))
		return
	}
	s.mu.Lock()
	s.acpID = ns.SessionID
	s.mu.Unlock()
	status.done()
	s.setStatus(StatusReady, "")
	if s.prompt != "" {
		if err := s.Prompt(s.StartedBy, s.prompt); err != nil {
			s.report("The agent couldn't take the alert: " + err.Error())
		}
	}
}

// authMethod picks the sign-in method that uses an API key from env.
func authMethod(methods []acp.AuthMethod, env map[string]string) string {
	var first string
	for _, am := range methods {
		if am.Type == "terminal" {
			continue
		}
		id := strings.ToLower(am.ID + " " + am.Name)
		if strings.Contains(id, "key") || strings.Contains(id, "api") {
			return am.ID
		}
		if first == "" {
			first = am.ID
		}
	}
	return first
}

// cleanup revokes the token and removes the container.
func (s *Session) cleanup() {
	s.mu.Lock()
	tok, proc := s.token, s.proc
	s.token = ""
	for id, ch := range s.perms {
		close(ch)
		delete(s.perms, id)
	}
	s.mu.Unlock()
	s.cancel()
	if tok != "" {
		if _, err := s.m.st.DeleteToken(context.Background(), tok); err != nil {
			s.m.log.Warn("revoking an agent token failed", "token", tok, "err", err)
		}
	}
	if proc != nil {
		proc.Kill()
	}
}

// stop ends the session.
func (s *Session) stop(why string) {
	s.mu.Lock()
	live := s.live()
	s.mu.Unlock()
	if !live {
		return
	}
	if why != "" {
		s.add("notice", why)
	}
	s.setStatus(StatusEnded, "")
	s.cleanup()
}

// Stop ends the session for a person.
func (s *Session) Stop(actor string) {
	s.m.audit(context.Background(), s.Org, actor, "agent.session.stop", s.Agent, map[string]any{"session": s.ID})
	s.stop("ended by " + actor)
}

// Prompt sends a person's message; the agent answers in the background.
func (s *Session) Prompt(actor, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return apierr.Invalid("text", "write a message")
	}
	s.mu.Lock()
	if s.Status != StatusReady {
		st := s.Status
		s.mu.Unlock()
		if st == StatusWorking {
			return apierr.New(409, "agent_busy", "the agent is still working on the last message; wait, or stop it")
		}
		return apierr.New(409, "agent_not_ready", "the session is %s", st)
	}
	s.Status = StatusWorking
	s.lastActive = time.Now()
	s.broadcast(Event{Type: "status", Status: StatusWorking})
	s.seq++
	s.upsert(&Item{Key: fmt.Sprintf("user:%d", s.seq), Kind: "user", Text: clip(text, maxText), By: actor})
	conn, id := s.conn, s.acpID
	s.mu.Unlock()
	go func() {
		var res acp.PromptResult
		err := conn.Call(s.ctx, "session/prompt", acp.PromptParams{SessionID: id, Prompt: []acp.ContentBlock{{Type: "text", Text: text}}}, &res)
		s.mu.Lock()
		s.lastActive = time.Now()
		s.mu.Unlock()
		failed := ""
		switch {
		case err != nil:
			failed = "the agent failed: " + err.Error()
			s.add("notice", failed)
		case res.StopReason != "" && res.StopReason != "end_turn":
			s.add("notice", "stopped: "+strings.ReplaceAll(res.StopReason, "_", " "))
		}
		if s.isLive() {
			s.report(failed)
		}
		s.mu.Lock()
		if s.Status == StatusWorking {
			s.Status = StatusReady
			s.broadcast(Event{Type: "status", Status: StatusReady})
		}
		s.mu.Unlock()
	}()
	return nil
}

// Cancel stops the agent's current turn.
func (s *Session) Cancel() {
	s.mu.Lock()
	conn, id := s.conn, s.acpID
	for pid, ch := range s.perms {
		ch <- acp.PermissionOutcome{Outcome: "cancelled"}
		delete(s.perms, pid)
	}
	s.mu.Unlock()
	if conn != nil && id != "" {
		conn.Notify("session/cancel", map[string]string{"sessionId": id})
	}
}

// Answer answers a permission request with one of its options.
func (s *Session) Answer(actor, permID, optionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.perms[permID]
	it := s.byKey["perm:"+permID]
	if !ok || it == nil {
		return apierr.New(409, "permission_not_pending", "that request was already answered or cancelled")
	}
	valid := false
	for _, o := range it.Permission.Options {
		if o.OptionID == optionID {
			valid = true
		}
	}
	if !valid {
		return apierr.Invalid("option", "unknown option %q", optionID)
	}
	delete(s.perms, permID)
	ch <- acp.PermissionOutcome{Outcome: "selected", OptionID: optionID}
	c := *it
	p := *it.Permission
	p.Status, p.Chosen, p.Answered = "answered", optionID, actor
	c.Permission = &p
	s.upsert(&c)
	s.lastActive = time.Now()
	return nil
}

// --- the agent's requests ---

func (s *Session) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "session/update":
		var n acp.SessionNotification
		if err := json.Unmarshal(params, &n); err != nil {
			return nil, err
		}
		var u acp.Update
		if err := json.Unmarshal(n.Update, &u); err != nil {
			return nil, err
		}
		s.apply(u)
		return nil, nil
	case "session/request_permission":
		var p acp.PermissionParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return s.askPermission(ctx, p), nil
	}
	return nil, acp.ErrMethodNotFound(method)
}

func (s *Session) askPermission(ctx context.Context, p acp.PermissionParams) acp.PermissionResult {
	ch := make(chan acp.PermissionOutcome, 1)
	s.mu.Lock()
	s.seq++
	id := fmt.Sprint(s.seq)
	title := ""
	if p.ToolCall.Title != nil {
		title = *p.ToolCall.Title
	}
	if t := s.byKey["tool:"+p.ToolCall.ToolCallID]; t != nil && title == "" {
		title = t.Tool.Title
	}
	if title == "" {
		title = "Use a tool"
	}
	view := &PermissionView{ID: id, Title: clip(title, 500), Input: clip(rawText(p.ToolCall.RawInput), maxOutput), Options: p.Options, Status: "pending"}
	s.perms[id] = ch
	s.upsert(&Item{Key: "perm:" + id, Kind: "permission", Permission: view})
	unattended := s.alert != nil
	s.mu.Unlock()
	// Nobody may be watching an alert session: ask people, and don't hold
	// a session slot forever.
	var timeout <-chan time.Time
	if unattended {
		s.waiting(title)
		t := time.NewTimer(s.m.PermissionTimeout)
		defer t.Stop()
		timeout = t.C
	}
	var out acp.PermissionOutcome
	var ok bool
	select {
	case out, ok = <-ch:
		if !ok {
			out = acp.PermissionOutcome{Outcome: "cancelled"}
		}
	case <-ctx.Done():
		out = acp.PermissionOutcome{Outcome: "cancelled"}
	case <-timeout:
		// ACP answers "cancelled" only for a cancelled turn: cancel it.
		out = acp.PermissionOutcome{Outcome: "cancelled"}
		s.add("notice", "nobody answered in "+s.m.PermissionTimeout.String()+"; the turn was cancelled")
		s.mu.Lock()
		conn, sid := s.conn, s.acpID
		s.mu.Unlock()
		if conn != nil && sid != "" {
			conn.Notify("session/cancel", map[string]string{"sessionId": sid})
		}
	}
	if out.Outcome == "cancelled" {
		s.mu.Lock()
		delete(s.perms, id)
		if it := s.byKey["perm:"+id]; it != nil && it.Permission.Status == "pending" {
			c := *it
			pv := *it.Permission
			pv.Status = "cancelled"
			c.Permission = &pv
			s.upsert(&c)
		}
		s.mu.Unlock()
	}
	return acp.PermissionResult{Outcome: out}
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func blockText(raw json.RawMessage) string {
	var b acp.ContentBlock
	if json.Unmarshal(raw, &b) != nil {
		return ""
	}
	switch b.Type {
	case "text":
		return b.Text
	case "":
		return ""
	}
	return "[" + b.Type + "]"
}

func toolContent(raw json.RawMessage) string {
	var list []acp.ToolCallContent
	if json.Unmarshal(raw, &list) != nil {
		return ""
	}
	var parts []string
	for _, c := range list {
		switch c.Type {
		case "content":
			if c.Content != nil && c.Content.Type == "text" {
				parts = append(parts, c.Content.Text)
			}
		case "diff":
			parts = append(parts, "--- "+c.Path+"\n"+c.NewText)
		case "terminal":
			parts = append(parts, "(terminal output)")
		}
	}
	return strings.Join(parts, "\n")
}

// apply folds a session/update into the transcript.
func (s *Session) apply(u acp.Update) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch u.SessionUpdate {
	case "agent_message_chunk", "agent_thought_chunk":
		kind := "agent"
		if u.SessionUpdate == "agent_thought_chunk" {
			kind = "thought"
		}
		text := blockText(u.Content)
		if text == "" {
			return
		}
		if n := len(s.items); n > 0 {
			last := s.items[n-1]
			if last.Kind == kind && last.messageID == u.MessageID {
				c := *last
				c.Text = clip(c.Text+text, maxText)
				s.upsert(&c)
				return
			}
		}
		s.seq++
		s.upsert(&Item{Key: fmt.Sprintf("%s:%d", kind, s.seq), Kind: kind, Text: clip(text, maxText), messageID: u.MessageID})
	case "tool_call", "tool_call_update":
		key := "tool:" + u.ToolCallID
		it := &Item{Key: key, Kind: "tool", Tool: &ToolView{ID: u.ToolCallID, Status: "pending"}}
		if old := s.byKey[key]; old != nil {
			c := *old
			t := *old.Tool
			c.Tool = &t
			it = &c
		}
		t := it.Tool
		if u.Title != nil {
			t.Title = clip(*u.Title, 500)
		}
		if u.Kind != "" {
			t.Kind = u.Kind
		}
		if u.Status != "" {
			t.Status = u.Status
		}
		if in := rawText(u.RawInput); in != "" {
			t.Input = clip(in, maxOutput)
		}
		if out := toolContent(u.Content); out != "" {
			t.Output = clip(out, maxOutput)
		}
		if t.Title == "" {
			t.Title = "Tool call"
		}
		s.upsert(it)
	case "plan":
		s.upsert(&Item{Key: "plan", Kind: "plan", Plan: u.Entries})
	}
}

// lineLog shows the container's startup output (npx downloading, the
// agent's own logs) as one transcript item, until the session is ready.
type lineLog struct {
	s     *Session
	key   string
	mu    sync.Mutex
	lines []string
	off   bool
	buf   strings.Builder
}

func (l *lineLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(b)
	text := l.buf.String()
	for {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(text[:i]); line != "" {
			l.lines = append(l.lines, clip(line, 400))
		}
		text = text[i+1:]
	}
	l.buf.Reset()
	l.buf.WriteString(text)
	if len(l.lines) > 15 {
		l.lines = l.lines[len(l.lines)-15:]
	}
	if !l.off {
		l.s.mu.Lock()
		l.s.upsert(&Item{Key: l.key, Kind: "log", Text: strings.Join(l.lines, "\n")})
		l.s.mu.Unlock()
	}
	return len(b), nil
}

func (l *lineLog) copy(r io.Reader) {
	br := bufio.NewReader(r)
	io.Copy(l, br)
}

// done stops updating the startup item; later stderr is dropped.
func (l *lineLog) done() {
	l.mu.Lock()
	l.off = true
	l.mu.Unlock()
}
