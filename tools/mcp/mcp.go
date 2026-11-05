// Package mcp is a tool source backed by an MCP server over Streamable
// HTTP, such as airlock.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/raven-clown/rosetta"
)

// Tool is a tool the server offers.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Source connects to the MCP server at URL. APIKey is sent only when the
// headers passed to Open carry no Authorization.
type Source struct {
	URL    string
	APIKey string
	HTTP   *http.Client
}

// Session is one connection, opened per run so the server sees each
// run's caller.
type Session struct {
	c *client.Client
}

// Open connects and initializes.
func (s *Source) Open(ctx context.Context, h http.Header) (*Session, error) {
	headers := map[string]string{}
	for k := range h {
		headers[k] = h.Get(k)
	}
	if headers["Authorization"] == "" && s.APIKey != "" {
		headers["Authorization"] = "Bearer " + s.APIKey
	}
	opts := []transport.StreamableHTTPCOption{transport.WithHTTPHeaders(headers)}
	if s.HTTP != nil {
		opts = append(opts, transport.WithHTTPBasicClient(s.HTTP))
	}
	c, err := client.NewStreamableHttpClient(s.URL, opts...)
	if err != nil {
		return nil, wrap(err)
	}
	if err := c.Start(ctx); err != nil {
		return nil, wrap(err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ClientInfo = mcp.Implementation{Name: "rosetta", Version: rosetta.Version()}
	if _, err := c.Initialize(ctx, init); err != nil {
		_ = c.Close()
		return nil, wrap(err)
	}
	return &Session{c: c}, nil
}

// List returns the tools whose names match any of globs.
func (s *Session) List(ctx context.Context, globs []string) ([]Tool, error) {
	if len(globs) == 0 {
		return nil, nil
	}
	res, err := s.c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, wrap(err)
	}
	var tools []Tool
	for _, t := range res.Tools {
		if !matches(globs, t.Name) {
			continue
		}
		schema, err := inputSchema(t)
		if err != nil {
			return nil, wrap(err)
		}
		tools = append(tools, Tool{Name: t.Name, Description: t.Description, Schema: schema})
	}
	return tools, nil
}

func matches(globs []string, name string) bool {
	for _, g := range globs {
		if ok, err := path.Match(g, name); err == nil && ok {
			return true
		}
	}
	return false
}

func inputSchema(t mcp.Tool) (json.RawMessage, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	var v struct {
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return v.InputSchema, nil
}

// Call calls a tool and returns its text, or its structured content as
// JSON when it has no text.
func (s *Session) Call(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	var arguments map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &arguments); err != nil {
			return "", false, fmt.Errorf("tool %s: arguments are not a JSON object: %w", name, err)
		}
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = arguments
	res, err := s.c.CallTool(ctx, req)
	if err != nil {
		return "", false, wrap(err)
	}
	var texts []string
	for _, c := range res.Content {
		if t, ok := c.(mcp.TextContent); ok {
			texts = append(texts, t.Text)
		}
	}
	if len(texts) == 0 && res.StructuredContent != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return "", false, wrap(err)
		}
		texts = append(texts, string(b))
	}
	return strings.Join(texts, "\n"), res.IsError, nil
}

// Close ends the session.
func (s *Session) Close() error { return s.c.Close() }

func wrap(err error) error { return fmt.Errorf("airlock: %w", err) }
