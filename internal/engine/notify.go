package engine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/netguard"
	"github.com/dunkbing/coldship/internal/planner"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

// Notifications: the engine emits events (a deploy failed, an app went
// down…); subscribers act on them. The built-in one sends them to the
// org's channels: Slack, Discord, Telegram or a signed webhook.

// Event kinds.
const (
	EvDeployFailed    = "deploy.failed"
	EvDeploySucceeded = "deploy.succeeded"
	EvApprovalNeeded  = "approval.needed"
	EvAppDown         = "app.down"
	EvAppRecovered    = "app.recovered"
	EvBackupFailing   = "backup.failing"
	EvBackupRecovered = "backup.recovered"
	EvDiskLow         = "disk.low"
)

// EventKind describes an event for pickers.
type EventKind struct {
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Default bool   `json:"default"`
	// Server events are about the host; only the default org gets them.
	Server bool `json:"server,omitempty"`
}

var EventKinds = []EventKind{
	{EvDeployFailed, "A deploy or build failed", true, false},
	{EvDeploySucceeded, "A deploy succeeded", false, false},
	{EvApprovalNeeded, "A change waits for an admin's approval", true, false},
	{EvAppDown, "An app stopped running", true, false},
	{EvAppRecovered, "An app is running again", true, false},
	{EvBackupFailing, "Off-site backups are failing", true, false},
	{EvBackupRecovered, "Off-site backups work again", true, false},
	{EvDiskLow, "The server's disk is almost full", true, true},
}

// Event is something worth telling people. Title and Detail may hold
// untrusted text (build output, error messages): senders escape it.
type Event struct {
	Kind       string    `json:"event"`
	Org        string    `json:"org"`
	App        string    `json:"app,omitempty"`
	Deployment string    `json:"deployment,omitempty"`
	Plan       string    `json:"plan,omitempty"`
	Severity   string    `json:"severity"` // error | warning | ok | info
	Title      string    `json:"title"`
	Detail     string    `json:"detail,omitempty"`
	Link       string    `json:"link,omitempty"`
	At         time.Time `json:"at"`
}

type notifier struct {
	once    sync.Once
	queue   chan Event
	subsMu  sync.Mutex
	subs    []func(Event)
	stMu    sync.Mutex
	last    map[string]delivery // by channel
	asked   map[string]bool     // plans whose approval was asked for
	client  func(restricted bool) *http.Client
	baseURL string
}

type delivery struct {
	at  time.Time
	err string
}

func (e *Engine) notif() *notifier {
	e.nt.once.Do(func() {
		e.nt.queue = make(chan Event, 256)
		e.nt.last = map[string]delivery{}
		e.nt.asked = map[string]bool{}
		if e.nt.client == nil {
			open, guarded := netguard.Client(false, 10*time.Second), netguard.Client(true, 10*time.Second)
			e.nt.client = func(r bool) *http.Client {
				if r {
					return guarded
				}
				return open
			}
		}
	})
	return &e.nt
}

// SetPublicURL is the dashboard's address, for links in notifications.
func (e *Engine) SetPublicURL(u string) { e.notif().baseURL = strings.TrimRight(u, "/") }

// Subscribe calls f for every event, on the emitting goroutine: f must
// not block.
func (e *Engine) Subscribe(f func(Event)) {
	n := e.notif()
	n.subsMu.Lock()
	n.subs = append(n.subs, f)
	n.subsMu.Unlock()
}

