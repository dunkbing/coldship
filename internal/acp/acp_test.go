package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

// A response must not overtake the notifications sent before it: a
// prompt's answer comes after its last message chunk, and session/load's
// after the history it replays.
func TestResponseWaitsForEarlierNotifications(t *testing.T) {
	inR, inW := io.Pipe()   // client -> agent
	outR, outW := io.Pipe() // agent -> client
	release := make(chan struct{})
	handled := make(chan struct{})
	c := NewConn(outR, inW, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		<-release // a slow update
		close(handled)
		return nil, nil
	})
	go func() {
		br := bufio.NewReader(inR)
		line, _ := br.ReadBytes('\n')
		var req struct{ ID json.RawMessage }
		json.Unmarshal(line, &req)
		io.WriteString(outW, `{"jsonrpc":"2.0","method":"session/update","params":{}}`+"\n")
		io.WriteString(outW, `{"jsonrpc":"2.0","id":`+string(req.ID)+`,"result":{}}`+"\n")
	}()
	done := make(chan error, 1)
	go func() { done <- c.Call(context.Background(), "session/load", map[string]string{}, nil) }()
	select {
	case <-done:
		t.Fatal("the response arrived before the update before it was applied")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	<-handled
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	outW.Close()
	<-c.Done()
	// Calls after the agent exited fail at once.
	if err := c.Call(context.Background(), "x", nil, nil); err == nil {
		t.Fatal("call after exit")
	}
}
