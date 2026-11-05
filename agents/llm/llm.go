// Package llm is the built-in tool-calling agent: it calls a model through
// an OpenAI-compatible endpoint and tools through an MCP server.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/internal/httpx"
	"github.com/raven-clown/rosetta/llm/openai"
	"github.com/raven-clown/rosetta/tools/mcp"
)

// Chat streams one chat completion. *openai.Client implements it.
type Chat interface {
	Stream(ctx context.Context, req openai.Request, h http.Header, onText func(string) error) (openai.Response, error)
}

// Tools opens a tool session for one run.
type Tools interface {
	Open(ctx context.Context, h http.Header) (ToolSession, error)
}

// ToolSession lists and calls tools during one run.
type ToolSession interface {
	List(ctx context.Context, globs []string) ([]mcp.Tool, error)
	Call(ctx context.Context, name string, args json.RawMessage) (string, bool, error)
	Close() error
}

// MCPTools adapts an MCP tool source to Tools.
func MCPTools(s *mcp.Source) Tools { return mcpTools{s} }

type mcpTools struct{ s *mcp.Source }

func (m mcpTools) Open(ctx context.Context, h http.Header) (ToolSession, error) {
	return m.s.Open(ctx, h)
}

// Config describes one llm agent. MaxSteps 0 means 8.
type Config struct {
	Name         string
	Description  string
	Model        string
	SystemPrompt string
	Tools        []string
	MaxSteps     int
}

// New returns the agent. tools may be nil, and the agent then runs
// without tools.
func New(cfg Config, chat Chat, tools Tools) rosetta.Agent {
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 8
	}
	return &agent{cfg: cfg, chat: chat, tools: tools}
}

type agent struct {
	cfg   Config
	chat  Chat
	tools Tools
}

func (a *agent) Card() rosetta.Card {
	return rosetta.Card{Name: a.cfg.Name, Description: a.cfg.Description}
}

func (a *agent) Run(ctx context.Context, req *rosetta.Request, out rosetta.Stream) error {
	h := httpx.Outbound(req.Caller, "")
	creq := openai.Request{Model: a.cfg.Model, Messages: messages(a.cfg.SystemPrompt, req)}

	var session ToolSession
	allowed := map[string]bool{}
	if a.tools != nil && len(a.cfg.Tools) > 0 {
		s, err := a.tools.Open(ctx, h)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		list, err := s.List(ctx, a.cfg.Tools)
		if err != nil {
			return err
		}
		for _, t := range list {
			creq.Tools = append(creq.Tools, openai.Tool{Name: t.Name, Description: t.Description, Parameters: t.Schema})
			allowed[t.Name] = true
		}
		session = s
	}

	for range a.cfg.MaxSteps {
		res, err := a.chat.Stream(ctx, creq, h, out.Text)
		if err != nil {
			return err
		}
		if res.InputTokens != 0 || res.OutputTokens != 0 {
			if err := out.Send(rosetta.Usage{InputTokens: int64(res.InputTokens), OutputTokens: int64(res.OutputTokens)}); err != nil {
				return err
			}
		}
		if len(res.Message.ToolCalls) == 0 {
			return nil
		}
		creq.Messages = append(creq.Messages, res.Message)
		for _, call := range res.Message.ToolCalls {
			msg, err := a.callTool(ctx, session, allowed, call, out)
			if err != nil {
				return err
			}
			creq.Messages = append(creq.Messages, msg)
		}
	}
	return fmt.Errorf("llm: step limit of %d reached", a.cfg.MaxSteps)
}

func (a *agent) callTool(ctx context.Context, s ToolSession, allowed map[string]bool, call openai.ToolCall, out rosetta.Stream) (openai.Message, error) {
	args := json.RawMessage(call.Arguments)
	valid := json.Valid(args)
	if !valid {
		args, _ = json.Marshal(call.Arguments)
	}
	if err := out.ToolCall(call.ID, call.Name, args); err != nil {
		return openai.Message{}, err
	}
	var text string
	var isError bool
	switch {
	case !valid:
		text, isError = "arguments are not valid JSON", true
	case !allowed[call.Name]:
		text, isError = "unknown tool "+call.Name, true
	default:
		var err error
		if text, isError, err = s.Call(ctx, call.Name, args); err != nil {
			return openai.Message{}, err
		}
	}
	result, _ := json.Marshal(map[string]string{"text": text})
	if err := out.ToolResult(call.ID, result, isError); err != nil {
		return openai.Message{}, err
	}
	return openai.Message{Role: "tool", ToolCallID: call.ID, Content: text}, nil
}

var roles = map[rosetta.Role]string{
	rosetta.RoleUser:   "user",
	rosetta.RoleAgent:  "assistant",
	rosetta.RoleSystem: "system",
}

func messages(system string, req *rosetta.Request) []openai.Message {
	var msgs []openai.Message
	if system != "" {
		msgs = append(msgs, openai.Message{Role: "system", Content: system})
	}
	for _, m := range req.History {
		role, ok := roles[m.Role]
		if !ok {
			role = "user"
		}
		msgs = append(msgs, openai.Message{Role: role, Content: text(m.Parts)})
	}
	return append(msgs, openai.Message{Role: "user", Content: text(req.Input)})
}

func text(parts []rosetta.Part) string {
	var s []string
	for _, p := range parts {
		switch {
		case p.Text != "":
			s = append(s, p.Text)
		case len(p.Data) > 0:
			s = append(s, string(p.Data))
		}
	}
	return strings.Join(s, "\n")
}
