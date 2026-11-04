package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/agents/echo"
)

const aguiBody = `{"threadId":"t1","runId":"r1","messages":[{"id":"u1","role":"user","content":"hi"}],"tools":[],"context":[],"state":null}`

const mcpInit = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestProtocolsMounted(t *testing.T) {
	tests := []struct {
		name    string
		disable func(*Protocols)
		method  string
		path    string
		body    string
	}{
		{"mcp", func(p *Protocols) { p.MCP.Disabled = true }, http.MethodPost, "/mcp", mcpInit},
		{"a2a", func(p *Protocols) { p.A2A.Disabled = true }, http.MethodGet, "/a2a/echo/.well-known/agent-card.json", ""},
		{"agui", func(p *Protocols) { p.AGUI.Disabled = true }, http.MethodPost, "/agui/echo", aguiBody},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			on := New(Options{}, echo.New("echo")).Handler()
			if rec := do(t, on, tt.method, tt.path, tt.body); rec.Code != http.StatusOK {
				t.Errorf("enabled: status %d, body %s", rec.Code, rec.Body.String())
			}
			var opts Options
			tt.disable(&opts.Protocols)
			off := New(opts, echo.New("echo")).Handler()
			if rec := do(t, off, tt.method, tt.path, tt.body); rec.Code != http.StatusNotFound {
				t.Errorf("disabled: status %d", rec.Code)
			}
		})
	}
}

func TestCustomPath(t *testing.T) {
	h := New(Options{Protocols: Protocols{AGUI: Toggle{Path: "/ui"}}}, echo.New("echo")).Handler()
	if rec := do(t, h, http.MethodPost, "/ui/echo", aguiBody); rec.Code != http.StatusOK {
		t.Errorf("status %d", rec.Code)
	}
}

func TestHealthz(t *testing.T) {
	rec := do(t, New(Options{}, echo.New("echo")).Handler(), http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("status %d, body %q", rec.Code, rec.Body.String())
	}
}

func TestDefaultStore(t *testing.T) {
	var seen []rosetta.Message
	a := rosetta.Func(rosetta.Card{Name: "a"}, func(_ context.Context, req *rosetta.Request, out rosetta.Stream) error {
		seen = req.History
		return out.Text("ok")
	})
	srv := httptest.NewServer(New(Options{}, a).Handler())
	defer srv.Close()
	c, err := client.NewStreamableHttpClient(srv.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "t", Version: "1"}
	if err := c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Initialize(t.Context(), init); err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"one", "two"} {
		req := mcp.CallToolRequest{}
		req.Params.Name = "a"
		req.Params.Arguments = map[string]any{"message": msg, "session_id": "s1"}
		if _, err := c.CallTool(t.Context(), req); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 {
		t.Errorf("history = %v", seen)
	}
}

type quietClient struct{ acpsdk.Client }

func TestServeStdioACP(t *testing.T) {
	toAgentR, toAgentW := io.Pipe()
	toClientR, toClientW := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- ServeStdio(ctx, ACP, toAgentR, toClientW, Options{}, echo.New("echo")) }()
	conn := acpsdk.NewClientSideConnection(quietClient{}, toAgentW, toClientR)
	res, err := conn.Initialize(t.Context(), acpsdk.InitializeRequest{ProtocolVersion: acpsdk.ProtocolVersionNumber})
	if err != nil || res.AgentInfo.Name != "echo" {
		t.Fatalf("initialize = %+v, %v", res, err)
	}
	cancel()
	_ = toAgentW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeStdio did not return")
	}
}

func TestServeStdioUnknown(t *testing.T) {
	err := ServeStdio(t.Context(), "x", strings.NewReader(""), io.Discard, Options{}, echo.New("echo"))
	if err == nil || !strings.Contains(err.Error(), `"x"`) {
		t.Errorf("err = %v", err)
	}
}

func TestNewPanicsOnBadAgents(t *testing.T) {
	tests := []struct {
		name   string
		agents []rosetta.Agent
		want   string
	}{
		{"duplicate", []rosetta.Agent{echo.New("echo"), echo.New("echo")}, "echo"},
		{"bad name", []rosetta.Agent{echo.New("Bad Name")}, "Bad Name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				p := recover()
				if p == nil || !strings.Contains(fmt.Sprint(p), tt.want) {
					t.Errorf("panic = %v", p)
				}
			}()
			New(Options{}, tt.agents...)
		})
	}
}
