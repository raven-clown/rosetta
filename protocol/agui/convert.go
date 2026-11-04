package agui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"

	"github.com/raven-clown/rosetta"
)

type stream struct {
	ctx       context.Context
	w         io.Writer
	writer    *sse.SSEWriter
	now       func() time.Time
	threadID  string
	runID     string
	text      string
	reasoning string
	nReason   int
}

func newStream(ctx context.Context, w io.Writer, now func() time.Time, threadID, runID string) *stream {
	return &stream{ctx: ctx, w: w, writer: sse.NewSSEWriter(), now: now, threadID: threadID, runID: runID}
}

func (s *stream) write(evs ...events.Event) error {
	for _, e := range evs {
		e.SetTimestamp(s.now().UnixMilli())
		if err := s.writer.WriteEvent(s.ctx, s.w, e); err != nil {
			return err
		}
	}
	return nil
}

func (s *stream) start() error {
	return s.write(events.NewRunStartedEvent(s.threadID, s.runID))
}

func (s *stream) closeOpen() error {
	if s.text != "" {
		id := s.text
		s.text = ""
		if err := s.write(events.NewTextMessageEndEvent(id)); err != nil {
			return err
		}
	}
	if s.reasoning != "" {
		id := s.reasoning
		s.reasoning = ""
		return s.write(events.NewReasoningMessageEndEvent(id), events.NewReasoningEndEvent(id))
	}
	return nil
}

func (s *stream) send(e rosetta.Event) error {
	switch e := e.(type) {
	case rosetta.TextDelta:
		if s.text != e.MessageID {
			if err := s.closeOpen(); err != nil {
				return err
			}
			s.text = e.MessageID
			if err := s.write(events.NewTextMessageStartEvent(e.MessageID, events.WithRole("assistant"))); err != nil {
				return err
			}
		}
		return s.write(events.NewTextMessageContentEvent(e.MessageID, e.Text))
	case rosetta.ReasoningDelta:
		if s.reasoning == "" {
			if err := s.closeOpen(); err != nil {
				return err
			}
			s.nReason++
			s.reasoning = fmt.Sprintf("%s-reasoning-%d", s.runID, s.nReason)
			if err := s.write(events.NewReasoningStartEvent(s.reasoning), events.NewReasoningMessageStartEvent(s.reasoning, "reasoning")); err != nil {
				return err
			}
		}
		return s.write(events.NewReasoningMessageContentEvent(s.reasoning, e.Text))
	}
	if err := s.closeOpen(); err != nil {
		return err
	}
	switch e := e.(type) {
	case rosetta.ToolCall:
		return s.write(
			events.NewToolCallStartEvent(e.ID, e.Name),
			events.NewToolCallArgsEvent(e.ID, string(e.Args)),
			events.NewToolCallEndEvent(e.ID),
		)
	case rosetta.ToolResult:
		return s.write(events.NewToolCallResultEvent(e.ID+"-result", e.ID, string(e.Result)))
	case rosetta.Step:
		if e.Finished {
			return s.write(events.NewStepFinishedEvent(e.Name))
		}
		return s.write(events.NewStepStartedEvent(e.Name))
	case rosetta.StateSnapshot:
		return s.write(events.NewStateSnapshotEvent(e.State))
	case rosetta.Artifact:
		return s.write(events.NewCustomEvent("artifact", events.WithValue(artifact(e))))
	case rosetta.Extension:
		if e.Protocol == "agui" {
			return s.write(events.NewRawEvent(e.Value))
		}
	}
	return nil
}

func (s *stream) finish(runErr error) error {
	if err := s.closeOpen(); err != nil {
		return err
	}
	if runErr != nil {
		return s.write(events.NewRunErrorEvent(runErr.Error(), events.WithRunID(s.runID)))
	}
	return s.write(events.NewRunFinishedEvent(s.threadID, s.runID))
}

type wirePart struct {
	Text string          `json:"text,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

type wireArtifact struct {
	Name  string     `json:"name"`
	Parts []wirePart `json:"parts"`
}

func artifact(a rosetta.Artifact) wireArtifact {
	out := wireArtifact{Name: a.Name}
	for _, p := range a.Parts {
		out.Parts = append(out.Parts, wirePart{Text: p.Text, Data: p.Data})
	}
	return out
}
