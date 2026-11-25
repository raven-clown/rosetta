package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestUsageErrors(t *testing.T) {
	tests := map[string][]string{
		"no args":         nil,
		"unknown command": {"fly"},
		"stdio no proto":  {"stdio"},
		"stdio bad proto": {"stdio", "--protocol", "x"},
		"bad flag":        {"serve", "--nope"},
		"bad agent name":  {"serve", "--agent", "Bad Name"},
		"check bad flag":  {"check", "--nope"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			var stderr syncBuffer
			code := run(t.Context(), args, strings.NewReader(""), io.Discard, &stderr)
			if code != 2 {
				t.Errorf("exit code %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), "usage") && !strings.Contains(stderr.String(), "Usage") {
				t.Errorf("stderr = %q", stderr.String())
			}
		})
	}
}

type lineWatcher struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	lines chan string
}

func (w *lineWatcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			w.buf.WriteString(line)
			return len(p), nil
		}
		select {
		case w.lines <- line:
		default:
		}
	}
}

// inTemp moves the test into an empty directory, where rosetta.db is made.
func inTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

func TestServe(t *testing.T) {
	inTemp(t)
	ctx, cancel := context.WithCancel(t.Context())
	out := &lineWatcher{lines: make(chan string, 16)}
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--listen", "127.0.0.1:0"}, strings.NewReader(""), out, io.Discard)
	}()
	var addr string
	for addr == "" {
		select {
		case line := <-out.lines:
			var entry struct{ Msg, Addr string }
			if json.Unmarshal([]byte(line), &entry) == nil && entry.Msg == "listening" {
				addr = entry.Addr
			}
		case <-time.After(5 * time.Second):
			t.Fatal("server never logged its address")
		}
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status %d", resp.StatusCode)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code %d", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
}

type quietClient struct{ acpsdk.Client }

func (quietClient) SessionUpdate(context.Context, acpsdk.SessionNotification) error { return nil }

func TestStdioACP(t *testing.T) {
	toAgentR, toAgentW := io.Pipe()
	toClientR, toClientW := io.Pipe()
	var stderr syncBuffer
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"stdio", "--protocol", "acp"}, toAgentR, toClientW, &stderr) }()
	raw := &bytes.Buffer{}
	conn := acpsdk.NewClientSideConnection(quietClient{}, toAgentW, io.TeeReader(toClientR, raw))
	res, err := conn.Initialize(t.Context(), acpsdk.InitializeRequest{ProtocolVersion: acpsdk.ProtocolVersionNumber})
	if err != nil || res.AgentInfo.Name != "echo" {
		t.Fatalf("initialize = %+v, %v", res, err)
	}
	cancel()
	_ = toAgentW.Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stdio did not stop")
	}
	sc := bufio.NewScanner(bytes.NewReader(raw.Bytes()))
	for sc.Scan() {
		var frame struct{ JSONRPC string }
		if json.Unmarshal(sc.Bytes(), &frame) != nil || frame.JSONRPC != "2.0" {
			t.Errorf("stdout carried a non-protocol line: %q", sc.Text())
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		if line != "" && !json.Valid([]byte(line)) {
			t.Errorf("stderr line is not JSON: %q", line)
		}
	}
}

func TestCheck(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		path := writeConfig(t, `
transponder:
  url: http://127.0.0.1:9/v1
agents:
  - name: support
    kind: llm
    model: fast
    tools: ["github__*"]
  - name: research
    kind: a2a
    url: https://agents.example.com/research
`)
		var stdout, stderr bytes.Buffer
		code := run(t.Context(), []string{"check", "--config", path}, strings.NewReader(""), &stdout, &stderr)
		if code != 0 {
			t.Fatalf("exit %d, stderr %s", code, stderr.String())
		}
		want := "ok  " + path + "\n" +
			"agent  support   llm   model=fast tools=github__*\n" +
			"agent  research  a2a   url=https://agents.example.com/research\n"
		if stdout.String() != want {
			t.Errorf("stdout:\n%s\nwant:\n%s", stdout.String(), want)
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr %q", stderr.String())
		}
	})

	t.Run("invalid", func(t *testing.T) {
		path := writeConfig(t, "agents: [{name: helper, kind: nope}]\n")
		var stdout, stderr bytes.Buffer
		code := run(t.Context(), []string{"check", "--config", path}, strings.NewReader(""), &stdout, &stderr)
		if code != 1 {
			t.Fatalf("exit %d, stderr %s", code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout %q", stdout.String())
		}
		if !strings.Contains(stderr.String(), "agents[0].kind") {
			t.Errorf("stderr %q", stderr.String())
		}
	})

	t.Run("missing config", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(t.Context(), []string{"check"}, strings.NewReader(""), &stdout, &stderr)
		if code != 2 {
			t.Fatalf("exit %d, stderr %s", code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout %q", stdout.String())
		}
		if !strings.Contains(stderr.String(), "--config is required") || !strings.Contains(stderr.String(), "usage:") {
			t.Errorf("stderr %q", stderr.String())
		}
	})

	t.Run("api key is not printed", func(t *testing.T) {
		const secret = "check-secret-value"
		path := writeConfig(t, fmt.Sprintf(`
auth:
  api_keys:
    - { name: local, key: %q, role: admin }
transponder:
  url: http://127.0.0.1:9/v1
  api_key: %q
airlock:
  url: http://127.0.0.1:9/mcp
  api_key: %q
agents:
  - name: support
    kind: llm
    model: fast
    system_prompt: %q
    tools: ["github__*", "slack__*"]
    max_steps: 4
  - name: research
    kind: a2a
    url: https://agents.example.com/research
    forward_token: true
  - name: plain
    kind: llm
    model: fast
  - name: echo
    kind: echo
`, secret, secret, secret, secret))
		var stdout, stderr bytes.Buffer
		code := run(t.Context(), []string{"check", "--config", path}, strings.NewReader(""), &stdout, &stderr)
		if code != 0 {
			t.Fatalf("exit %d, stderr %s", code, stderr.String())
		}
		want := "ok  " + path + "\n" +
			"agent  support   llm   model=fast tools=github__*,slack__*\n" +
			"agent  research  a2a   url=https://agents.example.com/research\n" +
			"agent  plain     llm   model=fast\n" +
			"agent  echo      echo\n"
		if stdout.String() != want {
			t.Errorf("stdout:\n%s\nwant:\n%s", stdout.String(), want)
		}
		if strings.Contains(stdout.String()+stderr.String(), secret) {
			t.Errorf("secret leaked:\nstdout %s\nstderr %s", stdout.String(), stderr.String())
		}
	})
}
