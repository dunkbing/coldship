package engine

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dunkbing/coldship/internal/hostinfo"
	"github.com/dunkbing/coldship/internal/netguard"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

// inbox records what notification channels received.
type inbox struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
	got  chan struct{}
}

func newInbox() *inbox { return &inbox{got: make(chan struct{}, 100)} }

func (b *inbox) RoundTrip(r *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	b.reqs = append(b.reqs, r)
	b.body = append(b.body, string(raw))
	b.mu.Unlock()
	b.got <- struct{}{}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}}, nil
}

func (b *inbox) wait(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-b.got:
		case <-time.After(5 * time.Second):
			t.Fatalf("got %d of %d notifications", i, n)
		}
	}
}

func (b *inbox) none(t *testing.T) {
	t.Helper()
	select {
	case <-b.got:
		b.mu.Lock()
		defer b.mu.Unlock()
		t.Fatalf("unexpected notification: %s", b.body[len(b.body)-1])
	case <-time.After(150 * time.Millisecond):
	}
}

func (b *inbox) last() (*http.Request, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reqs[len(b.reqs)-1], b.body[len(b.body)-1]
}

func notifySetup(t *testing.T) (*Engine, *runtime.Fake, *inbox, context.Context) {
	t.Helper()
	e, rt := dbSetup(t)
	box := newInbox()
	e.notif().client = func(bool) *http.Client { return &http.Client{Transport: box} }
	e.SetPublicURL("https://coldship.example.com/")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.RunNotifications(ctx, discardLog())
	return e, rt, box, store.WithOrg(context.Background(), store.DefaultOrg)
}

