// Package acp is a client for the Agent Client Protocol
// (https://agentclientprotocol.com): JSON-RPC 2.0 over an agent's stdin and
// stdout, one JSON message per line. Coldship is the client; the agent
// (Claude Code, Codex…) runs in a container.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

const ProtocolVersion = 1

// JSON-RPC error codes.
const (
	CodeMethodNotFound = -32601
	CodeAuthRequired   = -32000
)

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Handler answers the agent's requests (result or error) and receives its
// notifications (return values ignored). It runs on its own goroutine, so
// it may block, say on a person approving a tool call.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// Conn is a JSON-RPC connection to an agent. Reads and writes run in both
// directions at once: the agent asks for permission while answering a
// prompt.
type Conn struct {
	w       io.Writer
	wmu     sync.Mutex
	handler Handler
	ctx     context.Context
	cancel  context.CancelFunc

	mu      sync.Mutex
	next    int
	pending map[string]chan message
	err     error
	done    chan struct{}

	// Incoming messages are handled in order: notifications one at a
	// time (session updates must apply in sequence), requests each on
	// their own goroutine once their turn comes, so a request waiting on
	// a person never blocks what follows.
	qmu   sync.Mutex
	queue []message
	qcond *sync.Cond
}

// maxLine bounds one message; diffs and tool output can be large.
const maxLine = 64 << 20

// NewConn starts reading r. h handles the agent's requests and
// notifications.
func NewConn(r io.Reader, w io.Writer, h Handler) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{w: w, handler: h, ctx: ctx, cancel: cancel, pending: map[string]chan message{}, done: make(chan struct{})}
	c.qcond = sync.NewCond(&c.qmu)
	go c.read(r)
	go c.dispatch()
	return c
}

// Done is closed when the agent's output ends.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err is why the connection ended.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Conn) read(r io.Reader) {
	br := bufio.NewReaderSize(r, 1<<20)
	var err error
	for {
		var line []byte
		line, err = readLine(br)
		if err != nil {
			break
		}
		if len(line) == 0 {
			continue
		}
		var m message
		if json.Unmarshal(line, &m) != nil {
			continue // not a message (agents log to stderr, but be lenient)
		}
		switch {
		case m.Method != "":
			c.qmu.Lock()
			c.queue = append(c.queue, m)
			c.qcond.Signal()
			c.qmu.Unlock()
		case m.ID != nil:
			c.mu.Lock()
			ch := c.pending[string(m.ID)]
			delete(c.pending, string(m.ID))
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
	if errors.Is(err, io.EOF) {
		err = errors.New("the agent exited")
	}
	c.mu.Lock()
	c.err = err
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	c.cancel()
	close(c.done)
	c.qmu.Lock()
	c.qcond.Signal()
	c.qmu.Unlock()
}

func (c *Conn) dispatch() {
	for {
		c.qmu.Lock()
		for len(c.queue) == 0 {
			select {
			case <-c.done:
				c.qmu.Unlock()
				return
			default:
			}
			c.qcond.Wait()
		}
		m := c.queue[0]
		c.queue = c.queue[1:]
		c.qmu.Unlock()
		if m.ID == nil {
			c.handle(m)
		} else {
			go c.handle(m)
		}
	}
}

func readLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxLine {
			return nil, fmt.Errorf("agent message over %d bytes", maxLine)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if len(buf) > 0 && errors.Is(err, io.EOF) {
				return buf, nil
			}
			return nil, err
		}
		return buf[:len(buf)-1], nil
	}
}

func (c *Conn) handle(m message) {
	res, err := c.handler(c.ctx, m.Method, m.Params)
	if m.ID == nil {
		return // a notification
	}
	out := message{JSONRPC: "2.0", ID: m.ID}
	if err != nil {
		var re *RPCError
		if !errors.As(err, &re) {
			re = &RPCError{Code: -32603, Message: err.Error()}
		}
		out.Error = re
	} else {
		b, _ := json.Marshal(res)
		out.Result = b
	}
	c.send(out)
}

func (c *Conn) send(m message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(append(b, '\n'))
	return err
}

// Call sends a request and decodes its result into out (if non-nil).
func (c *Conn) Call(ctx context.Context, method string, params, out any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.next++
	id := json.RawMessage(strconv.Itoa(c.next))
	ch := make(chan message, 1)
	c.pending[string(id)] = ch
	c.mu.Unlock()
	if err := c.send(message{JSONRPC: "2.0", ID: id, Method: method, Params: p}); err != nil {
		return err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return c.Err()
		}
		if m.Error != nil {
			return m.Error
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, string(id))
		c.mu.Unlock()
		return ctx.Err()
	}
}

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.send(message{JSONRPC: "2.0", Method: method, Params: p})
}

// --- protocol types (the parts Coldship uses) ---

type InitializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         Implementation     `json:"clientInfo"`
}

// ClientCapabilities: Coldship offers no file system or terminal; agents
// work through Coldship's MCP tools.
type ClientCapabilities struct {
	FS       FSCapabilities `json:"fs"`
	Terminal bool           `json:"terminal"`
}

type FSCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

