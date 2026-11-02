package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dunkbing/coldship/internal/acp"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/secrets"
	"github.com/dunkbing/coldship/internal/store"
)

// fakeLauncher runs a fake ACP agent in-process, over pipes.
type fakeLauncher struct {
	mu    sync.Mutex
	specs []runtime.AttachSpec
	agent *fakeAgent
}

func (f *fakeLauncher) RunAttached(_ context.Context, s runtime.AttachSpec, _ io.Writer) (*runtime.Attached, error) {
	f.mu.Lock()
	f.specs = append(f.specs, s)
	f.mu.Unlock()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	a := &fakeAgent{in: inR, out: outW, pending: map[string]chan json.RawMessage{}, cancel: make(chan struct{}, 1), exited: make(chan struct{})}
	f.mu.Lock()
	f.agent = a
	f.mu.Unlock()
	go func() { io.WriteString(errW, "npm: downloading the adapter\n") }()
	go a.serve()
	var once sync.Once
	kill := func() {
		once.Do(func() {
			inW.Close()
			outW.Close()
			errW.Close()
		})
	}
	return &runtime.Attached{Stdin: inW, Stdout: outR, Stderr: errR, Wait: func() error { <-a.exited; return nil }, Kill: kill}, nil
}

func (f *fakeLauncher) RemoveLabeled(context.Context, string) error { return nil }

type fakeAgent struct {
	in      io.Reader
	out     io.Writer
	wmu     sync.Mutex
	authed  bool
	mcp     []acp.MCPServer
	pending map[string]chan json.RawMessage
	pmu     sync.Mutex
	next    int
	cancel  chan struct{}
	exited  chan struct{}
	fsErr   int
}

type rpc struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acp.RPCError   `json:"error,omitempty"`
}

func (a *fakeAgent) send(m rpc) {
	m.JSONRPC = "2.0"
	b, _ := json.Marshal(m)
	a.wmu.Lock()
	a.out.Write(append(b, '\n'))
	a.wmu.Unlock()
}

func (a *fakeAgent) reply(id json.RawMessage, v any) {
	b, _ := json.Marshal(v)
	a.send(rpc{ID: id, Result: b})
}

func (a *fakeAgent) update(u map[string]any) {
	p, _ := json.Marshal(map[string]any{"sessionId": "s1", "update": u})
	a.send(rpc{Method: "session/update", Params: p})
}

// call sends a request to the client and waits for its answer.
func (a *fakeAgent) call(method string, params any) rpc {
	a.pmu.Lock()
	a.next++
	id := json.RawMessage(`"agent-` + string(rune('0'+a.next)) + `"`)
	ch := make(chan json.RawMessage, 1)
	a.pending[string(id)] = ch
	a.pmu.Unlock()
	p, _ := json.Marshal(params)
	a.send(rpc{ID: id, Method: method, Params: p})
	var m rpc
	json.Unmarshal(<-ch, &m)
	return m
}

func (a *fakeAgent) serve() {
	defer close(a.exited)
	br := bufio.NewReader(a.in)
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			return
		}
		var m rpc
		json.Unmarshal(line, &m)
		if m.Method == "" {
			a.pmu.Lock()
			ch := a.pending[string(m.ID)]
			a.pmu.Unlock()
			if ch != nil {
				ch <- line
			}
			continue
		}
		go a.handle(m)
	}
}

