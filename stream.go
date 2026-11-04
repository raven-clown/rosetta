package rosetta

import "encoding/json"

// Stream is how an agent sends events. Send returns an error once the
// client is gone, so long loops can stop early.
type Stream interface {
	Send(Event) error
	Text(s string) error
	Reasoning(s string) error
	ToolCall(id, name string, args json.RawMessage) error
	ToolResult(id string, result json.RawMessage, isError bool) error
}

// NewStream returns a Stream that passes every event to send.
func NewStream(send func(Event) error) Stream {
	return stream(send)
}

type stream func(Event) error

func (s stream) Send(e Event) error { return s(e) }

func (s stream) Text(t string) error { return s(TextDelta{Text: t}) }

func (s stream) Reasoning(t string) error { return s(ReasoningDelta{Text: t}) }

func (s stream) ToolCall(id, name string, args json.RawMessage) error {
	return s(ToolCall{ID: id, Name: name, Args: args})
}

func (s stream) ToolResult(id string, result json.RawMessage, isError bool) error {
	return s(ToolResult{ID: id, Result: result, IsError: isError})
}
