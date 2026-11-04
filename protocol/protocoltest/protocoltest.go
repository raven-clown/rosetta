// Package protocoltest helps test protocol adapters.
package protocoltest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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

// NewRunner returns a runner with an in-memory store, a silent logger and
// IDs counting up from id-1, so wire output is stable.
func NewRunner() protocol.Runner {
	var n atomic.Int64
	ids := func() string { return fmt.Sprintf("id-%d", n.Add(1)) }
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return run.New(session.NewMemory(time.Hour, nil), run.WithIDs(ids), run.WithLogger(logger))
}
