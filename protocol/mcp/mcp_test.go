package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/agents/echo"
	"github.com/raven-clown/rosetta/internal/golden"
	"github.com/raven-clown/rosetta/protocol"
	"github.com/raven-clown/rosetta/protocol/protocoltest"
)

func connect(t *testing.T, c *client.Client) {
	t.Helper()
	ctx := t.Context()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "1"}
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
}

func inProcess(t *testing.T, reg protocol.Registry) *client.Client {
	t.Helper()
	srv := New(Options{}).newServer(t.Context(), reg, protocoltest.NewRunner())
	c, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatal(err)
	}
	connect(t, c)
	return c
}

func call(t *testing.T, c *client.Client, tool string, args map[string]any, progress any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = args
	if progress != nil {
		req.Params.Meta = &mcp.Meta{ProgressToken: progress}
	}
	res, err := c.CallTool(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestListTools(t *testing.T) {
	c := inProcess(t, protocoltest.StaticRegistry(echo.New("echo"), protocoltest.ScriptedAgent("other")))
	res, err := c.ListTools(t.Context(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 2 || res.Tools[0].Name != "echo" || res.Tools[0].Description != "Repeats what you send." {
		t.Fatalf("tools = %+v", res.Tools)
	}
	schema := res.Tools[0].InputSchema
	if _, ok := schema.Properties["message"]; !ok || len(schema.Required) != 1 || schema.Required[0] != "message" {
		t.Errorf("schema = %+v", schema)
	}
	if _, ok := schema.Properties["session_id"]; !ok {
		t.Errorf("schema has no session_id: %+v", schema)
	}
}

func TestCallTool(t *testing.T) {
	c := inProcess(t, protocoltest.StaticRegistry(echo.New("echo")))
	res := call(t, c, "echo", map[string]any{"message": "hello"}, nil)
	if res.IsError || text(res) != "hello" {
		t.Errorf("result = %+v", res)
	}
}

func TestProgressGolden(t *testing.T) {
	c := inProcess(t, protocoltest.StaticRegistry(protocoltest.ScriptedAgent("scripted", protocoltest.AllEvents()...)))
	notes := make(chan []byte, 16)
	c.OnNotification(func(n mcp.JSONRPCNotification) {
		if n.Method == "notifications/progress" {
			b, _ := json.Marshal(n)
			notes <- b
		}
	})
	res := call(t, c, "scripted", map[string]any{"message": "hi"}, "p1")
	got := map[float64][]byte{}
	for range 6 {
		select {
		case b := <-notes:
			var n struct {
				Params struct{ Progress float64 }
			}
			_ = json.Unmarshal(b, &n)
			got[n.Params.Progress] = b
		case <-time.After(5 * time.Second):
			t.Fatalf("got %d of 6 progress notifications", len(got))
		}
	}
	var lines bytes.Buffer
	for i := 1; i <= 6; i++ {
		lines.Write(append(got[float64(i)], '\n'))
	}
	b, _ := json.Marshal(res)
	lines.Write(append(b, '\n'))
	golden.Assert(t, "all_events", lines.Bytes())
}

func TestAgentErrorIsToolError(t *testing.T) {
	a := rosetta.Func(rosetta.Card{Name: "bad"}, func(context.Context, *rosetta.Request, rosetta.Stream) error {
		return errors.New("nope")
	})
	res := call(t, inProcess(t, protocoltest.StaticRegistry(a)), "bad", map[string]any{"message": "x"}, nil)
	if !res.IsError || text(res) != "nope" {
		t.Errorf("result = %+v", res)
	}
}

func TestSessionID(t *testing.T) {
	var seen []rosetta.Message
	a := rosetta.Func(rosetta.Card{Name: "a"}, func(_ context.Context, req *rosetta.Request, out rosetta.Stream) error {
		seen = req.History
		return out.Text("ok")
	})
	c := inProcess(t, protocoltest.StaticRegistry(a))
	call(t, c, "a", map[string]any{"message": "one", "session_id": "s1"}, nil)
	call(t, c, "a", map[string]any{"message": "two", "session_id": "s1"}, nil)
	if len(seen) != 2 || seen[0].Parts[0].Text != "one" {
		t.Errorf("history = %v", seen)
	}
}

func TestStreamableHTTP(t *testing.T) {
	mux := http.NewServeMux()
	if err := New(Options{}).Mount(mux, protocoltest.StaticRegistry(echo.New("echo")), protocoltest.NewRunner()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, err := client.NewStreamableHttpClient(srv.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	connect(t, c)
	if got := text(call(t, c, "echo", map[string]any{"message": "over http"}, nil)); got != "over http" {
		t.Errorf("text = %q", got)
	}
}

func TestStdio(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- New(Options{}).ServeStdio(ctx, protocoltest.StaticRegistry(echo.New("echo")), protocoltest.NewRunner(), inR, outW)
	}()
	c := client.NewClient(transport.NewIO(outR, inW, io.NopCloser(strings.NewReader(""))))
	connect(t, c)
	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil || len(res.Tools) != 1 {
		t.Fatalf("tools = %v, err = %v", res, err)
	}
	cancel()
	_ = inW.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeStdio did not return")
	}
}

func TestCancel(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	a := rosetta.Func(rosetta.Card{Name: "slow"}, func(ctx context.Context, _ *rosetta.Request, _ rosetta.Stream) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	})
	c := inProcess(t, protocoltest.StaticRegistry(a))
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-started
		cancel()
	}()
	req := mcp.CallToolRequest{}
	req.Params.Name = "slow"
	req.Params.Arguments = map[string]any{"message": "x"}
	_, _ = c.CallTool(ctx, req)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("agent was not cancelled")
	}
}

type changingRegistry struct {
	protocol.Registry
	ch chan struct{}
}

func (r changingRegistry) Changes() <-chan struct{} { return r.ch }

func TestListChanged(t *testing.T) {
	reg := changingRegistry{Registry: protocoltest.StaticRegistry(echo.New("echo")), ch: make(chan struct{})}
	c := inProcess(t, reg)
	got := make(chan string, 4)
	c.OnNotification(func(n mcp.JSONRPCNotification) { got <- n.Method })
	reg.ch <- struct{}{}
	select {
	case m := <-got:
		if m != "notifications/tools/list_changed" {
			t.Errorf("notification = %q", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no list_changed notification")
	}
}

func TestEmptyMessage(t *testing.T) {
	c := inProcess(t, protocoltest.StaticRegistry(echo.New("echo")))
	res := call(t, c, "echo", map[string]any{"message": ""}, nil)
	if !res.IsError || !strings.Contains(text(res), "no text in input") {
		t.Errorf("result = %+v", res)
	}
}

func TestExtensionContent(t *testing.T) {
	a := protocoltest.ScriptedAgent("img", rosetta.Extension{Protocol: "mcp", Value: mcp.NewImageContent("aGk=", "image/png")})
	res := call(t, inProcess(t, protocoltest.StaticRegistry(a)), "img", map[string]any{"message": "x"}, nil)
	if len(res.Content) != 2 {
		t.Fatalf("content = %+v", res.Content)
	}
	if _, ok := res.Content[1].(mcp.ImageContent); !ok {
		t.Errorf("content[1] = %T", res.Content[1])
	}
}
