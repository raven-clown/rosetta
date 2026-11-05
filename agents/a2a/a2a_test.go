package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/protocol/protocoltest"
	"github.com/raven-clown/rosetta/server"
)

type remote struct {
	mu        sync.Mutex
	auth      []string
	project   []string
	streaming bool
}

func (r *remote) seen() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.auth...), append([]string{}, r.project...)
}

func serveRemote(t *testing.T, streaming bool, agents ...rosetta.Agent) (*httptest.Server, *remote) {
	t.Helper()
	rm := &remote{streaming: streaming}
	h := server.New(server.Options{}, agents...).Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "agent-card.json") {
			rm.mu.Lock()
			rm.auth = append(rm.auth, r.Header.Get("Authorization"))
			rm.project = append(rm.project, r.Header.Get("X-Project"))
			rm.mu.Unlock()
			h.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		var card map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
			t.Errorf("card: %v", err)
		}
		card["capabilities"] = map[string]any{"streaming": rm.streaming}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(card)
	}))
	t.Cleanup(srv.Close)
	return srv, rm
}

func collect(t *testing.T, a rosetta.Agent, req *rosetta.Request) ([]rosetta.Event, error) {
	t.Helper()
	var events []rosetta.Event
	err := a.Run(t.Context(), req, rosetta.NewStream(func(e rosetta.Event) error {
		events = append(events, e)
		return nil
	}))
	return events, err
}

func hi() *rosetta.Request {
	return &rosetta.Request{SessionID: "s1", Input: []rosetta.Part{{Text: "hi"}}}
}

func TestCardFromRemote(t *testing.T) {
	remoteAgent := rosetta.Func(rosetta.Card{Name: "remote", Description: "Plans trips.", Version: "2.0.0",
		Skills: []rosetta.Skill{{ID: "book", Name: "Book", Description: "Books flights."}}},
		func(context.Context, *rosetta.Request, rosetta.Stream) error { return nil })
	srv, _ := serveRemote(t, true, remoteAgent)
	tests := map[string]struct {
		cfg  Config
		desc string
	}{
		"remote description": {Config{Name: "travel", URL: srv.URL + "/a2a/remote"}, "Plans trips."},
		"config wins":        {Config{Name: "travel", Description: "Mine.", URL: srv.URL + "/a2a/remote/"}, "Mine."},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			c := New(t.Context(), tt.cfg).Card()
			if c.Name != "travel" || c.Description != tt.desc || c.Version != "2.0.0" {
				t.Errorf("card = %+v", c)
			}
			if len(c.Skills) != 1 || c.Skills[0].ID != "book" || c.Skills[0].Description != "Books flights." {
				t.Errorf("skills = %+v", c.Skills)
			}
		})
	}
}

func TestRun(t *testing.T) {
	for _, streaming := range []bool{true, false} {
		name := map[bool]string{true: "streaming", false: "blocking"}[streaming]
		t.Run(name, func(t *testing.T) {
			srv, _ := serveRemote(t, streaming, protocoltest.ScriptedAgent("remote", protocoltest.AllEvents()...))
			a := New(t.Context(), Config{Name: "travel", URL: srv.URL + "/a2a/remote"})
			events, err := collect(t, a, hi())
			if err != nil {
				t.Fatal(err)
			}
			var text bytes.Buffer
			var artifact rosetta.Artifact
			var tools, steps int
			for _, e := range events {
				switch e := e.(type) {
				case rosetta.TextDelta:
					text.WriteString(e.Text)
				case rosetta.Artifact:
					artifact = e
				case rosetta.ToolCall:
					tools++
					if e.Name != "search" || string(e.Args) != `{"q":"rosetta"}` {
						t.Errorf("tool call = %+v", e)
					}
				case rosetta.ToolResult:
					tools++
				case rosetta.Step:
					steps++
				}
			}
			if text.String() != "Hello, world.Found it." {
				t.Errorf("text = %q", text.String())
			}
			if artifact.Name != "report" || len(artifact.Parts) != 2 || artifact.Parts[0].Text != "summary" || string(artifact.Parts[1].Data) != `{"score":9}` {
				t.Errorf("artifact = %+v", artifact)
			}
			if streaming && (tools != 2 || steps != 2) {
				t.Errorf("tools = %d, steps = %d", tools, steps)
			}
		})
	}
}

