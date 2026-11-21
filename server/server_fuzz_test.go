package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/raven-clown/rosetta/agents/echo"
)

var fuzzPaths = []string{"/mcp", "/a2a/echo", "/agui/echo", "/a2a/echo/v1/message:send", "/a2a/echo/v1/message:stream"}

// FuzzHTTP posts any body to each protocol endpoint. Every request must get an answer, and none may crash the server.
func FuzzHTTP(f *testing.F) {
	f.Add(uint8(0), []byte(mcpInit))
	f.Add(uint8(0), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"input":"hi"}}}`))
	f.Add(uint8(1), []byte(`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hi"}]}}}`))
	f.Add(uint8(1), []byte(`{"jsonrpc":"2.0","id":1,"method":"SendStreamingMessage","params":{}}`))
	f.Add(uint8(2), []byte(aguiBody))
	f.Add(uint8(3), []byte(`{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hi"}]}}`))
	f.Add(uint8(4), []byte(`[]`))
	h := New(Options{}, echo.New("echo")).Handler()
	f.Fuzz(func(t *testing.T, path uint8, body []byte) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, fuzzPaths[int(path)%len(fuzzPaths)], bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if ctx.Err() != nil {
			t.Fatalf("POST %s %q did not finish", req.URL.Path, body)
		}
	})
}

// FuzzServeStdio sends any bytes to the ACP and MCP stdio servers. Each must return once its input ends.
func FuzzServeStdio(f *testing.F) {
	f.Add(true, []byte(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1}}`+"\n"))
	f.Add(true, []byte(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/","mcpServers":[]}}`+"\n"+`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"x","prompt":[{"type":"text","text":"hi"}]}}`+"\n"))
	f.Add(false, []byte(mcpInit+"\n"+`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"+`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"input":"hi"}}}`+"\n"))
	f.Add(false, []byte("{\n\x00"))
	f.Fuzz(func(t *testing.T, acp bool, in []byte) {
		p := MCP
		if acp {
			p = ACP
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = ServeStdio(ctx, p, bytes.NewReader(in), io.Discard, Options{}, echo.New("echo"))
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("ServeStdio(%s, %q) did not return after its input ended", p, in)
		}
	})
}
