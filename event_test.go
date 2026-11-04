package rosetta

import "testing"

func TestEventsAreEvents(t *testing.T) {
	events := []Event{
		TextDelta{}, ReasoningDelta{}, ToolCall{}, ToolResult{}, Step{},
		StateSnapshot{}, Artifact{}, Usage{}, Extension{},
	}
	for _, e := range events {
		e.isEvent()
	}
	if RoleUser != "user" || RoleAgent != "agent" || RoleSystem != "system" {
		t.Error("unexpected role values")
	}
	if ErrRunEnded.Error() != "rosetta: run has ended" {
		t.Errorf("ErrRunEnded = %q", ErrRunEnded)
	}
}