// emit sends ev to its org's channels and the subscribers. It never blocks.
func (e *Engine) emit(ctx context.Context, ev Event) {
	n := e.notif()
	if ev.App != "" {
		// Apps move between orgs: the event belongs to the current one.
		if a, err := e.st.GetApp(context.WithoutCancel(ctx), ev.App); err == nil && a != nil {
			if a.PreviewOf != "" {
				return // previews come and go; they don't page anyone
			}
			ev.Org = a.Org
		}
	}
	ev.Org = store.OrgOr(ev.Org)
	if ev.At.IsZero() {
		ev.At = e.Now()
	}
	if n.baseURL != "" && ev.Link == "" {
		switch {
		case ev.Kind == EvApprovalNeeded:
			ev.Link = n.baseURL + "/approvals"
		case ev.App != "":
			ev.Link = n.baseURL + "/apps/" + url.PathEscape(ev.App)
		case strings.HasPrefix(ev.Kind, "backup."):
			ev.Link = n.baseURL + "/backups"
		case ev.Kind == EvDiskLow:
			ev.Link = n.baseURL + "/server"
		}
	}
	select {
	case n.queue <- ev:
	default:
		slog.Warn("notification queue full, dropping event", "event", ev.Kind, "app", ev.App)
	}
	n.subsMu.Lock()
	subs := slices.Clone(n.subs)
	n.subsMu.Unlock()
	for _, f := range subs {
		f(ev)
	}
}

// askApproval notifies once per plan that it needs an admin.
func (e *Engine) askApproval(ctx context.Context, p *store.Plan, by string) {
	n := e.notif()
	n.stMu.Lock()
	seen := n.asked[p.ID]
	n.asked[p.ID] = true
	n.stMu.Unlock()
	if seen {
		return
	}
	e.emit(ctx, Event{Kind: EvApprovalNeeded, Org: p.Org, App: p.App, Plan: p.ID, Severity: "warning",
		Title:  fmt.Sprintf("%s: a change needs approval", p.App),
		Detail: fmt.Sprintf("%s wants to apply plan %s, a destructive change to a production app. Review it, then: coldship approve %s", by, p.ID, p.ID)})
}

// RunNotifications delivers events until ctx ends.
func (e *Engine) RunNotifications(ctx context.Context, log *slog.Logger) {
	n := e.notif()
	sem := make(chan struct{}, 4)
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-n.queue:
			chans, err := e.st.ListChannels(ctx, ev.Org)
			if err != nil {
				log.Error("notifications", "err", err)
				continue
			}
			for _, c := range chans {
				if !slices.Contains(c.Events, ev.Kind) {
					continue
				}
				sem <- struct{}{}
				go func(c store.Channel) {
					defer func() { <-sem }()
					var err error
					for try, wait := 0, 2*time.Second; try < 3; try, wait = try+1, wait*4 {
						if err = e.send(ctx, &c, ev); err == nil || try == 2 {
							break
						}
						select {
						case <-ctx.Done():
							return
						case <-time.After(wait):
						}
					}
					if err != nil {
						log.Warn("notification not delivered", "channel", c.Name, "event", ev.Kind, "err", err)
					}
				}(c)
			}
		}
	}
}

// --- sending ---

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// discordEscape stops markdown (links, mentions, formatting) in a line.
func discordEscape(s string) string {
	return strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)", "*", "\\*", "_", "\\_",
		"~", "\\~", "`", "\\`", "|", "\\|", "<", "\\<", ">", "\\>", "#", "\\#").Replace(s)
}

// slackEscape keeps text from becoming links or mentions.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func (e *Engine) send(ctx context.Context, c *store.Channel, ev Event) error {
	err := e.deliver(ctx, c, ev)
	n := e.notif()
	n.stMu.Lock()
	d := delivery{at: e.Now()}
	if err != nil {
		d.err = err.Error()
	}
	n.last[c.ID] = d
	n.stMu.Unlock()
	return err
}

