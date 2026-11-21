package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/internal/golden"
	"github.com/raven-clown/rosetta/protocol"
	"github.com/raven-clown/rosetta/protocol/protocoltest"
)

type recorder struct {
	mu       sync.Mutex
	updates  []acpsdk.SessionNotification
	elicited int
}

func (r *recorder) SessionUpdate(_ context.Context, n acpsdk.SessionNotification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates = append(r.updates, n)
	return nil
}

func (r *recorder) all() []acpsdk.SessionNotification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]acpsdk.SessionNotification{}, r.updates...)
}

var errUnused = errors.New("not used in tests")

func (r *recorder) ReadTextFile(context.Context, acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	return acpsdk.ReadTextFileResponse{}, errUnused
}

func (r *recorder) WriteTextFile(context.Context, acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	return acpsdk.WriteTextFileResponse{}, errUnused
}

func (r *recorder) RequestPermission(context.Context, acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	return acpsdk.RequestPermissionResponse{}, errUnused
}

func (r *recorder) CreateTerminal(context.Context, acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.CreateTerminalResponse{}, errUnused
}

func (r *recorder) KillTerminal(context.Context, acpsdk.KillTerminalRequest) (acpsdk.KillTerminalResponse, error) {
	return acpsdk.KillTerminalResponse{}, errUnused
}

func (r *recorder) TerminalOutput(context.Context, acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return acpsdk.TerminalOutputResponse{}, errUnused
}

func (r *recorder) ReleaseTerminal(context.Context, acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.ReleaseTerminalResponse{}, errUnused
}

func (r *recorder) WaitForTerminalExit(context.Context, acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return acpsdk.WaitForTerminalExitResponse{}, errUnused
}

// rosetta never asks the client for input, so the elicitation calls fail
// the test if they ever arrive.
func (r *recorder) CreateElicitation(context.Context, acpsdk.CreateElicitationRequest) (acpsdk.CreateElicitationResponse, error) {
	r.mu.Lock()
	r.elicited++
	r.mu.Unlock()
	return acpsdk.CreateElicitationResponse{}, errUnused
}

func (r *recorder) CompleteElicitation(context.Context, acpsdk.CompleteElicitationNotification) error {
	r.mu.Lock()
	r.elicited++
	r.mu.Unlock()
	return nil
}

func start(t *testing.T, agent rosetta.Agent) (*acpsdk.ClientSideConnection, *recorder) {
	t.Helper()
	return startWith(t, protocoltest.NewRunner(), agent)
}

func startWith(t *testing.T, run protocol.Runner, agent rosetta.Agent) (*acpsdk.ClientSideConnection, *recorder) {
	t.Helper()
	toAgentR, toAgentW := io.Pipe()
	toClientR, toClientW := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- New(Options{}).ServeStdio(ctx, protocoltest.StaticRegistry(agent), run, toAgentR, toClientW)
	}()
	rec := &recorder{}
	conn := acpsdk.NewClientSideConnection(rec, toAgentW, toClientR)
	t.Cleanup(func() {
		cancel()
		_ = toAgentW.Close()
		_ = toClientW.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("ServeStdio: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("ServeStdio did not return")
		}
	})
	if _, err := conn.Initialize(t.Context(), acpsdk.InitializeRequest{ProtocolVersion: acpsdk.ProtocolVersionNumber}); err != nil {
		t.Fatal(err)
	}
	return conn, rec
}

func newSession(t *testing.T, conn *acpsdk.ClientSideConnection) acpsdk.SessionId {
	t.Helper()
	s, err := conn.NewSession(t.Context(), acpsdk.NewSessionRequest{Cwd: "/tmp", McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	return s.SessionId
}

func prompt(ctx context.Context, conn *acpsdk.ClientSideConnection, id acpsdk.SessionId, text string) (acpsdk.PromptResponse, error) {
	return conn.Prompt(ctx, acpsdk.PromptRequest{SessionId: id, Prompt: []acpsdk.ContentBlock{acpsdk.TextBlock(text)}})
}

func TestInitialize(t *testing.T) {
	toAgentR, toAgentW := io.Pipe()
	toClientR, toClientW := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		_ = New(Options{}).ServeStdio(ctx, protocoltest.StaticRegistry(protocoltest.ScriptedAgent("a")), protocoltest.NewRunner(), toAgentR, toClientW)
	}()
	conn := acpsdk.NewClientSideConnection(&recorder{}, toAgentW, toClientR)
	res, err := conn.Initialize(t.Context(), acpsdk.InitializeRequest{ProtocolVersion: acpsdk.ProtocolVersionNumber})
	if err != nil {
		t.Fatal(err)
	}
	if res.ProtocolVersion != acpsdk.ProtocolVersionNumber || res.AgentCapabilities.LoadSession {
		t.Errorf("initialize = %+v", res)
	}
	if res.AgentInfo == nil || res.AgentInfo.Name != "a" {
		t.Errorf("agent info = %+v", res.AgentInfo)
	}
}

func TestRunTagged(t *testing.T) {
	spy := protocoltest.NewSpy()
	conn, _ := startWith(t, spy, protocoltest.ScriptedAgent("a", rosetta.TextDelta{Text: "x"}))
	if _, err := prompt(t.Context(), conn, newSession(t, conn), "hi"); err != nil {
		t.Fatal(err)
	}
	if got := spy.Names(); len(got) != 1 || got[0] != "acp" {
		t.Errorf("protocols = %v", got)
	}
}

func TestFailedGolden(t *testing.T) {
	conn, rec := start(t, protocoltest.Failing("failing"))
	id := newSession(t, conn)
	_, err := prompt(t.Context(), conn, id, "hi")
	var reqErr *acpsdk.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("err = %v, want a JSON-RPC error", err)
	}
	var lines bytes.Buffer
	for _, u := range rec.all() {
		b, _ := json.Marshal(u)
		lines.Write(append(b, '\n'))
	}
	b, _ := json.Marshal(reqErr)
	lines.Write(append(b, '\n'))
	golden.Assert(t, "failed", bytes.ReplaceAll(lines.Bytes(), []byte(id), []byte("SESSION")))
}