func (a *fakeAgent) handle(m rpc) {
	switch m.Method {
	case "initialize":
		a.reply(m.ID, map[string]any{"protocolVersion": 1, "agentInfo": map[string]string{"name": "fake", "version": "1.0"},
			"authMethods": []map[string]string{{"id": "browser-login", "name": "Log in"}, {"id": "api-key", "name": "API key"}}})
	case "authenticate":
		var p struct{ MethodID string }
		json.Unmarshal(m.Params, &p)
		a.authed = p.MethodID == "api-key"
		a.reply(m.ID, map[string]any{})
	case "session/new":
		if !a.authed {
			a.send(rpc{ID: m.ID, Error: &acp.RPCError{Code: acp.CodeAuthRequired, Message: "auth required"}})
			return
		}
		var p acp.NewSessionParams
		json.Unmarshal(m.Params, &p)
		a.mcp = p.MCPServers
		a.reply(m.ID, map[string]string{"sessionId": "s1"})
	case "session/cancel":
		a.cancel <- struct{}{}
	case "session/prompt":
		var p acp.PromptParams
		json.Unmarshal(m.Params, &p)
		if p.Prompt[0].Text == "wait" {
			res := a.call("session/request_permission", map[string]any{"sessionId": "s1", "toolCall": map[string]any{"toolCallId": "t9", "title": "Delete everything"},
				"options": []map[string]string{{"optionId": "ok", "name": "Allow", "kind": "allow_once"}}})
			<-a.cancel
			a.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "outcome " + string(res.Result)}})
			a.reply(m.ID, map[string]string{"stopReason": "cancelled"})
			return
		}
		a.update(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "m1", "content": map[string]string{"type": "text", "text": "Hello "}})
		a.update(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "m1", "content": map[string]string{"type": "text", "text": "world"}})
		a.update(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]string{"type": "text", "text": strings.Repeat("x", 100<<10)}})
		a.update(map[string]any{"sessionUpdate": "plan", "entries": []map[string]string{{"content": "check logs", "status": "in_progress"}}})
		a.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "coldship_logs", "kind": "read", "status": "pending",
			"rawInput": map[string]string{"app": "web"}})
		if r := a.call("fs/read_text_file", map[string]string{"sessionId": "s1", "path": "/etc/passwd"}); r.Error != nil {
			a.fsErr = r.Error.Code
		}
		res := a.call("session/request_permission", map[string]any{"sessionId": "s1", "toolCall": map[string]any{"toolCallId": "t1"},
			"options": []map[string]string{{"optionId": "yes", "name": "Allow", "kind": "allow_once"}, {"optionId": "no", "name": "Reject", "kind": "reject_once"}}})
		var out struct{ Outcome acp.PermissionOutcome }
		json.Unmarshal(res.Result, &out)
		a.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "completed",
			"content": []map[string]any{{"type": "content", "content": map[string]string{"type": "text", "text": "chose " + out.Outcome.OptionID}}}})
		a.reply(m.ID, map[string]string{"stopReason": "end_turn"})
	}
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSession(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"), secrets.New())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fl := &fakeLauncher{}
	m := New(st, fl, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.SocketDir, m.Binary = "/var/lib/coldship/agent-run", "/usr/local/bin/coldship"

	if _, err := m.Save(ctx, "default", "dana", "helper", ConfigRequest{Kind: "claude"}); err == nil {
		t.Fatal("claude without a key")
	}
	if _, err := m.Save(ctx, "default", "dana", "helper", ConfigRequest{Kind: "claude", Scope: "admin", Env: map[string]string{"ANTHROPIC_API_KEY": "k"}}); err == nil {
		t.Fatal("an admin agent")
	}
	v, err := m.Save(ctx, "default", "dana", "helper", ConfigRequest{Kind: "claude", Env: map[string]string{"ANTHROPIC_API_KEY": "sk-secret"}, Apps: []string{"agent-*"}})
	if err != nil || v.Scope != store.ScopeDeploy || len(v.Keys) != 1 {
		t.Fatalf("%+v %v", v, err)
	}
	// Saving again without the key keeps it.
	if _, err := m.Save(ctx, "default", "dana", "helper", ConfigRequest{Kind: "claude", Env: map[string]string{"ANTHROPIC_API_KEY": ""}, Apps: []string{"agent-*"}}); err != nil {
		t.Fatal(err)
	}

	// A viewer starting it gives the agent read rights only.
	sv, err := m.StartSession(ctx, StartRequest{Org: "default", Agent: "helper", Actor: "dana", Caller: store.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := m.Session("default", sv.ID)
	waitFor(t, "ready", func() bool { return s.View(false).Status == StatusReady })
	if _, err := m.Session("acme", sv.ID); err == nil {
		t.Fatal("another org sees the session")
	}
	spec := fl.specs[0]
	if !strings.Contains(strings.Join(spec.Env, " "), "ANTHROPIC_API_KEY=sk-secret") || strings.Contains(strings.Join(spec.Cmd, " "), "sk-secret") {
		t.Fatalf("key handling: %+v", spec)
	}
	a := fl.agent
	var tok string
	for _, e := range a.mcp[0].Env {
		if e.Name == "COLDSHIP_TOKEN" {
			tok = e.Value
		}
	}
	tt, _ := st.LookupToken(ctx, tok)
	if tt == nil || tt.Scope != store.ScopeRead || tt.Org != "default" || strings.Join(tt.Apps, ",") != "agent-*" {
		t.Fatalf("agent token: %+v", tt)
	}

	events, unsub := s.Subscribe()
	defer unsub()
	if e := <-events; e.Type != "snapshot" {
		t.Fatalf("first event: %+v", e)
	}
	if err := s.Prompt("dana", "check web"); err != nil {
		t.Fatal(err)
	}
	if err := s.Prompt("dana", "again"); err == nil {
		t.Fatal("prompt while working")
	}
	var permID string
	waitFor(t, "permission", func() bool {
		for _, it := range s.View(true).Items {
			if it.Kind == "permission" && it.Permission.Status == "pending" {
				permID = it.Permission.ID
				return true
			}
		}
		return false
	})
	if err := s.Answer("dana", permID, "nope"); err == nil {
		t.Fatal("unknown option")
	}
	if err := s.Answer("dana", permID, "yes"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "turn end", func() bool { return s.View(false).Status == StatusReady })
	items := s.View(true).Items
	var text, tool, perm string
	var thought int
	for _, it := range items {
		switch it.Kind {
		case "agent":
			text += it.Text
		case "thought":
			thought = len(it.Text)
		case "tool":
			tool = it.Tool.Title + "|" + it.Tool.Status + "|" + it.Tool.Output + "|" + it.Tool.Input
		case "permission":
			perm = it.Permission.Status + ":" + it.Permission.Chosen + ":" + it.Permission.Title
		}
	}
	if text != "Hello world" || thought < 100<<10 || !strings.Contains(tool, "coldship_logs|completed|chose yes|") || !strings.Contains(tool, `"app": "web"`) ||
		perm != "answered:yes:coldship_logs" || a.fsErr != acp.CodeMethodNotFound {
		t.Fatalf("transcript: text=%q thought=%d tool=%q perm=%q fs=%d", text, thought, tool, perm, a.fsErr)
	}

	// Cancelling answers a pending permission request with cancelled.
	if err := s.Prompt("dana", "wait"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second permission", func() bool {
		for _, it := range s.View(true).Items {
			if it.Kind == "permission" && it.Permission.Status == "pending" {
				return true
			}
		}
		return false
	})
	s.Cancel()
	waitFor(t, "cancelled", func() bool {
		v := s.View(true)
		last := v.Items[len(v.Items)-1]
		return v.Status == StatusReady && strings.Contains(last.Text, "cancelled")
	})

	// Ending the session revokes the token.
	s.Stop("dana")
	if tt, _ := st.LookupToken(ctx, tok); tt != nil {
		t.Fatal("token outlived the session")
	}
	if s.View(false).Status != StatusEnded {
		t.Fatal("not ended")
	}
}