func TestNotifications(t *testing.T) {
	e, rt, box, ctx := notifySetup(t)

	wh, err := e.SaveChannel(ctx, "admin", "", ChannelRequest{Kind: "webhook", Name: "ops", URL: "https://ops.example.com/hook"})
	if err != nil || !strings.HasPrefix(wh.Secret, "whsec_") || wh.Target != "ops.example.com" {
		t.Fatalf("webhook channel: %+v %v", wh, err)
	}
	if !contains(wh.Events, EvDeployFailed) || contains(wh.Events, EvDeploySucceeded) || !contains(wh.Events, EvDiskLow) {
		t.Fatalf("default events: %v", wh.Events)
	}
	list, _ := e.ListChannels(ctx)
	if len(list) != 1 || list[0].Secret != "" {
		t.Fatalf("the secret is shown once: %+v", list)
	}
	for _, bad := range []ChannelRequest{
		{Kind: "slack", URL: "https://example.com/services/x"},
		{Kind: "discord", URL: "https://evil.example/api/webhooks/1/x"},
		{Kind: "telegram", BotToken: "nope", ChatID: "1"},
		{Kind: "telegram", BotToken: "123456:" + strings.Repeat("a", 35), ChatID: "not a chat"},
		{Kind: "webhook", URL: "ftp://x"},
		{Kind: "pager"},
		{Kind: "webhook", URL: "https://x.example", Events: []string{"nope"}},
	} {
		if _, err := e.SaveChannel(ctx, "admin", "", bad); code(err) != "invalid_spec" && code(err) != "invalid_request" {
			t.Errorf("%+v: %v", bad, err)
		}
	}

	// A failed deploy is announced, signed.
	rt.FailNext = errors.New("container exited with code 1")
	p, _ := e.Plan(ctx, "agent", PlanRequest{Spec: &spec.App{Name: "web", Image: "web:1", Port: 80}})
	if _, err := e.Apply(ctx, "agent", p.ID, "ship"); err == nil {
		t.Fatal("want a failed deploy")
	}
	box.wait(t, 1)
	req, body := box.last()
	var ev Event
	json.Unmarshal([]byte(body), &ev)
	// Actor is the caller's token name: agents skip their own sessions' events by it.
	if ev.Kind != EvDeployFailed || ev.App != "web" || ev.Actor != "agent" || ev.Link != "https://coldship.example.com/apps/web" || !strings.Contains(ev.Detail, "exited") {
		t.Fatalf("event: %+v", ev)
	}
	mac := hmac.New(sha256.New, []byte(wh.Secret))
	mac.Write([]byte(req.Header.Get("X-Coldship-Timestamp") + "." + body))
	if req.Header.Get("X-Coldship-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || req.Header.Get("X-Coldship-Event") != EvDeployFailed {
		t.Fatalf("signature headers: %v", req.Header)
	}
	// Success isn't subscribed by default.
	deploy(t, e, &spec.App{Name: "web", Image: "web:1", Port: 80})
	box.none(t)

	// Slack: untrusted text can't become links or mentions.
	sl, err := e.SaveChannel(ctx, "admin", "", ChannelRequest{Kind: "slack", URL: "https://hooks.slack.com/services/T/B/x", Events: []string{EvDeployFailed}})
	if err != nil {
		t.Fatal(err)
	}
	e.DeleteChannel(ctx, "admin", wh.ID)
	e.emit(ctx, Event{Kind: EvDeployFailed, App: "web", Title: "web: deploy failed", Detail: "<!channel> <https://evil|click> & more"})
	box.wait(t, 1)
	_, body = box.last()
	if strings.Contains(body, "<!channel>") || !strings.Contains(body, "&lt;!channel&gt;") || !strings.Contains(body, "<https://coldship.example.com/apps/web|Open in Coldship>") {
		t.Fatalf("slack body: %s", body)
	}

	// Discord: no mentions; Telegram: plain text to the Bot API.
	e.DeleteChannel(ctx, "admin", sl.ID)
	dc, _ := e.SaveChannel(ctx, "admin", "", ChannelRequest{Kind: "discord", URL: "https://discord.com/api/webhooks/1/abc", Events: []string{EvDeployFailed}})
	e.emit(ctx, Event{Kind: EvDeployFailed, App: "web", Title: "t [x](https://evil)", Detail: "@everyone [Approve here](https://evil) ``` out"})
	box.wait(t, 1)
	if _, body = box.last(); !strings.Contains(body, `"allowed_mentions":{"parse":[]}`) || !strings.Contains(body, "```\\n@everyone [Approve") ||
		strings.Contains(body, "``` out") || !strings.Contains(body, `t \\[x\\]\\(https://evil\\)`) {
		t.Fatalf("discord body: %s", body)
	}
	e.DeleteChannel(ctx, "admin", dc.ID)
	tg, err := e.SaveChannel(ctx, "admin", "", ChannelRequest{Kind: "telegram", BotToken: "123456:" + strings.Repeat("A", 35), ChatID: "-100123", Events: []string{EvApprovalNeeded}})
	if err != nil || tg.Target != "Telegram chat -100123" || strings.Contains(tg.Target, "AAAA") {
		t.Fatalf("telegram: %+v %v", tg, err)
	}

	// Approval is asked once per plan, when an apply is refused.
	prod := &spec.App{Name: "web", Image: "web:1", Port: 80, Stage: spec.StageProduction}
	deploy(t, e, prod)
	pd, _ := e.Plan(ctx, "agent", PlanRequest{Delete: "web"})
	box.none(t) // planning alone says nothing
	for i := 0; i < 2; i++ {
		if _, err := e.Apply(ctx, "agent", pd.ID, "cleanup"); code(err) != "approval_required" {
			t.Fatalf("apply: %v", err)
		}
	}
	box.wait(t, 1)
	req, body = box.last()
	if !strings.HasPrefix(req.URL.String(), "https://api.telegram.org/bot123456:") || !strings.Contains(body, `"chat_id":"-100123"`) ||
		!strings.Contains(body, "coldship approve "+pd.ID) || strings.Contains(body, "parse_mode") {
		t.Fatalf("telegram request: %s %s", req.URL, body)
	}
	box.none(t)

	// Another org can't see or change this org's channels, nor get server events.
	other := store.WithOrg(context.Background(), "acme")
	if l, _ := e.ListChannels(other); len(l) != 0 {
		t.Fatalf("other org sees channels: %+v", l)
	}
	if err := e.DeleteChannel(other, "x", tg.ID); code(err) != "channel_not_found" {
		t.Fatalf("other org deletes: %v", err)
	}
	if _, err := e.SaveChannel(other, "x", "", ChannelRequest{Kind: "webhook", URL: "https://a.example", Events: []string{EvDiskLow}}); err == nil {
		t.Fatal("disk.low is for the default org only")
	}
	if _, err := e.SaveChannel(other, "x", "", ChannelRequest{Kind: "webhook", URL: "http://a.example"}); err == nil {
		t.Fatal("other orgs need https")
	}
}

