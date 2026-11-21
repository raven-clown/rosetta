package run

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/protocol"
	"github.com/raven-clown/rosetta/store"
)

type recorder struct {
	mu   sync.Mutex
	runs []store.Run
	err  error
}

func (r *recorder) SaveRun(ctx context.Context, run store.Run) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("no deadline on the save")
	}
	r.runs = append(r.runs, run)
	return r.err
}

func TestRecordRuns(t *testing.T) {
	errBoom := errors.New("boom")
	tests := map[string]struct {
		run    func(ctx context.Context, req *rosetta.Request, out rosetta.Stream) error
		cancel bool
		status string
		errMsg string
	}{
		"completed": {
			run: func(_ context.Context, _ *rosetta.Request, out rosetta.Stream) error {
				_ = out.Send(rosetta.Usage{InputTokens: 1, OutputTokens: 2, ReasoningTokens: 3})
				return out.Text("hi")
			},
			status: store.RunCompleted,
		},
		"failed": {
			run:    func(context.Context, *rosetta.Request, rosetta.Stream) error { return errBoom },
			status: store.RunFailed,
			errMsg: "boom",
		},
		"cancelled": {
			run: func(ctx context.Context, _ *rosetta.Request, _ rosetta.Stream) error {
				<-ctx.Done()
				return ctx.Err()
			},
			cancel: true,
			status: store.RunCancelled,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			r := newRunner(nil)
			WithRecorder(rec)(r)
			ctx, cancel := context.WithCancel(protocol.WithName(t.Context(), "agui"))
			defer cancel()
			if tt.cancel {
				cancel()
			}
			req := &rosetta.Request{SessionID: "s1", Input: []rosetta.Part{{Text: "x"}},
				Caller: rosetta.Caller{ID: "key:abc", RequestID: "req-1"}}
			_ = r.Run(ctx, agentFunc(tt.run), req, func(rosetta.Event) error { return nil })
			if len(rec.runs) != 1 {
				t.Fatalf("runs = %+v", rec.runs)
			}
			got := rec.runs[0]
			if got.ID != req.RunID || got.Agent != "a" || got.Protocol != "agui" || got.SessionID != "s1" ||
				got.Caller != "key:abc" || got.RequestID != "req-1" || got.Status != tt.status || got.Error != tt.errMsg {
				t.Errorf("run = %+v", got)
			}
			if got.Started.IsZero() || got.Ended.Before(got.Started) {
				t.Errorf("times = %v, %v", got.Started, got.Ended)
			}
			if tt.status == store.RunCompleted && (got.InputTokens != 1 || got.OutputTokens != 2 || got.ReasoningTokens != 3) {
				t.Errorf("tokens = %+v", got)
			}
		})
	}
}

func TestRecordErrorDoesNotFailRun(t *testing.T) {
	rec := &recorder{err: errors.New("disk full")}
	obs := &observer{}
	r := newRunner(nil)
	WithRecorder(rec)(r)
	WithObserver(obs)(r)
	err := r.Run(t.Context(), agentFunc(func(_ context.Context, _ *rosetta.Request, out rosetta.Stream) error {
		return out.Text("fine")
	}), &rosetta.Request{}, func(rosetta.Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if got := obs.stores; len(got) != 1 || got[0] != "run" {
		t.Errorf("store errors = %v", got)
	}
}

func TestNoRecorder(t *testing.T) {
	r := newRunner(nil)
	start := time.Now()
	if err := r.Run(t.Context(), agentFunc(func(context.Context, *rosetta.Request, rosetta.Stream) error { return nil }),
		&rosetta.Request{}, func(rosetta.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("run without a recorder was slow")
	}
}