type InitializeResult struct {
	ProtocolVersion   int             `json:"protocolVersion"`
	AgentCapabilities json.RawMessage `json:"agentCapabilities,omitempty"`
	AgentInfo         *Implementation `json:"agentInfo,omitempty"`
	AuthMethods       []AuthMethod    `json:"authMethods,omitempty"`
}

type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"` // "" or "agent"; "terminal" can't be used here
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// MCPServer is a stdio MCP server the agent starts.
type MCPServer struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []EnvVar `json:"env"`
}

type NewSessionParams struct {
	Cwd        string      `json:"cwd"`
	MCPServers []MCPServer `json:"mcpServers"`
}

type NewSessionResult struct {
	SessionID     string         `json:"sessionId"`
	Modes         *ModeState     `json:"modes,omitempty"`
	ConfigOptions []ConfigOption `json:"configOptions,omitempty"`
}

// LoadSessionParams reopens an earlier session (agents with loadSession);
// the agent replays its history as session/update before answering.
type LoadSessionParams struct {
	SessionID  string      `json:"sessionId"`
	Cwd        string      `json:"cwd"`
	MCPServers []MCPServer `json:"mcpServers"`
}

type LoadSessionResult struct {
	Modes         *ModeState     `json:"modes,omitempty"`
	ConfigOptions []ConfigOption `json:"configOptions,omitempty"`
}

// AgentCapabilities: the parts Coldship uses.
type AgentCapabilities struct {
	LoadSession bool `json:"loadSession"`
}

// ModeState is the agent's session modes (like "ask" or "plan").
type ModeState struct {
	CurrentModeID  string `json:"currentModeId"`
	AvailableModes []Mode `json:"availableModes"`
}

type Mode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ConfigOption is a session setting the agent offers, like its model.
// Coldship advertises no boolean support, so these are selects.
type ConfigOption struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	Category     string          `json:"category,omitempty"`
	Type         string          `json:"type"`
	CurrentValue json.RawMessage `json:"currentValue"`
	Options      []ConfigValue   `json:"options,omitempty"`
}

// ConfigValue is a choice, or a group of choices (Group set).
type ConfigValue struct {
	Value       string        `json:"value,omitempty"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Group       string        `json:"group,omitempty"`
	Options     []ConfigValue `json:"options,omitempty"`
}

// Has reports whether v is one of the option's choices.
func (o ConfigOption) Has(v string) bool {
	var has func([]ConfigValue) bool
	has = func(list []ConfigValue) bool {
		for _, c := range list {
			if (c.Group == "" && c.Value == v) || (c.Group != "" && has(c.Options)) {
				return true
			}
		}
		return false
	}
	return has(o.Options)
}

// Command is a slash command the agent offers; it is invoked by sending
// "/name input" as a prompt.
type Command struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Input       *CommandInput `json:"input,omitempty"`
}

type CommandInput struct {
	Hint string `json:"hint"`
}

// Usage is how full the agent's context window is.
type Usage struct {
	Size int64 `json:"size"`
	Used int64 `json:"used"`
	Cost *Cost `json:"cost,omitempty"`
}

type Cost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type PromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

type PromptResult struct {
	StopReason string `json:"stopReason"`
}

type SessionNotification struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// Update is a session/update, flattened: which fields are set depends on
// SessionUpdate.
type Update struct {
	SessionUpdate string          `json:"sessionUpdate"`
	MessageID     string          `json:"messageId,omitempty"`
	Content       json.RawMessage `json:"content,omitempty"` // a block, or tool call content
	ToolCallID    string          `json:"toolCallId,omitempty"`
	Title         *string         `json:"title,omitempty"`
	Kind          string          `json:"kind,omitempty"`
	Status        string          `json:"status,omitempty"`
	RawInput      json.RawMessage `json:"rawInput,omitempty"`
	Entries       []PlanEntry     `json:"entries,omitempty"`

	// current_mode_update, config_option_update, available_commands_update,
	// usage_update (session_info_update uses Title).
	CurrentModeID     string         `json:"currentModeId,omitempty"`
	ConfigOptions     []ConfigOption `json:"configOptions,omitempty"`
	AvailableCommands []Command      `json:"availableCommands,omitempty"`
	Size              *int64         `json:"size,omitempty"`
	Used              *int64         `json:"used,omitempty"`
	Cost              *Cost          `json:"cost,omitempty"`
}

type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status"`
}

// ToolCallContent is one item of a tool call's content.
type ToolCallContent struct {
	Type    string        `json:"type"` // content | diff | terminal
	Content *ContentBlock `json:"content,omitempty"`
	Path    string        `json:"path,omitempty"`
	OldText *string       `json:"oldText,omitempty"`
	NewText string        `json:"newText,omitempty"`
}

type PermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  Update             `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // allow_once | allow_always | reject_once | reject_always
}

type PermissionOutcome struct {
	Outcome  string `json:"outcome"` // selected | cancelled
	OptionID string `json:"optionId,omitempty"`
}

type PermissionResult struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// ErrMethodNotFound answers methods Coldship doesn't offer.
func ErrMethodNotFound(method string) error {
	return &RPCError{Code: CodeMethodNotFound, Message: "method not found: " + method}
}