func TestWatch(t *testing.T) {
	e, rt, box, ctx := notifySetup(t)
	if _, err := e.SaveChannel(ctx, "admin", "", ChannelRequest{Kind: "webhook", URL: "https://ops.example.com/hook"}); err != nil {
		t.Fatal(err)
	}
	deploy(t, e, &spec.App{Name: "api", Image: "api:1", Port: 80})
	disk := hostinfo.Disk{Path: "/", For: "Coldship's data", Usage: hostinfo.Usage{Total: 100, Used: 50}}
	e.DiskInfo = func() []hostinfo.Disk { return []hostinfo.Disk{disk} }

	// Down before the server started: the first check only records it.
	rt.Statuses = map[string]runtime.Status{"api": {Exists: true, State: "exited", ExitCode: 1}}
	e.watchOnce(ctx)
	e.watchOnce(ctx)
	e.watchOnce(ctx)
	box.none(t)
	// Recovers after two good checks, then goes down: one bad check isn't enough.
	delete(rt.Statuses, "api")
	e.watchOnce(ctx)
	box.none(t)
	e.watchOnce(ctx)
	box.wait(t, 1)
	if _, body := box.last(); !strings.Contains(body, `"event":"app.recovered"`) {
		t.Fatalf("recovered: %s", body)
	}
	rt.Statuses["api"] = runtime.Status{Exists: true, State: "exited", OOMKilled: true, RestartCount: 3}
	e.watchOnce(ctx)
	box.none(t)
	e.watchOnce(ctx)
	box.wait(t, 1)
	if _, body := box.last(); !strings.Contains(body, `"event":"app.down"`) || !strings.Contains(body, "out of memory") {
		t.Fatalf("down: %s", body)
	}
	e.watchOnce(ctx)
	box.none(t)

	// A crash loop looks running between restarts; the count gives it away.
	rt.Statuses["api"] = runtime.Status{Exists: true, State: "running", Running: true, RestartCount: 3}
	e.watchOnce(ctx)
	e.watchOnce(ctx)
	box.wait(t, 1) // recovered: running, restart count steady
	for _, n := range []int{5, 9} {
		rt.Statuses["api"] = runtime.Status{Exists: true, State: "running", Running: true, RestartCount: n}
		e.watchOnce(ctx)
	}
	box.wait(t, 1)
	if _, body := box.last(); !strings.Contains(body, `"event":"app.down"`) || !strings.Contains(body, "keeps crashing") {
		t.Fatalf("crash loop: %s", body)
	}

	rt.Statuses["api"] = runtime.Status{Exists: true, State: "exited"} // still down: no news

	// Disk: alert at 90%, once, and again only after it cleared.
	disk.Used = 93
	e.watchOnce(ctx)
	box.wait(t, 1)
	if _, body := box.last(); !strings.Contains(body, `"event":"disk.low"`) || !strings.Contains(body, "93%") {
		t.Fatalf("disk: %s", body)
	}
	e.watchOnce(ctx)
	disk.Used = 88 // between the thresholds: still low
	e.watchOnce(ctx)
	disk.Used = 91
	e.watchOnce(ctx)
	box.none(t)
	disk.Used = 50
	e.watchOnce(ctx)
	disk.Used = 95
	e.watchOnce(ctx)
	box.wait(t, 1)
}

func TestRestrictedChannelsStayOffLocalNetwork(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	if _, err := netguard.Client(true, 5*time.Second).Post(srv.URL, "application/json", nil); err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Fatalf("restricted client reached loopback: %v", err)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestPreviewsDontNotify(t *testing.T) {
	e, rt, box, ctx := notifySetup(t)
	if _, err := e.SaveChannel(ctx, "admin", "", ChannelRequest{Kind: "webhook", URL: "https://ops.example.com/hook", Events: []string{EvDeployFailed, EvDeploySucceeded}}); err != nil {
		t.Fatal(err)
	}
	deploy(t, e, &spec.App{Name: "img", Image: "nginx:1", Port: 80})
	box.wait(t, 1) // the app's own deploy
	rt.FailNext = errors.New("boom")
	if _, err := e.CreatePreview(ctx, "agent", PreviewRequest{App: "img", Image: "nginx:2", Name: "try", TTL: "1h"}); err == nil {
		t.Fatal("want the preview's deploy to fail")
	}
	box.none(t)
}
