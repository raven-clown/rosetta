package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"sync"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/protocol"
)

const responseArtifact = "response"

type executor struct {
	agent   rosetta.Agent
	run     protocol.Runner
	mu      sync.Mutex
	running map[a2a.TaskID]context.CancelFunc
}

func newExecutor(agent rosetta.Agent, run protocol.Runner) *executor {
	return &executor{agent: agent, run: run, running: map[a2a.TaskID]context.CancelFunc{}}
}

func (x *executor) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if ec.StoredTask == nil {
			if !yield(a2a.NewSubmittedTask(ec, ec.Message), nil) {
				return
			}
		}
		if !yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateWorking, nil), nil) {
			return
		}

		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		x.mu.Lock()
		x.running[ec.TaskID] = cancel
		x.mu.Unlock()
		defer func() {
			x.mu.Lock()
			delete(x.running, ec.TaskID)
			x.mu.Unlock()
		}()

		events := make(chan a2a.Event)
		errc := make(chan error, 1)
		m := &mapper{ec: ec}
		go func() {
			defer close(events)
			errc <- x.run.Run(runCtx, x.agent, request(ec), func(e rosetta.Event) error {
				for _, ev := range m.convert(e) {
					select {
					case events <- ev:
					case <-runCtx.Done():
						return runCtx.Err()
					}
				}
				return nil
			})
		}()
		for ev := range events {
			if !yield(ev, nil) {
				cancel()
				for range events {
				}
				return
			}
		}
		for _, ev := range m.finish(<-errc) {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func (x *executor) Cancel(_ context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	x.mu.Lock()
	if cancel, ok := x.running[ec.TaskID]; ok {
		cancel()
	}
	x.mu.Unlock()
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCanceled, nil), nil)
	}
}

func request(ec *a2asrv.ExecutorContext) *rosetta.Request {
	req := &rosetta.Request{SessionID: ec.ContextID, RunID: string(ec.TaskID)}
	if ec.Message != nil {
		for _, p := range ec.Message.Parts {
			switch {
			case p.Text() != "":
				req.Input = append(req.Input, rosetta.Part{Text: p.Text()})
			case p.Data() != nil:
				if b, err := json.Marshal(p.Data()); err == nil {
					req.Input = append(req.Input, rosetta.Part{Data: b})
				}
			}
		}
	}
	if ec.ServiceParams != nil {
		for k, v := range ec.ServiceParams.List() {
			if len(v) == 0 {
				continue
			}
			switch strings.ToLower(k) {
			case "authorization":
				req.Caller.Token = strings.TrimPrefix(v[0], "Bearer ")
			case "x-request-id":
				req.Caller.RequestID = v[0]
			}
		}
	}
	return req
}

type mapper struct {
	ec      *a2asrv.ExecutorContext
	started bool
}

func agentMessage(ec *a2asrv.ExecutorContext, parts ...*a2a.Part) *a2a.Message {
	return a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, parts...)
}

func (m *mapper) chunk(parts ...*a2a.Part) *a2a.TaskArtifactUpdateEvent {
	ev := a2a.NewArtifactUpdateEvent(m.ec, responseArtifact, parts...)
	ev.Artifact.Name = responseArtifact
	ev.Append = m.started
	m.started = true
	return ev
}

func (m *mapper) convert(e rosetta.Event) []a2a.Event {
	working := func(data map[string]any) []a2a.Event {
		return []a2a.Event{a2a.NewStatusUpdateEvent(m.ec, a2a.TaskStateWorking, agentMessage(m.ec, a2a.NewDataPart(data)))}
	}
	switch e := e.(type) {
	case rosetta.TextDelta:
		return []a2a.Event{m.chunk(a2a.NewTextPart(e.Text))}
	case rosetta.ToolCall:
		return working(map[string]any{"toolCall": map[string]any{"id": e.ID, "name": e.Name, "args": decode(e.Args)}})
	case rosetta.ToolResult:
		return working(map[string]any{"toolResult": map[string]any{"id": e.ID, "result": decode(e.Result), "isError": e.IsError}})
	case rosetta.Step:
		return working(map[string]any{"step": map[string]any{"name": e.Name, "finished": e.Finished}})
	case rosetta.Artifact:
		var parts []*a2a.Part
		for _, p := range e.Parts {
			if p.Text != "" {
				parts = append(parts, a2a.NewTextPart(p.Text))
			}
			if p.Data != nil {
				parts = append(parts, a2a.NewDataPart(decode(p.Data)))
			}
		}
		ev := a2a.NewArtifactEvent(m.ec, parts...)
		ev.Artifact.Name = e.Name
		return []a2a.Event{ev}
	case rosetta.Extension:
		if ev, ok := e.Value.(a2a.Event); ok && e.Protocol == "a2a" {
			return []a2a.Event{ev}
		}
	}
	return nil
}

func (m *mapper) finish(err error) []a2a.Event {
	var out []a2a.Event
	if m.started {
		last := m.chunk(a2a.NewTextPart(""))
		last.LastChunk = true
		out = append(out, last)
	}
	switch {
	case err == nil:
		out = append(out, a2a.NewStatusUpdateEvent(m.ec, a2a.TaskStateCompleted, nil))
	case errors.Is(err, context.Canceled):
		out = append(out, a2a.NewStatusUpdateEvent(m.ec, a2a.TaskStateCanceled, nil))
	default:
		out = append(out, a2a.NewStatusUpdateEvent(m.ec, a2a.TaskStateFailed, agentMessage(m.ec, a2a.NewTextPart(err.Error()))))
	}
	return out
}

func decode(raw json.RawMessage) any {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return v
}