func TestFailedRemote(t *testing.T) {
	bad := rosetta.Func(rosetta.Card{Name: "remote"}, func(context.Context, *rosetta.Request, rosetta.Stream) error {
		return errors.New("no flights")
	})
	for _, streaming := range []bool{true, false} {
		srv, _ := serveRemote(t, streaming, bad)
		a := New(t.Context(), Config{Name: "travel", URL: srv.URL + "/a2a/remote"})
		if _, err := collect(t, a, hi()); err == nil || !strings.Contains(err.Error(), "no flights") {
			t.Errorf("streaming %v: err = %v", streaming, err)
		}
	}
}

func TestUnreachableAtStart(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	a := New(t.Context(), Config{Name: "travel", Description: "Trips.", URL: srv.URL})
	if c := a.Card(); c.Name != "travel" || c.Description != "Trips." {
		t.Errorf("card = %+v", c)
	}
	_, err := collect(t, a, hi())
	if err == nil || !strings.HasPrefix(err.Error(), "a2a: ") {
		t.Errorf("err = %v", err)
	}
}

func TestResolvesLater(t *testing.T) {
	var up bool
	var mu sync.Mutex
	h := server.New(server.Options{}, protocoltest.ScriptedAgent("remote", rosetta.TextDelta{Text: "ok"})).Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ok := up
		mu.Unlock()
		if !ok {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	a := New(t.Context(), Config{Name: "travel", URL: srv.URL + "/a2a/remote"})
	mu.Lock()
	up = true
	mu.Unlock()
	events, err := collect(t, a, hi())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].(rosetta.TextDelta).Text != "ok" {
		t.Errorf("events = %+v", events)
	}
}

func TestForwardToken(t *testing.T) {
	tests := map[string]struct {
		forward bool
		want    string
	}{
		"dropped":   {false, ""},
		"forwarded": {true, "Bearer tok"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			srv, rm := serveRemote(t, true, protocoltest.ScriptedAgent("remote", rosetta.TextDelta{Text: "ok"}))
			a := New(t.Context(), Config{Name: "travel", URL: srv.URL + "/a2a/remote", ForwardToken: tt.forward})
			req := hi()
			req.Caller = rosetta.Caller{Token: "tok", Project: "p"}
			if _, err := collect(t, a, req); err != nil {
				t.Fatal(err)
			}
			auth, project := rm.seen()
			if len(auth) == 0 {
				t.Fatal("no requests")
			}
			for i := range auth {
				if auth[i] != tt.want || project[i] != "p" {
					t.Errorf("Authorization %q, X-Project %q", auth[i], project[i])
				}
			}
		})
	}
}

func TestSessionsKeptApart(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	remoteAgent := rosetta.Func(rosetta.Card{Name: "remote"}, func(_ context.Context, req *rosetta.Request, out rosetta.Stream) error {
		mu.Lock()
		seen = append(seen, req.SessionID)
		mu.Unlock()
		return out.Text("ok")
	})
	srv, _ := serveRemote(t, true, remoteAgent)
	a := New(t.Context(), Config{Name: "travel", URL: srv.URL + "/a2a/remote", ForwardToken: true})
	for _, tok := range []string{"alice", "bob", ""} {
		req := hi()
		req.Caller.Token = tok
		if _, err := collect(t, a, req); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 || seen[0] == seen[1] || seen[2] != "anon-s1" || !strings.HasPrefix(seen[0], "caller-") {
		t.Errorf("remote context IDs = %q", seen)
	}
}

func TestCancelled(t *testing.T) {
	started := make(chan struct{})
	slow := rosetta.Func(rosetta.Card{Name: "remote"}, func(ctx context.Context, _ *rosetta.Request, out rosetta.Stream) error {
		_ = out.Text("partial")
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	srv, _ := serveRemote(t, true, slow)
	a := New(t.Context(), Config{Name: "travel", URL: srv.URL + "/a2a/remote"})
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-started
		cancel()
	}()
	err := a.Run(ctx, hi(), rosetta.NewStream(func(rosetta.Event) error { return nil }))
	if err == nil {
		t.Error("want an error after cancel")
	}
}
