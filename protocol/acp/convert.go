package acp

import (
	"encoding/json"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/raven-clown/rosetta"
)

func decode(raw json.RawMessage) any {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return v
}

func updates(e rosetta.Event) []acpsdk.SessionUpdate {
	switch e := e.(type) {
	case rosetta.TextDelta:
		return []acpsdk.SessionUpdate{acpsdk.UpdateAgentMessageText(e.Text)}
	case rosetta.ReasoningDelta:
		return []acpsdk.SessionUpdate{acpsdk.UpdateAgentThoughtText(e.Text)}
	case rosetta.ToolCall:
		return []acpsdk.SessionUpdate{acpsdk.StartToolCall(acpsdk.ToolCallId(e.ID), e.Name,
			acpsdk.WithStartKind(acpsdk.ToolKindOther),
			acpsdk.WithStartStatus(acpsdk.ToolCallStatusInProgress),
			acpsdk.WithStartRawInput(decode(e.Args)),
		)}
	case rosetta.ToolResult:
		status := acpsdk.ToolCallStatusCompleted
		if e.IsError {
			status = acpsdk.ToolCallStatusFailed
		}
		return []acpsdk.SessionUpdate{acpsdk.UpdateToolCall(acpsdk.ToolCallId(e.ID),
			acpsdk.WithUpdateStatus(status),
			acpsdk.WithUpdateRawOutput(decode(e.Result)),
		)}
	case rosetta.Artifact:
		var out []acpsdk.SessionUpdate
		for _, p := range e.Parts {
			if p.Text != "" {
				out = append(out, acpsdk.UpdateAgentMessageText(p.Text))
			}
			if p.Data != nil {
				out = append(out, acpsdk.UpdateAgentMessageText(string(p.Data)))
			}
		}
		return out
	case rosetta.Extension:
		if u, ok := e.Value.(acpsdk.SessionUpdate); ok && e.Protocol == "acp" {
			return []acpsdk.SessionUpdate{u}
		}
	}
	return nil
}
