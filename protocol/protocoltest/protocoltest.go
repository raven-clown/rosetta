// Package protocoltest helps test protocol adapters.
package protocoltest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/internal/run"
	"github.com/raven-clown/rosetta/protocol"
	"github.com/raven-clown/rosetta/registry"
	"github.com/raven-clown/rosetta/session"
)

// ScriptedAgent returns an agent that sends events in order and returns nil.
func ScriptedAgent(name string, events ...rosetta.Event) rosetta.Agent {
	return rosetta.Func(rosetta.Card{Name: name, Description: "Scripted agent " + name},
		func(_ context.Context, _ *rosetta.Request, out rosetta.Stream) error {
			for _, e := range events {
				if err := out.Send(e); err != nil {
					return err
				}
			}
			return nil
		})
}

// ErrFailing is what the Failing agent returns.
var ErrFailing = errors.New("upstream said no")

// Failing returns an agent that sends one text delta and then fails with
// ErrFailing, for pinning each protocol's failure shape.
func Failing(name string) rosetta.Agent {
	return rosetta.Func(rosetta.Card{Name: name, Description: "Failing agent " + name},
		func(_ context.Context, _ *rosetta.Request, out rosetta.Stream) error {
			if err := out.Text("partial"); err != nil {
				return err
			}
			return ErrFailing
		})
}

// AllEvents returns one of every event type with fixed values, so golden
// files stay stable.
func AllEvents() []rosetta.Event {
	return []rosetta.Event{
		rosetta.Step{Name: "plan"},
		rosetta.ReasoningDelta{Text: "thinking"},
		rosetta.TextDelta{Text: "Hello, "},
		rosetta.TextDelta{Text: "world."},
		rosetta.ToolCall{ID: "call-1", Name: "search", Args: json.RawMessage(`{"q":"rosetta"}`)},
		rosetta.ToolResult{ID: "call-1", Result: json.RawMessage(`{"hits":1}`)},
		rosetta.TextDelta{Text: "Found it."},
		rosetta.StateSnapshot{State: json.RawMessage(`{"step":2}`)},
		rosetta.Artifact{Name: "report", Parts: []rosetta.Part{{Text: "summary"}, {Data: json.RawMessage(`{"score":9}`)}}},
		rosetta.Usage{InputTokens: 10, OutputTokens: 5},
		rosetta.Extension{Protocol: "none", Value: "ignored"},
		rosetta.Step{Name: "plan", Finished: true},
	}
}

// StaticRegistry returns a registry of the given agents and panics on bad names.
func StaticRegistry(agents ...rosetta.Agent) protocol.Registry {
	r, err := registry.New(agents...)
	if err != nil {
		panic(err)
	}
	return r
}

// Spy is a Runner that notes the protocol each run was tagged with.
type Spy struct {
	protocol.Runner
	mu    sync.Mutex
	names []string
}

// NewSpy wraps NewRunner.
func NewSpy() *Spy { return &Spy{Runner: NewRunner()} }

// Run notes protocol.NameFrom(ctx) and runs.
func (s *Spy) Run(ctx context.Context, agent rosetta.Agent, req *rosetta.Request, sink func(rosetta.Event) error) error {
	s.mu.Lock()
	s.names = append(s.names, protocol.NameFrom(ctx))
	s.mu.Unlock()
	return s.Runner.Run(ctx, agent, req, sink)
}

// Names returns the protocols seen so far.
func (s *Spy) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.names...)
}

// NewRunner returns a runner with an in-memory store, a silent logger and
// IDs counting up from id-1, so wire output is stable.
func NewRunner() protocol.Runner {
	var n atomic.Int64
	ids := func() string { return fmt.Sprintf("id-%d", n.Add(1)) }
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return run.New(session.NewMemory(time.Hour, nil), run.WithIDs(ids), run.WithLogger(logger))
}
