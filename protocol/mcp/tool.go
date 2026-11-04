package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/protocol"
)

func tools(agents protocol.Registry, run protocol.Runner) []server.ServerTool {
	var out []server.ServerTool
	for _, a := range agents.List() {
		card := a.Card()
		tool := mcp.NewTool(card.Name,
			mcp.WithDescription(card.Description),
			mcp.WithString("message", mcp.Required(), mcp.Description("What to send to the agent.")),
			mcp.WithString("session_id", mcp.Description("Continue an earlier conversation with this ID.")),
		)
		out = append(out, server.ServerTool{Tool: tool, Handler: handler(a, run)})
	}
	return out
}

func handler(agent rosetta.Agent, run protocol.Runner) server.ToolHandlerFunc {
	return func(ctx context.Context, call mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		req := &rosetta.Request{SessionID: call.GetString("session_id", "")}
		if msg := call.GetString("message", ""); msg != "" {
			req.Input = []rosetta.Part{{Text: msg}}
		}
		if c, ok := ctx.Value(callerKey{}).(rosetta.Caller); ok {
			req.Caller = c
		}
		r := &result{ctx: ctx, srv: server.ServerFromContext(ctx)}
		if call.Params.Meta != nil {
			r.token = call.Params.Meta.ProgressToken
		}
		if err := run.Run(ctx, agent, req, r.send); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return r.toolResult(), nil
	}
}

type result struct {
	ctx        context.Context
	srv        *server.MCPServer
	token      mcp.ProgressToken
	progress   int
	text       strings.Builder
	extra      []mcp.Content
	structured map[string]json.RawMessage
}

func (r *result) notify(message string) {
	if r.token == nil || r.srv == nil {
		return
	}
	r.progress++
	_ = r.srv.SendNotificationToClient(r.ctx, "notifications/progress", map[string]any{
		"progressToken": r.token,
		"progress":      r.progress,
		"message":       message,
	})
}

func (r *result) send(e rosetta.Event) error {
	switch e := e.(type) {
	case rosetta.TextDelta:
		r.text.WriteString(e.Text)
		r.notify(e.Text)
	case rosetta.ToolCall:
		r.notify("calling " + e.Name)
	case rosetta.Step:
		if e.Finished {
			r.notify(e.Name + " finished")
		} else {
			r.notify(e.Name + " started")
		}
	case rosetta.Artifact:
		for _, p := range e.Parts {
			if p.Text != "" {
				r.extra = append(r.extra, mcp.NewTextContent(p.Text))
			}
			if p.Data != nil {
				if r.structured == nil {
					r.structured = map[string]json.RawMessage{}
				}
				r.structured[e.Name] = p.Data
			}
		}
	case rosetta.Extension:
		if c, ok := e.Value.(mcp.Content); ok && e.Protocol == "mcp" {
			r.extra = append(r.extra, c)
		}
	}
	return nil
}

func (r *result) toolResult() *mcp.CallToolResult {
	res := mcp.NewToolResultText(r.text.String())
	res.Content = append(res.Content, r.extra...)
	if r.structured != nil {
		res.StructuredContent = r.structured
	}
	return res
}