func (e *Engine) deliver(ctx context.Context, c *store.Channel, ev Event) error {
	var body []byte
	target := c.Target.URL
	headers := map[string]string{"Content-Type": "application/json", "User-Agent": "Coldship"}
	detail := clipText(ev.Detail, 1500)
	switch c.Kind {
	case "slack":
		text := "*" + slackEscape(ev.Title) + "*"
		if detail != "" {
			text += "\n" + slackEscape(detail)
		}
		if ev.Link != "" {
			text += "\n<" + ev.Link + "|Open in Coldship>"
		}
		body = jsonBody(map[string]any{"text": text})
	case "discord":
		// The detail goes in a code block (with its backticks swapped for
		// look-alikes), so untrusted text can't render as masked links.
		text := "**" + discordEscape(ev.Title) + "**"
		if detail != "" {
			text += "\n```\n" + strings.ReplaceAll(clipText(detail, 1700), "`", "\u02cb") + "\n```"
		}
		if ev.Link != "" {
			text += "\n" + ev.Link
		}
		body = jsonBody(map[string]any{"content": clipText(text, 1900), "allowed_mentions": map[string]any{"parse": []string{}}})
	case "telegram":
		// Plain text: no parse mode, so nothing in it is markup.
		text := ev.Title
		if detail != "" {
			text += "\n\n" + detail
		}
		if ev.Link != "" {
			text += "\n\n" + ev.Link
		}
		target = "https://api.telegram.org/bot" + c.Target.BotToken + "/sendMessage"
		body = jsonBody(map[string]any{"chat_id": c.Target.ChatID, "text": clipText(text, 4000), "disable_web_page_preview": true})
	case "webhook":
		body = jsonBody(ev)
		stamp := strconv.FormatInt(e.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(c.Target.Secret))
		mac.Write([]byte(stamp + "." + string(body)))
		headers["X-Coldship-Event"] = ev.Kind
		headers["X-Coldship-Timestamp"] = stamp
		headers["X-Coldship-Signature"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	default:
		return fmt.Errorf("unknown channel kind %q", c.Kind)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid channel address")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.notif().client(restricted(c.Org)).Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL may hold a token
		}
		return fmt.Errorf("couldn't reach %s: %v", hostOf(c), err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s answered %s", hostOf(c), resp.Status)
	}
	return nil
}

// hostOf names a channel's destination without its secrets.
func hostOf(c *store.Channel) string {
	if c.Kind == "telegram" {
		return "Telegram"
	}
	if u, err := url.Parse(c.Target.URL); err == nil && u.Host != "" {
		return u.Host
	}
	return c.Kind
}

// jsonBody encodes v without HTML escaping, so text arrives as written.
func jsonBody(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// --- managing channels ---

// ChannelRequest creates or changes a channel. Empty secret fields keep
// the saved ones on update.
type ChannelRequest struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	URL      string   `json:"url,omitempty"`
	BotToken string   `json:"bot_token,omitempty"`
	ChatID   string   `json:"chat_id,omitempty"`
	Events   []string `json:"events,omitempty"`
}

// ChannelView is a channel without its secrets.
type ChannelView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Kind      string     `json:"kind"`
	Target    string     `json:"target"` // where it goes, without secrets
	Events    []string   `json:"events"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	LastAt    *time.Time `json:"last_at,omitempty"`
	LastError string     `json:"last_error,omitempty"`
	// Secret signs webhook deliveries; it's returned once, on creation.
	Secret string `json:"secret,omitempty"`
}

func (e *Engine) channelView(c *store.Channel) ChannelView {
	v := ChannelView{ID: c.ID, Name: c.Name, Kind: c.Kind, Target: hostOf(c), Events: c.Events, CreatedBy: c.CreatedBy, CreatedAt: c.CreatedAt}
	if c.Kind == "telegram" {
		v.Target = "Telegram chat " + c.Target.ChatID
	}
	n := e.notif()
	n.stMu.Lock()
	if d, ok := n.last[c.ID]; ok {
		at := d.at
		v.LastAt, v.LastError = &at, d.err
	}
	n.stMu.Unlock()
	return v
}

var (
	telegramToken = regexp.MustCompile(`^[0-9]{3,20}:[A-Za-z0-9_-]{30,100}$`)
	telegramChat  = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)
)

func checkChannelURL(kind, raw string, restrict bool) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil {
		return apierr.Invalid("url", "give the %s webhook URL", kind)
	}
	switch kind {
	case "slack":
		if u.Scheme != "https" || u.Host != "hooks.slack.com" || !strings.HasPrefix(u.Path, "/services/") {
			return apierr.Invalid("url", "a Slack webhook URL starts with https://hooks.slack.com/services/").
				WithHint("in Slack: Apps → Incoming Webhooks → Add New Webhook to Workspace")
		}
	case "discord":
		if u.Scheme != "https" || (u.Host != "discord.com" && u.Host != "discordapp.com") || !strings.HasPrefix(u.Path, "/api/webhooks/") {
			return apierr.Invalid("url", "a Discord webhook URL starts with https://discord.com/api/webhooks/").
				WithHint("in Discord: channel settings → Integrations → Webhooks → New Webhook → Copy Webhook URL")
		}
	case "webhook":
		if u.Scheme != "https" && (restrict || u.Scheme != "http") {
			if restrict {
				return apierr.Invalid("url", "webhook URLs must use https")
			}
			return apierr.Invalid("url", "webhook URLs must use http or https")
		}
	}
	return nil
}

func validEvents(org string, events []string) ([]string, error) {
	if len(events) == 0 {
		for _, k := range EventKinds {
			if k.Default && (!k.Server || org == store.DefaultOrg) {
				events = append(events, k.Kind)
			}
		}
		return events, nil
	}
	out := []string{}
	for _, ev := range events {
		i := slices.IndexFunc(EventKinds, func(k EventKind) bool { return k.Kind == ev })
		if i < 0 {
			return nil, apierr.Invalid("events", "unknown event %q", ev)
		}
		if EventKinds[i].Server && org != store.DefaultOrg {
			return nil, apierr.Invalid("events", "%s is about the server; only the default organization gets it", ev)
		}
		if !slices.Contains(out, ev) {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (e *Engine) ListChannels(ctx context.Context) ([]ChannelView, error) {
	chans, err := e.st.ListChannels(ctx, store.OrgOr(store.OrgFrom(ctx)))
	if err != nil {
		return nil, err
	}
	out := make([]ChannelView, len(chans))
	for i := range chans {
		out[i] = e.channelView(&chans[i])
	}
	return out, nil
}

func (e *Engine) orgChannel(ctx context.Context, id string) (*store.Channel, error) {
	c, err := e.st.GetChannel(ctx, id)
	if err != nil {
		return nil, err
	}
	if c == nil || c.Org != store.OrgOr(store.OrgFrom(ctx)) {
		return nil, apierr.NotFound("channel", id)
	}
	return c, nil
}

// SaveChannel creates a channel (id "") or changes one.
func (e *Engine) SaveChannel(ctx context.Context, actor, id string, req ChannelRequest) (*ChannelView, error) {
	org := store.OrgOr(store.OrgFrom(ctx))
	c := &store.Channel{ID: store.NewID("ch"), Org: org, Kind: req.Kind, CreatedBy: actor, CreatedAt: e.Now()}
	if id != "" {
		old, err := e.orgChannel(ctx, id)
		if err != nil {
			return nil, err
		}
		c = old
		if req.Kind != "" && req.Kind != c.Kind {
			return nil, apierr.Invalid("kind", "a channel's kind can't change; add a new channel")
		}
	}
	if strings.TrimSpace(req.Name) != "" {
		c.Name = strings.TrimSpace(req.Name)
	}
	if c.Name == "" {
		c.Name = c.Kind
	}
	if len(c.Name) > 60 {
		return nil, apierr.Invalid("name", "keep the name under 60 characters")
	}
	events, err := validEvents(org, req.Events)
	if err != nil {
		return nil, err
	}
	if id == "" || len(req.Events) > 0 {
		c.Events = events
	}
	switch c.Kind {
	case "slack", "discord", "webhook":
		if req.URL != "" || id == "" {
			if err := checkChannelURL(c.Kind, req.URL, restricted(org)); err != nil {
				return nil, err
			}
			c.Target.URL = strings.TrimSpace(req.URL)
		}
	case "telegram":
		if req.BotToken != "" || id == "" {
			if !telegramToken.MatchString(strings.TrimSpace(req.BotToken)) {
				return nil, apierr.Invalid("bot_token", "give the bot token @BotFather sent, like 123456:ABC…")
			}
			c.Target.BotToken = strings.TrimSpace(req.BotToken)
		}
		if req.ChatID != "" || id == "" {
			if !telegramChat.MatchString(strings.TrimSpace(req.ChatID)) {
				return nil, apierr.Invalid("chat_id", "give the chat's numeric id (groups start with -) or a public @channel").
					WithHint("add the bot to the chat, send it a message, then open https://api.telegram.org/bot<token>/getUpdates to find the chat id")
			}
			c.Target.ChatID = strings.TrimSpace(req.ChatID)
		}
	default:
		return nil, apierr.Invalid("kind", "kind must be slack, discord, telegram or webhook")
	}
	created := id == ""
	if created && c.Kind == "webhook" {
		b := make([]byte, 24)
		rand.Read(b)
		c.Target.Secret = "whsec_" + hex.EncodeToString(b)
	}
	if err := e.st.SaveChannel(ctx, c); err != nil {
		return nil, err
	}
	action := "channel.update"
	if created {
		action = "channel.create"
	}
	e.audit(ctx, actor, action, c.Name, "", map[string]any{"channel": c.ID, "kind": c.Kind, "events": c.Events})
	v := e.channelView(c)
	if created {
		v.Secret = c.Target.Secret
	}
	return &v, nil
}

func (e *Engine) DeleteChannel(ctx context.Context, actor, id string) error {
	c, err := e.orgChannel(ctx, id)
	if err != nil {
		return err
	}
	if err := e.st.DeleteChannel(ctx, id); err != nil {
		return err
	}
	e.audit(ctx, actor, "channel.delete", c.Name, "", map[string]any{"channel": id, "kind": c.Kind})
	return nil
}

// TestChannel sends a test message now and reports how it went.
func (e *Engine) TestChannel(ctx context.Context, id string) (*ChannelView, error) {
	c, err := e.orgChannel(ctx, id)
	if err != nil {
		return nil, err
	}
	ev := Event{Kind: "test", Org: c.Org, Severity: "info", Title: "Test message from Coldship",
		Detail: "Notifications reach this channel.", At: e.Now()}
	if b := e.notif().baseURL; b != "" {
		ev.Link = b + "/org/notifications"
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := e.send(ctx, c, ev); err != nil {
		return nil, apierr.New(502, "channel_unreachable", "the test message wasn't delivered: %v", err)
	}
	v := e.channelView(c)
	return &v, nil
}

// notifyDeploy tells the org how an applied plan went.
func (e *Engine) notifyDeploy(ctx context.Context, p *store.Plan, d *store.Deployment, actor string) {
	if p.Desired != nil && p.Desired.Stage == spec.StagePreview {
		return // a preview's first deploy has no app record to tell
	}
	if d.Status == store.DeployFailed {
		what := "deploy failed"
		if d.FailedIn == store.PhaseBuild {
			what = "build failed"
		}
		detail := d.Error
		if d.ErrorCode != "" {
			detail = d.ErrorCode + ": " + d.Error
		}
		if d.KeptPrevious {
			detail += "\nThe previous version is still running."
		}
		detail += "\nDeploy log: coldship deploy-log " + d.ID
		e.emit(ctx, Event{Kind: EvDeployFailed, Org: p.Org, App: p.App, Deployment: d.ID, Plan: p.ID, Severity: "error",
			Title: p.App + ": " + what, Detail: clipText(detail, 1500)})
		return
	}
	if p.Action == planner.OpDelete || p.Action == "noop" || d.Status != store.DeployRunning {
		return
	}
	e.emit(ctx, Event{Kind: EvDeploySucceeded, Org: p.Org, App: p.App, Deployment: d.ID, Plan: p.ID, Severity: "ok",
		Title: p.App + " deployed", Detail: "Deployed by " + actor + "."})
}
