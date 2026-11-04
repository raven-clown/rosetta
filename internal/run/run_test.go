package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/session"
)

func counter() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("m%d", n)
	}
}

func newRunner(store session.Store) *Runner {
	if store == nil {
		store = session.NewMemory(time.Hour, nil)
	}
	return New(store, WithIDs(counter()), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
}

func agentFunc(f func(ctx context.Context, req *rosetta.Request, out rosetta.Stream) error) rosetta.Agent {
	return rosetta.Func(rosetta.Card{Name: "a"}, f)
}

func record(events *[]rosetta.Event) func(rosetta.Event) error {
	return func(e rosetta.Event) error {
		*events = append(*events, e)
		return nil
	}
}

func TestFraming(t *testing.T) {
	call := rosetta.ToolCall{ID: "c1", Name: "t", Args: json.RawMessage(`{}`)}
	tests := []struct {
		name string
		send []rosetta.Event
		want []rosetta.Event
	}{
		{"text only shares one id",
			[]rosetta.Event{rosetta.TextDelta{Text: "a"}, rosetta.TextDelta{Text: "b"}},
			[]rosetta.Event{rosetta.TextDelta{MessageID: "m2", Text: "a"}, rosetta.TextDelta{MessageID: "m2", Text: "b"}}},
		{"tool call starts a new message",
			[]rosetta.Event{rosetta.TextDelta{Text: "a"}, call, rosetta.TextDelta{Text: "b"}},
			[]rosetta.Event{rosetta.TextDelta{MessageID: "m2", Text: "a"}, call, rosetta.TextDelta{MessageID: "m3", Text: "b"}}},
		{"agent ids are kept",
			[]rosetta.Event{rosetta.TextDelta{MessageID: "x", Text: "a"}, rosetta.TextDelta{Text: "b"}},
			[]rosetta.Event{rosetta.TextDelta{MessageID: "x", Text: "a"}, rosetta.TextDelta{MessageID: "x", Text: "b"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []rosetta.Event
			a := agentFunc(func(_ context.Context, _ *rosetta.Request, out rosetta.Stream) error {
				for _, e := range tt.send {
					if err := out.Send(e); err != nil {
						return err
					}
				}
				return nil
			})
			if err := newRunner(nil).Run(context.Background(), a, &rosetta.Request{}, record(&got)); err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("got %v\nwant %v", got, tt.want)
			}
		})
	}
}

func TestRunFillsRunID(t *testing.T) {
	var seen string
	a := agentFunc(func(_ context.Context, req *rosetta.Request, _ rosetta.Stream) error {
		seen = req.RunID
		return nil
	})
	_ = newRunner(nil).Run(context.Background(), a, &rosetta.Request{}, record(new([]rosetta.Event)))
	if seen != "m1" {
		t.Errorf("RunID = %q, want m1", seen)
	}
	_ = newRunner(nil).Run(context.Background(), a, &rosetta.Request{RunID: "given"}, record(new([]rosetta.Event)))
	if seen != "given" {
		t.Errorf("RunID = %q, want given", seen)
	}
}

func userMsg(text string) rosetta.Message {
	return rosetta.Message{Role: rosetta.RoleUser, Parts: []rosetta.Part{{Text: text}}}
}

func TestHistoryLoadedWhenMissing(t *testing.T) {
	store := session.NewMemory(time.Hour, nil)
	_ = store.Append(context.Background(), "a", "s1", userMsg("earlier"))
	var seen []rosetta.Message
	a := agentFunc(func(_ context.Context, req *rosetta.Request, _ rosetta.Stream) error {
		seen = req.History
		return nil
	})
	_ = newRunner(store).Run(context.Background(), a, &rosetta.Request{SessionID: "s1"}, record(new([]rosetta.Event)))
	if len(seen) != 1 || seen[0].Parts[0].Text != "earlier" {
		t.Errorf("History = %v", seen)
	}
}

func TestHistoryKeptWhenSent(t *testing.T) {
	store := session.NewMemory(time.Hour, nil)
	_ = store.Append(context.Background(), "a", "s1", userMsg("stored"))
	var seen []rosetta.Message
	a := agentFunc(func(_ context.Context, req *rosetta.Request, _ rosetta.Stream) error {
		seen = req.History
		return nil
	})
	req := &rosetta.Request{SessionID: "s1", History: []rosetta.Message{userMsg("sent")}}
	_ = newRunner(store).Run(context.Background(), a, req, record(new([]rosetta.Event)))
	if len(seen) != 1 || seen[0].Parts[0].Text != "sent" {
		t.Errorf("History = %v", seen)
	}
}

func TestHistoryAppendedAfterSuccess(t *testing.T) {
	store := session.NewMemory(time.Hour, nil)
	a := agentFunc(func(_ context.Context, _ *rosetta.Request, out rosetta.Stream) error {
		_ = out.Text("Hello, ")
		return out.Text("Ada.")
	})
	req := &rosetta.Request{SessionID: "s1", Input: []rosetta.Part{{Text: "hi"}}}
	if err := newRunner(store).Run(context.Background(), a, req, record(new([]rosetta.Event))); err != nil {
		t.Fatal(err)
	}
	got, _ := store.History(context.Background(), "a", "s1")
	if len(got) != 2 || got[0].Role != rosetta.RoleUser || got[0].Parts[0].Text != "hi" ||
		got[1].Role != rosetta.RoleAgent || got[1].Parts[0].Text != "Hello, Ada." {
		t.Errorf("stored = %v", got)
	}
}

func TestNoAppendOnError(t *testing.T) {
	store := session.NewMemory(time.Hour, nil)
	a := agentFunc(func(context.Context, *rosetta.Request, rosetta.Stream) error { return errors.New("nope") })
	req := &rosetta.Request{SessionID: "s1", Input: []rosetta.Part{{Text: "hi"}}}
	err := newRunner(store).Run(context.Background(), a, req, record(new([]rosetta.Event)))
	if err == nil || err.Error() != "nope" {
		t.Fatalf("err = %v", err)
	}
	if got, _ := store.History(context.Background(), "a", "s1"); len(got) != 0 {
		t.Errorf("stored = %v", got)
	}
}

func TestPanicBecomesError(t *testing.T) {
	a := agentFunc(func(context.Context, *rosetta.Request, rosetta.Stream) error { panic("boom") })
	err := newRunner(nil).Run(context.Background(), a, &rosetta.Request{}, record(new([]rosetta.Event)))
	if err == nil || !strings.Contains(err.Error(), "panic") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
}

func TestCancelReturnsCanceled(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	started := make(chan struct{})
	a := agentFunc(func(context.Context, *rosetta.Request, rosetta.Stream) error {
		close(started)
		<-block
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	err := newRunner(nil).Run(ctx, a, &rosetta.Request{}, record(new([]rosetta.Event)))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestSinkErrorStops(t *testing.T) {
	errGone := errors.New("client gone")
	var sendErr error
	a := agentFunc(func(_ context.Context, _ *rosetta.Request, out rosetta.Stream) error {
		sendErr = out.Text("a")
		return sendErr
	})
	err := newRunner(nil).Run(context.Background(), a, &rosetta.Request{}, func(rosetta.Event) error { return errGone })
	if !errors.Is(err, errGone) || !errors.Is(sendErr, errGone) {
		t.Errorf("err = %v, sendErr = %v", err, sendErr)
	}
}

func TestConcurrentSendsAreSerialised(t *testing.T) {
	var inside, maxInside, count atomic.Int32
	ids := map[string]bool{}
	sink := func(e rosetta.Event) error {
		n := inside.Add(1)
		if n > maxInside.Load() {
			maxInside.Store(n)
		}
		ids[e.(rosetta.TextDelta).MessageID] = true
		count.Add(1)
		inside.Add(-1)
		return nil
	}
	a := agentFunc(func(_ context.Context, _ *rosetta.Request, out rosetta.Stream) error {
		var wg sync.WaitGroup
		for range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 100 {
					_ = out.Text("x")
				}
			}()
		}
		wg.Wait()
		return nil
	})
	if err := newRunner(nil).Run(context.Background(), a, &rosetta.Request{}, sink); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1000 || maxInside.Load() != 1 || len(ids) != 1 {
		t.Errorf("count=%d maxInside=%d ids=%d", count.Load(), maxInside.Load(), len(ids))
	}
}

func TestSendAfterRunEnded(t *testing.T) {
	var kept rosetta.Stream
	var got []rosetta.Event
	a := agentFunc(func(_ context.Context, _ *rosetta.Request, out rosetta.Stream) error {
		kept = out
		return nil
	})
	if err := newRunner(nil).Run(context.Background(), a, &rosetta.Request{}, record(&got)); err != nil {
		t.Fatal(err)
	}
	if err := kept.Text("late"); !errors.Is(err, rosetta.ErrRunEnded) {
		t.Errorf("err = %v, want ErrRunEnded", err)
	}
	if len(got) != 0 {
		t.Errorf("sink got %v", got)
	}
}

func TestDefaultIDs(t *testing.T) {
	r := New(session.NewMemory(time.Hour, nil))
	a, b := r.ids(), r.ids()
	if len(a) != 32 || a == b {
		t.Errorf("ids %q %q", a, b)
	}
}
