package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/raven-clown/rosetta/config"
	"github.com/raven-clown/rosetta/internal/auth"
	"github.com/raven-clown/rosetta/store"
)

type served struct {
	addr   string
	lines  []string
	stderr *syncBuffer
	stop   func() int
}

func startServe(t *testing.T, args ...string) *served {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	out := &lineWatcher{lines: make(chan string, 64)}
	done := make(chan int, 1)
	s := &served{stderr: &syncBuffer{}}
	go func() {
		done <- run(ctx, append([]string{"serve", "--listen", "127.0.0.1:0"}, args...), strings.NewReader(""), out, s.stderr)
	}()
	for s.addr == "" {
		select {
		case line := <-out.lines:
			s.lines = append(s.lines, line)
			var entry struct{ Msg, Addr string }
			if json.Unmarshal([]byte(line), &entry) == nil && entry.Msg == "listening" {
				s.addr = entry.Addr
			}
		case code := <-done:
			t.Fatalf("serve exited with %d: %s", code, s.stderr.String())
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
		if r.Method == http.MethodGet {
			return
		}
		if r.Header.Get("Authorization") != "Bearer t-key" {
			http.Error(w, "wrong key "+r.Header.Get("Authorization"), http.StatusUnauthorized)
			return
		}
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
	inTemp(t)
	model := fakeModel(t)
	path := writeConfig(t, `
auth:
  api_keys:
    - { name: ci, key: "${TEST_ROSETTA_KEY}", role: member }
transponder:
  url: `+model.URL+`/v1
  api_key: t-key
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
	inTemp(t)
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

var keyLine = regexp.MustCompile(`rsk_[A-Za-z0-9_-]{43}`)

func TestServeWithoutConfig(t *testing.T) {
	dir := inTemp(t)
	s := startServe(t)
	key := keyLine.FindString(s.stderr.String())
	if key == "" {
		t.Fatalf("no admin key on stderr: %q", s.stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "rosetta.db")); err != nil {
		t.Errorf("no rosetta.db in the working directory: %v", err)
	}
	base := "http://" + s.addr
	if code, _ := get(t, base+"/agui/echo", "", aguiInput); code != http.StatusUnauthorized {
		t.Errorf("echo without a key: status %d", code)
	}
	if code, body := get(t, base+"/agui/echo", key, aguiInput); code != http.StatusOK || !strings.Contains(body, "hello") {
		t.Errorf("echo with the key: status %d, body %s", code, body)
	}
	if code, body := get(t, base+"/admin/v1/agents/echo", key, ""); code != http.StatusOK || !strings.Contains(body, `"managed_by_code":true`) {
		t.Errorf("admin: status %d, body %s", code, body)
	}
	s.stop()

	again := startServe(t)
	if strings.Contains(again.stderr.String(), "rsk_") {
		t.Errorf("a second start printed a key: %q", again.stderr.String())
	}
	if code, _ := get(t, "http://"+again.addr+"/agui/echo", key, aguiInput); code != http.StatusOK {
		t.Errorf("first key after restart: status %d", code)
	}
}

func TestBootstrapKeyFromEnv(t *testing.T) {
	inTemp(t)
	const chosen = "chosen-admin-key-of-thirty-two-chars"
	t.Setenv("ROSETTA_BOOTSTRAP_ADMIN_KEY", chosen)
	s := startServe(t)
	if strings.Contains(s.stderr.String(), chosen) || !strings.Contains(s.stderr.String(), "ROSETTA_BOOTSTRAP_ADMIN_KEY") {
		t.Errorf("stderr = %q", s.stderr.String())
	}
	if code, _ := get(t, "http://"+s.addr+"/admin/v1/status", chosen, ""); code != http.StatusOK {
		t.Errorf("status with the chosen key: %d", code)
	}
}

func TestBootstrapKeyTooShort(t *testing.T) {
	inTemp(t)
	t.Setenv("ROSETTA_BOOTSTRAP_ADMIN_KEY", "short-key")
	var stderr syncBuffer
	code := run(t.Context(), []string{"serve", "--listen", "127.0.0.1:0"}, strings.NewReader(""), io.Discard, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "at least 32 characters") || strings.Contains(stderr.String(), "short-key") {
		t.Errorf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestBootstrapRace(t *testing.T) {
	dir := inTemp(t)
	cfg, err := config.Parse(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	dsn := "sqlite://" + filepath.Join(dir, "race.db")
	const replicas = 4
	stores := make([]*store.Store, replicas)
	for i := range stores {
		if stores[i], err = store.Open(t.Context(), dsn); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stores[i].Close() })
	}
	errs := make([]error, replicas)
	var wg sync.WaitGroup
	for i := range replicas {
		wg.Go(func() { errs[i] = bootstrap(t.Context(), stores[i], cfg, io.Discard) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("replica %d: %v", i, err)
		}
	}
	if keys, _ := stores[0].ListKeys(t.Context()); len(keys) != 1 {
		t.Errorf("keys = %+v, want one", keys)
	}
}

func TestServeAdminAgents(t *testing.T) {
	inTemp(t)
	path := writeConfig(t, "auth:\n  api_keys:\n    - { name: ops, key: ops-key, role: admin }\nagents:\n  - { name: fixed, kind: echo }\n")
	s := startServe(t, "--config", path)
	if strings.Contains(s.stderr.String(), "rsk_") {
		t.Errorf("a key was made although the config has one: %q", s.stderr.String())
	}
	base := "http://" + s.addr
	code, body := get(t, base+"/admin/v1/agents", "ops-key", `{"name":"made","kind":"echo"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", code, body)
	}
	if code, body := get(t, base+"/agui/made", "ops-key", aguiInput); code != http.StatusOK || !strings.Contains(body, "hello") {
		t.Errorf("run the new agent: status %d, body %s", code, body)
	}
	if code, body := get(t, base+"/admin/v1/runs?agent=made", "ops-key", ""); code != http.StatusOK || !strings.Contains(body, `"protocol":"agui"`) {
		t.Errorf("runs: status %d, body %s", code, body)
	}
	if code, _ := get(t, base+"/admin/v1/agents", "ops-key", `{"name":"fixed","kind":"echo"}`); code != http.StatusConflict {
		t.Errorf("shadowing a config agent: status %d", code)
	}
	if code, _ := get(t, base+"/admin/v1/agents", "ops-key", `{"name":"llm","kind":"llm","model":"m"}`); code != http.StatusBadRequest {
		t.Errorf("llm without transponder.url: status %d", code)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodDelete, base+"/admin/v1/agents/made", nil)
	req.Header.Set("Authorization", "Bearer ops-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete: status %d", resp.StatusCode)
	}
	if code, _ := get(t, base+"/agui/made", "ops-key", aguiInput); code != http.StatusNotFound {
		t.Errorf("deleted agent: status %d", code)
	}
	if code, body := get(t, base+"/readyz", "", ""); code != http.StatusOK || !strings.Contains(body, `"storage":"ok"`) {
		t.Errorf("readyz: status %d, body %s", code, body)
	}
}

func TestReloadFromAnotherProcess(t *testing.T) {
	dir := inTemp(t)
	old := reloadEvery
	reloadEvery = 20 * time.Millisecond
	t.Cleanup(func() { reloadEvery = old })
	path := writeConfig(t, "auth:\n  api_keys:\n    - { name: ops, key: ops-key, role: admin }\n")
	s := startServe(t, "--config", path)

	other, err := store.Open(t.Context(), "sqlite://"+filepath.Join(dir, "rosetta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if _, err := other.CreateAgent(t.Context(), store.Agent{Name: "late", Spec: json.RawMessage(`{"name":"late","kind":"echo"}`)},
		store.Audit{Actor: "test", Action: "agent.create", Target: "late"}); err != nil {
		t.Fatal(err)
	}
	if _, err := other.CreateAgent(t.Context(), store.Agent{Name: "broken", Spec: json.RawMessage(`{"name":"broken","kind":"robot"}`)},
		store.Audit{Actor: "test", Action: "agent.create", Target: "broken"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, _ := get(t, "http://"+s.addr+"/agui/late", "ops-key", aguiInput)
		if code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent made by another process never served, last status %d", code)
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	if code, _ := get(t, "http://"+s.addr+"/agui/broken", "ops-key", aguiInput); code != http.StatusNotFound {
		t.Errorf("broken agent: status %d", code)
	}
}

// TestSlowAgentDoesNotDelayKeys checks that an a2a agent whose card server
// hangs does not hold back a key made at the same time.
func TestSlowAgentDoesNotDelayKeys(t *testing.T) {
	dir := inTemp(t)
	old := reloadEvery
	reloadEvery = 20 * time.Millisecond
	t.Cleanup(func() { reloadEvery = old })
	hang := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(hang) })
	path := writeConfig(t, "auth:\n  api_keys:\n    - { name: ops, key: ops-key, role: admin }\n")
	s := startServe(t, "--config", path)

	other, err := store.Open(t.Context(), "sqlite://"+filepath.Join(dir, "rosetta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	spec := json.RawMessage(`{"name":"slow","kind":"a2a","url":"` + slow.URL + `"}`)
	if _, err := other.CreateAgent(t.Context(), store.Agent{Name: "slow", Spec: spec}, store.Audit{Actor: "test", Action: "agent.create", Target: "slow"}); err != nil {
		t.Fatal(err)
	}
	const key = "a-key-made-by-another-process-0123456789"
	if _, err := other.CreateKey(t.Context(), store.Key{ID: "k_other", Name: "other", Role: "viewer", Hash: auth.Hash(key)},
		store.Audit{Actor: "test", Action: "api_key.create", Target: "k_other"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if code, _ := get(t, "http://"+s.addr+"/admin/v1/status", key, ""); code == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the new key did not work within two seconds")
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestStdioFollowsDatabase checks that rosetta stdio, serving every agent,
// sees an agent made through the database after it started.
func TestStdioFollowsDatabase(t *testing.T) {
	dir := inTemp(t)
	old := reloadEvery
	reloadEvery = 20 * time.Millisecond
	t.Cleanup(func() { reloadEvery = old })
	path := writeConfig(t, "auth: {disabled: true}\nagents: [{name: fixed, kind: echo}]\n")
	toAgentR, toAgentW := io.Pipe()
	toClientR, toClientW := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"stdio", "--protocol", "mcp", "--config", path}, toAgentR, toClientW, io.Discard)
	}()
	t.Cleanup(func() {
		cancel()
		_ = toAgentW.Close()
		<-done
	})
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(toClientR)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	send := func(s string) {
		if _, err := io.WriteString(toAgentW, s+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	reply := func(id string) string {
		t.Helper()
		for {
			select {
			case l := <-lines:
				if strings.Contains(l, `"id":`+id+`,`) || strings.Contains(l, `"id":`+id+`}`) {
					return l
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("no reply to request %s", id)
			}
		}
	}
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	reply("1")
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	other, err := store.Open(t.Context(), "sqlite://"+filepath.Join(dir, "rosetta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if _, err := other.CreateAgent(t.Context(), store.Agent{Name: "late", Spec: json.RawMessage(`{"name":"late","kind":"echo"}`)},
		store.Audit{Actor: "test", Action: "agent.create", Target: "late"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for id := 2; ; id++ {
		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/list"}`, id))
		if l := reply(fmt.Sprint(id)); strings.Contains(l, `"name":"late"`) {
			if !strings.Contains(l, `"name":"fixed"`) {
				t.Errorf("tools = %s", l)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("stdio never listed the agent made after it started")
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestMigrateCommand(t *testing.T) {
	dir := inTemp(t)
	dsn := "sqlite://" + filepath.Join(dir, "m.db")
	for i, want := range []string{"applied 1 migrations", "applied 0 migrations"} {
		var stdout, stderr syncBuffer
		if code := run(t.Context(), []string{"migrate", "--dsn", dsn}, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("run %d: exit %d, stderr %s", i, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("run %d: stdout %q, want %q", i, stdout.String(), want)
		}
	}
	var stderr syncBuffer
	if code := run(t.Context(), []string{"migrate", "--dsn", "mysql://x"}, strings.NewReader(""), io.Discard, &stderr); code != 1 {
		t.Errorf("bad dsn: exit %d", code)
	}
}

func TestBadConfig(t *testing.T) {
	inTemp(t)
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
	inTemp(t)
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
		t.Fatalf("initialize = %+v, %v", res, err)
	}
	sess, err := conn.NewSession(t.Context(), acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{SessionId: sess.SessionId, Prompt: []acpsdk.ContentBlock{acpsdk.TextBlock("hi")}}); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = toAgentW.Close()
	<-done

	st, err := store.Open(t.Context(), config.DefaultDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	runs, _, err := st.ListRuns(t.Context(), store.RunFilter{}, store.Page{})
	if err != nil || len(runs) != 1 || runs[0].Agent != "b" || runs[0].Protocol != "acp" {
		t.Errorf("runs in the shared database = %+v, %v", runs, err)
	}
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
