package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/raven-clown/rosetta/config"
)

type served struct {
	addr  string
	lines []string
	stop  func() int
}

func startServe(t *testing.T, args ...string) *served {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	out := &lineWatcher{lines: make(chan string, 64)}
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, append([]string{"serve", "--listen", "127.0.0.1:0"}, args...), strings.NewReader(""), out, io.Discard)
	}()
	s := &served{}
	for s.addr == "" {
		select {
		case line := <-out.lines:
			s.lines = append(s.lines, line)
			var entry struct{ Msg, Addr string }
			if json.Unmarshal([]byte(line), &entry) == nil && entry.Msg == "listening" {
				s.addr = entry.Addr
			}
		case code := <-done:
			t.Fatalf("serve exited with %d", code)
		case <-time.After(5 * time.Second):
			t.Fatal("server never logged its address")
		}
	}
	stopped := false
	s.stop = func() int {
		if stopped {
			return 0
		}
		stopped = true
		cancel()
		select {
		case code := <-done:
			return code
		case <-time.After(15 * time.Second):
			t.Fatal("serve did not stop")
			return -1
		}
	}
	t.Cleanup(func() { s.stop() })
	return s
}

func get(t *testing.T, url, key, body string) (int, string) {
	t.Helper()
	method := http.MethodGet
	if body != "" {
		method = http.MethodPost
	}
	req, _ := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rosetta.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeModel(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"hi from model"}}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`+"\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestServeConfig(t *testing.T) {
	model := fakeModel(t)
	path := writeConfig(t, `
auth:
  api_keys:
    - { name: ci, key: "${TEST_ROSETTA_KEY}", role: member }
transponder:
  url: `+model.URL+`/v1
agents:
  - { name: helper, kind: llm, model: m }
`)
	t.Setenv("TEST_ROSETTA_KEY", "k1")
	s := startServe(t, "--config", path)
	base := "http://" + s.addr

	if code, _ := get(t, base+"/agui/helper", "", aguiInput); code != http.StatusUnauthorized {
		t.Errorf("no key: status %d", code)
	}
	code, body := get(t, base+"/agui/helper", "k1", aguiInput)
	if code != http.StatusOK || !strings.Contains(body, `"delta":"hi from model"`) {
		t.Errorf("run: status %d, body %s", code, body)
	}
	if code, body := get(t, base+"/readyz", "", ""); code != http.StatusOK || !strings.Contains(body, `"transponder":"ok"`) {
		t.Errorf("readyz: status %d, body %s", code, body)
	}
	if _, body := get(t, base+"/metrics", "", ""); !strings.Contains(body, `rosetta_runs_total{agent="helper",status="completed"} 1`) ||
		!strings.Contains(body, `rosetta_tokens_total{agent="helper",direction="output"} 3`) {
		t.Errorf("metrics: %s", body)
	}
	if code := s.stop(); code != 0 {
		t.Errorf("exit code %d", code)
	}
}

const aguiInput = `{"threadId":"t1","runId":"r1","messages":[{"id":"u1","role":"user","content":"hello"}],"tools":[],"context":[],"state":null}`

func TestServeConfigListenAndProtocols(t *testing.T) {
	path := writeConfig(t, `
listen_addr: "127.0.0.1:1"
auth: { disabled: true }
protocols:
  agui: { path: /ui }
  mcp: { enabled: false }
agents:
  - { name: e, kind: echo }
`)
	s := startServe(t, "--config", path)
	base := "http://" + s.addr
	if code, body := get(t, base+"/ui/e", "", aguiInput); code != http.StatusOK || !strings.Contains(body, "hello") {
		t.Errorf("moved agui: status %d, body %s", code, body)
	}
	if code, _ := get(t, base+"/mcp", "", `{}`); code != http.StatusNotFound {
		t.Errorf("disabled mcp: status %d", code)
	}
}

func TestServeWithoutConfigWarns(t *testing.T) {
	s := startServe(t)
	var warned bool
	for _, line := range s.lines {
		var entry struct{ Level, Msg string }
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Level == "WARN" && strings.Contains(entry.Msg, "auth") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no auth warning in %q", s.lines)
	}
	if code, _ := get(t, "http://"+s.addr+"/agui/echo", "", aguiInput); code != http.StatusOK {
		t.Errorf("echo without config: status %d", code)
	}
}

func TestBadConfig(t *testing.T) {
	tests := map[string]struct {
		args []string
		code int
		want string
	}{
		"missing file":     {[]string{"serve", "--config", filepath.Join(t.TempDir(), "nope.yaml")}, 1, "nope.yaml"},
		"invalid":          {[]string{"serve", "--config", writeConfig(t, "agents: [{name: a, kind: robot}]\nauth: {disabled: true}\n")}, 1, "agents[0].kind"},
		"stdio missing":    {[]string{"stdio", "--protocol", "acp", "--config", filepath.Join(t.TempDir(), "nope.yaml")}, 1, "nope.yaml"},
		"agent and config": {[]string{"stdio", "--protocol", "acp", "--config", writeConfig(t, "auth: {disabled: true}\nagents: [{name: a, kind: echo}]\n"), "--agent", "b"}, 2, `"b"`},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var stderr syncBuffer
			if code := run(t.Context(), tt.args, strings.NewReader(""), io.Discard, &stderr); code != tt.code {
				t.Errorf("exit code %d, want %d", code, tt.code)
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Errorf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestStdioConfigAgent(t *testing.T) {
	path := writeConfig(t, "auth: {disabled: true}\nagents: [{name: a, kind: echo}, {name: b, kind: echo}]\n")
	toAgentR, toAgentW := io.Pipe()
	toClientR, toClientW := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"stdio", "--protocol", "acp", "--config", path, "--agent", "b"}, toAgentR, toClientW, io.Discard)
	}()
	conn := acpsdk.NewClientSideConnection(quietClient{}, toAgentW, toClientR)
	res, err := conn.Initialize(t.Context(), acpsdk.InitializeRequest{ProtocolVersion: acpsdk.ProtocolVersionNumber})
	if err != nil || res.AgentInfo.Name != "b" {
		t.Errorf("initialize = %+v, %v", res, err)
	}
	cancel()
	_ = toAgentW.Close()
	<-done
}

func TestBuildAgents(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	cfg := &config.Config{
		Transponder: config.Upstream{URL: "http://t/v1"},
		Airlock:     config.Upstream{URL: "http://a/mcp"},
		Agents: []config.Agent{
			{Name: "e", Kind: "echo"},
			{Name: "l", Kind: "llm", Model: "m", Description: "Helps."},
			{Name: "r", Kind: "a2a", URL: closed.URL},
		},
	}
	agents, err := buildAgents(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range agents {
		names = append(names, a.Card().Name)
	}
	if strings.Join(names, ",") != "e,l,r" || agents[1].Card().Description != "Helps." {
		t.Errorf("agents = %v", names)
	}
	cfg.Agents = []config.Agent{{Name: "x", Kind: "robot"}}
	if _, err := buildAgents(t.Context(), cfg); err == nil {
		t.Error("want an error for an unknown kind")
	}
}
