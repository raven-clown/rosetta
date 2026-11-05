// Package openai is a small client for OpenAI-compatible streaming chat
// completions, used to reach transponder or any other compatible endpoint.
package openai

import (
	"encoding/json"
	"net/http"
)

// Message is one chat message.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall is a function call the model asked for.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type wireFunction struct {
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Arguments   *string         `json:"arguments,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type wireToolCall struct {
	Index    int          `json:"index"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function wireFunction `json:"function"`
}

// MarshalJSON writes the OpenAI shape.
func (c ToolCall) MarshalJSON() ([]byte, error) {
	args := c.Arguments
	return json.Marshal(struct {
		ID       string       `json:"id"`
		Type     string       `json:"type"`
		Function wireFunction `json:"function"`
	}{c.ID, "function", wireFunction{Name: c.Name, Arguments: &args}})
}

// Tool is a function the model may call.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// MarshalJSON writes the OpenAI shape.
func (t Tool) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type     string       `json:"type"`
		Function wireFunction `json:"function"`
	}{"function", wireFunction{Name: t.Name, Description: t.Description, Parameters: t.Parameters}})
}

// Request is one chat completion call.
type Request struct {
	Model    string
	Messages []Message
	Tools    []Tool
}

// Response is the assembled reply and its token counts.
type Response struct {
	Message      Message
	InputTokens  int
	OutputTokens int
}

// Client calls URL + "/chat/completions". APIKey is sent only when the
// headers passed to Stream carry no Authorization.
type Client struct {
	URL    string
	APIKey string
	HTTP   *http.Client
}