func TestPromptGolden(t *testing.T) {
	conn, rec := start(t, protocoltest.ScriptedAgent("scripted", protocoltest.AllEvents()...))
	id := newSession(t, conn)
	res, err := prompt(t.Context(), conn, id, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != acpsdk.StopReasonEndTurn {
		t.Errorf("stop reason = %s", res.StopReason)
	}
	var lines bytes.Buffer
	for _, u := range rec.all() {
		b, _ := json.Marshal(u)
		lines.Write(append(b, '\n'))
	}
	golden.Assert(t, "all_events", bytes.ReplaceAll(lines.Bytes(), []byte(id), []byte("SESSION")))
}

func TestPromptError(t *testing.T) {
	a := rosetta.Func(rosetta.Card{Name: "bad"}, func(context.Context, *rosetta.Request, rosetta.Stream) error {
		return errors.New("nope")
	})
	conn, _ := start(t, a)
	_, err := prompt(t.Context(), conn, newSession(t, conn), "x")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("err = %v", err)
	}
}

func TestCancel(t *testing.T) {
	started := make(chan struct{})
	a := rosetta.Func(rosetta.Card{Name: "slow"}, func(ctx context.Context, _ *rosetta.Request, _ rosetta.Stream) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	conn, _ := start(t, a)
	id := newSession(t, conn)
	go func() {
		<-started
		_ = conn.Cancel(t.Context(), acpsdk.CancelNotification{SessionId: id})
	}()
	res, err := prompt(t.Context(), conn, id, "x")
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != acpsdk.StopReasonCancelled {
		t.Errorf("stop reason = %s", res.StopReason)
	}
}

func TestDeleteSession(t *testing.T) {
	started := make(chan struct{})
	a := rosetta.Func(rosetta.Card{Name: "slow"}, func(ctx context.Context, _ *rosetta.Request, _ rosetta.Stream) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	conn, rec := start(t, a)
	id := newSession(t, conn)
	go func() {
		<-started
		_, _ = conn.DeleteSession(t.Context(), acpsdk.DeleteSessionRequest{SessionId: id})
	}()
	res, err := prompt(t.Context(), conn, id, "x")
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != acpsdk.StopReasonCancelled {
		t.Errorf("stop reason = %s, want cancelled", res.StopReason)
	}
	if _, err := prompt(t.Context(), conn, id, "again"); err == nil {
		t.Error("prompt on a deleted session succeeded")
	}
	if _, err := conn.DeleteSession(t.Context(), acpsdk.DeleteSessionRequest{SessionId: "unknown"}); err != nil {
		t.Errorf("deleting an unknown session = %v, want nil", err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.elicited != 0 {
		t.Errorf("rosetta sent %d elicitation calls", rec.elicited)
	}
}

func TestCapabilities(t *testing.T) {
	conn, _ := start(t, protocoltest.ScriptedAgent("a"))
	res, err := conn.Initialize(t.Context(), acpsdk.InitializeRequest{ProtocolVersion: acpsdk.ProtocolVersionNumber})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.AgentCapabilities)
	for _, word := range []string{"delete", "list", "elicitation", "loadSession"} {
		if strings.Contains(string(b), word) {
			t.Errorf("capabilities %s advertise %s, which rosetta does not serve", b, word)
		}
	}
}

func TestSessionHistory(t *testing.T) {
	var seen []rosetta.Message
	a := rosetta.Func(rosetta.Card{Name: "a"}, func(_ context.Context, req *rosetta.Request, out rosetta.Stream) error {
		seen = req.History
		return out.Text("ok")
	})
	conn, _ := start(t, a)
	id := newSession(t, conn)
	for _, text := range []string{"one", "two"} {
		if _, err := prompt(t.Context(), conn, id, text); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 || seen[0].Parts[0].Text != "one" {
		t.Errorf("history = %v", seen)
	}
}

func TestUnknownSession(t *testing.T) {
	conn, _ := start(t, protocoltest.ScriptedAgent("a"))
	_, err := prompt(t.Context(), conn, "nope", "x")
	var re *acpsdk.RequestError
	if !errors.As(err, &re) || re.Code != -32602 {
		t.Errorf("err = %v", err)
	}
}

func TestEmptyRegistry(t *testing.T) {
	err := New(Options{}).ServeStdio(t.Context(), protocoltest.StaticRegistry(), protocoltest.NewRunner(), strings.NewReader(""), io.Discard)
	if err == nil {
		t.Error("want an error with no agents")
	}
}

type checkedReader struct {
	opened *atomic.Bool
	t      *testing.T
}

func (c checkedReader) Read(p []byte) (int, error) {
	if !c.opened.Load() {
		c.t.Error("read before the gate opened")
	}
	return copy(p, "x"), nil
}

func TestGateHoldsReads(t *testing.T) {
	var opened atomic.Bool
	g := newGate(checkedReader{&opened, t})
	got := make(chan string, 1)
	go func() {
		b := make([]byte, 1)
		n, _ := g.Read(b)
		got <- string(b[:n])
	}()
	runtime.Gosched()
	opened.Store(true)
	g.open()
	if s := <-got; s != "x" {
		t.Errorf("read %q", s)
	}
}
