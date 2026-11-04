package rosetta

import "encoding/json"

// Event is something an agent sends during a run. The set is closed: only
// the types in this package implement it.
type Event interface {
	isEvent()
}

// TextDelta is a piece of the agent's answer.
type TextDelta struct {
	MessageID string
	Text      string
}

// ReasoningDelta is a piece of the agent's visible reasoning.
type ReasoningDelta struct {
	Text string
}

// ToolCall reports a tool the agent is calling.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// ToolResult reports the result of a tool call.
type ToolResult struct {
	ID      string
	Result  json.RawMessage
	IsError bool
}

// Step marks the start or end of a named step.
type Step struct {
	Name     string
	Finished bool
}

// StateSnapshot replaces the shared state the client holds.
type StateSnapshot struct {
	State json.RawMessage
}

// Artifact is a named output, such as a file or a JSON document.
type Artifact struct {
	Name  string
	Parts []Part
}

// Usage reports tokens used by a model call.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
}

// Extension carries a payload that only the named protocol adapter passes on.
type Extension struct {
	Protocol string
	Value    any
}

func (TextDelta) isEvent()      {}
func (ReasoningDelta) isEvent() {}
func (ToolCall) isEvent()       {}
func (ToolResult) isEvent()     {}
func (Step) isEvent()           {}
func (StateSnapshot) isEvent()  {}
func (Artifact) isEvent()       {}
func (Usage) isEvent()          {}
func (Extension) isEvent()      {}
