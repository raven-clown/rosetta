package agui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/client/sse"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/sirupsen/logrus"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/internal/golden"
	"github.com/raven-clown/rosetta/protocol/protocoltest"
)

var fixed = time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)

func handler(t *testing.T, agents ...rosetta.Agent) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	a := New(Options{Now: func() time.Time { return fixed }})
	if err := a.Mount(mux, protocoltest.StaticRegistry(agents...), protocoltest.NewRunner()); err != nil {
		t.Fatal(err)
	}
	return mux
}

func input(messages ...types.Message) string {
	b, _ := json.Marshal(types.RunAgentInput{ThreadID: "t1", RunID: "r1", Messages: messages})
	return string(b)
}

func user(id, text string) types.Message {
	return types.Message{ID: id, Role: types.RoleUser, Content: text}
}

func post(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body)))
	return rec
}

func eventTypes(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var e struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			t.Fatalf("bad frame %q: %v", data, err)
		}
		out = append(out, e.Type)
	}
	return out
}

func TestAllEventsGolden(t *testing.T) {
	h := handler(t, protocoltest.ScriptedAgent("scripted", protocoltest.AllEvents()...))
	rec := post(t, h, "/agui/scripted", input(user("u1", "hi")))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, content type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	golden.Assert(t, "all_events", rec.Body.Bytes())
}

func TestRoundTripWithSDKClient(t *testing.T) {
	srv := httptest.NewServer(handler(t, protocoltest.ScriptedAgent("scripted", protocoltest.AllEvents()...)))
	defer srv.Close()
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	c := sse.NewClient(sse.Config{Endpoint: srv.URL + "/agui/scripted", Logger: logger, ReadTimeout: 5 * time.Second})
	defer func() { _ = c.Close() }()
	frames, errs, err := c.Stream(sse.StreamOptions{
		Context: context.Background(),
		Payload: types.RunAgentInput{ThreadID: "t1", RunID: "r1", Messages: []types.Message{user("u1", "hi")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for f := range frames {
		var e struct{ Type string }
		if err := json.Unmarshal(f.Data, &e); err != nil {
			t.Fatal(err)
		}
		got = append(got, e.Type)
	}
	for err := range errs {
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
	}
	if len(got) < 2 || got[0] != "RUN_STARTED" || got[len(got)-1] != "RUN_FINISHED" {
		t.Errorf("events = %v", got)
	}
}

func TestRunError(t *testing.T) {
	a := rosetta.Func(rosetta.Card{Name: "bad"}, func(context.Context, *rosetta.Request, rosetta.Stream) error {
		return errors.New("nope")
	})
	rec := post(t, handler(t, a), "/agui/bad", input(user("u1", "hi")))
	got := eventTypes(t, rec.Body.String())
	if got[len(got)-1] != "RUN_ERROR" || !strings.Contains(rec.Body.String(), `"message":"nope"`) {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestBadRequests(t *testing.T) {
	h := handler(t, protocoltest.ScriptedAgent("scripted"))
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"invalid json", http.MethodPost, "/agui/scripted", "{", http.StatusBadRequest},
		{"unknown agent", http.MethodPost, "/agui/nobody", input(), http.StatusNotFound},
		{"wrong method", http.MethodGet, "/agui/scripted", "", http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, strings.NewReader(tt.body)))
			if rec.Code != tt.want {
				t.Errorf("status %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestHistoryFromMessages(t *testing.T) {
	var seen *rosetta.Request
	a := rosetta.Func(rosetta.Card{Name: "a"}, func(_ context.Context, req *rosetta.Request, _ rosetta.Stream) error {
		seen = req
		return nil
	})
	assistant := types.Message{ID: "a1", Role: types.RoleAssistant, Content: "hello"}
	post(t, handler(t, a), "/agui/a", input(user("u1", "hi"), assistant, user("u2", "again")))
	if seen.SessionID != "t1" || seen.RunID != "r1" {
		t.Errorf("session %q run %q", seen.SessionID, seen.RunID)
	}
	if len(seen.History) != 2 || seen.History[0].Role != rosetta.RoleUser || seen.History[1].Role != rosetta.RoleAgent ||
		seen.History[1].Parts[0].Text != "hello" {
		t.Errorf("history = %v", seen.History)
	}
	if seen.Text() != "again" {
		t.Errorf("input = %v", seen.Input)
	}
}

func TestNoUserMessage(t *testing.T) {
	var seen *rosetta.Request
	a := rosetta.Func(rosetta.Card{Name: "a"}, func(_ context.Context, req *rosetta.Request, _ rosetta.Stream) error {
		seen = req
		return nil
	})
	rec := post(t, handler(t, a), "/agui/a", input())
	if len(seen.Input) != 0 || seen.History == nil {
		t.Errorf("input %v, history %v", seen.Input, seen.History)
	}
	if got := eventTypes(t, rec.Body.String()); got[0] != "RUN_STARTED" {
		t.Errorf("events = %v", got)
	}
}

func TestInputContentParts(t *testing.T) {
	var seen *rosetta.Request
	a := rosetta.Func(rosetta.Card{Name: "a"}, func(_ context.Context, req *rosetta.Request, _ rosetta.Stream) error {
		seen = req
		return nil
	})
	parts := []types.InputContent{{Type: "text", Text: "one"}, {Type: "text", Text: "two"}}
	post(t, handler(t, a), "/agui/a", input(types.Message{ID: "u1", Role: types.RoleUser, Content: parts}))
	if seen.Text() != "one\ntwo" {
		t.Errorf("input = %v", seen.Input)
	}
}

func TestClientDisconnectCancelsRun(t *testing.T) {
	cancelled := make(chan struct{})
	a := rosetta.Func(rosetta.Card{Name: "slow"}, func(ctx context.Context, _ *rosetta.Request, out rosetta.Stream) error {
		if err := out.Text("first"); err != nil {
			return err
		}
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	})
	srv := httptest.NewServer(handler(t, a))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/agui/slow", strings.NewReader(input(user("u1", "hi"))))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(resp.Body)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, "TEXT_MESSAGE_CONTENT") {
			break
		}
	}
	cancel()
	_ = resp.Body.Close()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never saw the client leave")
	}
}
