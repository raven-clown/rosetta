package run

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/raven-clown/rosetta"
)

type done struct {
	agent string
	err   error
	usage rosetta.Usage
}

type observer struct {
	mu     sync.Mutex
	runs   []done
	stores []string
}

func (o *observer) RunDone(agent string, err error, d time.Duration, u rosetta.Usage) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if d < 0 {
		panic("negative duration")
	}
	o.runs = append(o.runs, done{agent, err, u})
}

func (o *observer) StoreError(op string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stores = append(o.stores, op)
}

type brokenStore struct{}

var errStore = errors.New("disk full")

func (brokenStore) History(context.Context, string, string) ([]rosetta.Message, error) {
	return nil, errStore
}

func (brokenStore) Append(context.Context, string, string, ...rosetta.Message) error {
	return errStore
}

func TestObserver(t *testing.T) {
	errNope := errors.New("nope")
	tests := map[string]struct {
		run     func(context.Context, *rosetta.Request, rosetta.Stream) error
		cancel  bool
		wantErr error
		usage   rosetta.Usage
	}{
		"completed with usage": {
			run: func(_ context.Context, _ *rosetta.Request, out rosetta.Stream) error {
				_ = out.Send(rosetta.Usage{InputTokens: 1, OutputTokens: 2})
				return out.Send(rosetta.Usage{InputTokens: 10, OutputTokens: 20})
			},
			usage: rosetta.Usage{InputTokens: 11, OutputTokens: 22},
		},
		"failed": {
			run:     func(context.Context, *rosetta.Request, rosetta.Stream) error { return errNope },
			wantErr: errNope,
		},
		"cancelled": {
			run: func(ctx context.Context, _ *rosetta.Request, _ rosetta.Stream) error {
				<-ctx.Done()
				return ctx.Err()
			},
			cancel:  true,
			wantErr: context.Canceled,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			o := &observer{}
			r := New(brokenStore{}, WithObserver(o), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			ctx, cancel := context.WithCancel(t.Context())
			if tt.cancel {
				cancel()
			}
			defer cancel()
			_ = r.Run(ctx, agentFunc(tt.run), &rosetta.Request{SessionID: "s"}, func(rosetta.Event) error { return nil })
			if len(o.runs) != 1 {
				t.Fatalf("runs = %+v", o.runs)
			}
			got := o.runs[0]
			if got.agent != "a" || !errors.Is(got.err, tt.wantErr) || (tt.wantErr == nil && got.err != nil) || got.usage != tt.usage {
				t.Errorf("run = %+v", got)
			}
			wantStores := []string{"history"}
			if tt.wantErr == nil {
				wantStores = append(wantStores, "append")
			}
			if !reflect.DeepEqual(o.stores, wantStores) {
				t.Errorf("store errors = %v, want %v", o.stores, wantStores)
			}
		})
	}
}

func (brokenStore) Delete(context.Context, string, string) error { return errStore }
