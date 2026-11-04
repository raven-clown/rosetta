package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/agents/echo"
	"github.com/raven-clown/rosetta/internal/golden"
	"github.com/raven-clown/rosetta/protocol/protocoltest"
)

func serve(t *testing.T, agents ...rosetta.Agent) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	if err := New(Options{}).Mount(mux, protocoltest.StaticRegistry(agents...), protocoltest.NewRunner()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func card(t *testing.T, url string) *a2a.AgentCard {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", url, resp.StatusCode)
	}
	var c a2a.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	return &c
}

func dial(t *testing.T, srv *httptest.Server, agent string) *a2aclient.Client {
	t.Helper()
	c, err := a2aclient.NewFromCard(t.Context(), card(t, srv.URL+"/a2a/"+agent+"/.well-known/agent-card.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Destroy() })
	return c
}

func message(contextID string, parts ...*a2a.Part) *a2a.SendMessageRequest {
	m := a2a.NewMessage(a2a.MessageRoleUser, parts...)
	m.ContextID = contextID
	return &a2a.SendMessageRequest{Message: m}
}

func TestAgentCard(t *testing.T) {
	a := rosetta.Func(rosetta.Card{Name: "helper", Description: "Helps.", Version: "1.2.0",
		Skills: []rosetta.Skill{{ID: "s1", Name: "Search", Description: "Finds things.", Tags: []string{"web"}}}},
		func(context.Context, *rosetta.Request, rosetta.Stream) error { return nil })
	srv := serve(t, a)
	c := card(t, srv.URL+"/a2a/helper/.well-known/agent-card.json")
	if c.Name != "helper" || c.Description != "Helps." || c.Version != "1.2.0" || !c.Capabilities.Streaming {
		t.Errorf("card = %+v", c)
	}
	if len(c.Skills) != 1 || c.Skills[0].ID != "s1" || c.Skills[0].Tags[0] != "web" {
		t.Errorf("skills = %+v", c.Skills)
	}
	if strings.Join(c.DefaultInputModes, ",") != "text/plain,application/json" || strings.Join(c.DefaultOutputModes, ",") != "text/plain,application/json" {
		t.Errorf("modes = %v %v", c.DefaultInputModes, c.DefaultOutputModes)
	}
	want := map[a2a.TransportProtocol]string{
		a2a.TransportProtocolJSONRPC:  srv.URL + "/a2a/helper",
		a2a.TransportProtocolHTTPJSON: srv.URL + "/a2a/helper/v1",
	}
	for _, i := range c.SupportedInterfaces {
		if want[i.ProtocolBinding] != i.URL {
			t.Errorf("interface %s = %s, want %s", i.ProtocolBinding, i.URL, want[i.ProtocolBinding])
		}
		delete(want, i.ProtocolBinding)
	}
	if len(want) != 0 {
		t.Errorf("missing interfaces %v", want)
	}
}

func TestDefaultCard(t *testing.T) {
	srv := serve(t, echo.New("first"), echo.New("second"))
	if c := card(t, srv.URL+"/.well-known/agent-card.json"); c.Name != "first" {
		t.Errorf("default card = %q", c.Name)
	}
}

func TestSendMessage(t *testing.T) {
	c := dial(t, serve(t, echo.New("echo")), "echo")
	res, err := c.SendMessage(t.Context(), message("", a2a.NewTextPart("hello")))
	if err != nil {
		t.Fatal(err)
	}
	task, ok := res.(*a2a.Task)
	if !ok {
		t.Fatalf("result = %T", res)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("state = %s", task.Status.State)
	}
	var text string
	var parts int
	for _, a := range task.Artifacts {
		if a.Name == "response" {
			for _, p := range a.Parts {
				text += p.Text()
				parts++
			}
		}
	}
	if text != "hello" || parts != 1 {
		t.Errorf("response artifact = %q, artifacts = %+v", text, task.Artifacts)
	}
}

var (
	uuidPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	timePattern = regexp.MustCompile(`"\d{4}-\d{2}-\d{2}T[^"]+"`)
)

func TestStreamGolden(t *testing.T) {
	c := dial(t, serve(t, protocoltest.ScriptedAgent("scripted", protocoltest.AllEvents()...)), "scripted")
	var lines bytes.Buffer
	for ev, err := range c.SendStreamingMessage(t.Context(), message("ctx-1", a2a.NewTextPart("hi"))) {
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(ev)
		lines.Write(append(b, '\n'))
	}
	out := uuidPattern.ReplaceAll(lines.Bytes(), []byte("ID"))
	out = timePattern.ReplaceAll(out, []byte(`"T"`))
	golden.Assert(t, "all_events", out)
}

func TestFailed(t *testing.T) {
	a := rosetta.Func(rosetta.Card{Name: "bad"}, func(context.Context, *rosetta.Request, rosetta.Stream) error {
		return errors.New("nope")
	})
	c := dial(t, serve(t, a), "bad")
	res, err := c.SendMessage(t.Context(), message("", a2a.NewTextPart("x")))
	if err != nil {
		t.Fatal(err)
	}
	task := res.(*a2a.Task)
	if task.Status.State != a2a.TaskStateFailed || task.Status.Message == nil || task.Status.Message.Parts[0].Text() != "nope" {
		t.Errorf("status = %+v", task.Status)
	}
}

func TestCancelTask(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	a := rosetta.Func(rosetta.Card{Name: "slow"}, func(ctx context.Context, _ *rosetta.Request, out rosetta.Stream) error {
		if err := out.Text("working"); err != nil {
			return err
		}
		close(started)
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	})
	c := dial(t, serve(t, a), "slow")
	ids := make(chan a2a.TaskID, 1)
	streamCtx, stop := context.WithCancel(t.Context())
	defer stop()
	go func() {
		for ev, err := range c.SendStreamingMessage(streamCtx, message("", a2a.NewTextPart("x"))) {
			if err != nil {
				return
			}
			select {
			case ids <- ev.TaskInfo().TaskID:
			default:
			}
		}
	}()
	taskID := <-ids
	<-started
	task, err := c.CancelTask(t.Context(), &a2a.CancelTaskRequest{ID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	if task.Status.State != a2a.TaskStateCanceled {
		t.Errorf("state = %s", task.Status.State)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("agent was not cancelled")
	}
}

func TestContextIsSession(t *testing.T) {
	var seen []rosetta.Message
	a := rosetta.Func(rosetta.Card{Name: "a"}, func(_ context.Context, req *rosetta.Request, out rosetta.Stream) error {
		seen = req.History
		return out.Text("ok")
	})
	c := dial(t, serve(t, a), "a")
	for _, text := range []string{"one", "two"} {
		if _, err := c.SendMessage(t.Context(), message("ctx-1", a2a.NewTextPart(text))); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 || seen[0].Parts[0].Text != "one" {
		t.Errorf("history = %v", seen)
	}
}

func TestUnknownAgent(t *testing.T) {
	srv := serve(t, echo.New("echo"))
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/a2a/nobody/.well-known/agent-card.json", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d", resp.StatusCode)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{}}`
	post, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/a2a/nobody", strings.NewReader(body))
	resp, err = http.DefaultClient.Do(post)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestDataPartOnly(t *testing.T) {
	var seen *rosetta.Request
	a := rosetta.Func(rosetta.Card{Name: "a"}, func(_ context.Context, req *rosetta.Request, _ rosetta.Stream) error {
		seen = req
		return nil
	})
	c := dial(t, serve(t, a), "a")
	if _, err := c.SendMessage(t.Context(), message("", a2a.NewDataPart(map[string]any{"k": 1}))); err != nil {
		t.Fatal(err)
	}
	if len(seen.Input) != 1 || seen.Input[0].Text != "" || string(seen.Input[0].Data) != `{"k":1}` {
		t.Errorf("input = %+v", seen.Input)
	}
}
