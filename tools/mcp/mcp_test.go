package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type seen struct {
	mu sync.Mutex
	h  []http.Header
}

func (s *seen) add(h http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.h = append(s.h, h.Clone())
}

func (s *seen) all() []http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.h)
}

func start(t *testing.T) (*Source, *seen) {
	t.Helper()
	srv := server.NewMCPServer("tools", "1.0.0", server.WithToolCapabilities(false))
	add := func(name string, h server.ToolHandlerFunc) {
		srv.AddTool(mcp.NewTool(name, mcp.WithDescription(name+" tool"), mcp.WithString("q")), h)
	}
	add("github__search", func(_ context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("found " + r.GetString("q", "")), nil
	})
	add("github__issue", func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultError("no access"), nil
	})
	add("logbook__recall", func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{}, StructuredContent: map[string]any{"n": 1}}, nil
	})
	s := &seen{}
	h := server.NewStreamableHTTPServer(srv)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.add(r.Header)
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return &Source{URL: ts.URL + "/mcp", HTTP: ts.Client()}, s
}

func open(t *testing.T, src *Source, h http.Header) *Session {
	t.Helper()
	s, err := src.Open(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestList(t *testing.T) {
	src, _ := start(t)
	s := open(t, src, nil)
	tests := map[string]struct {
		globs []string
		want  []string
	}{
		"all":      {[]string{"*"}, []string{"github__issue", "github__search", "logbook__recall"}},
		"prefix":   {[]string{"github__*"}, []string{"github__issue", "github__search"}},
		"exact":    {[]string{"logbook__recall", "nope"}, []string{"logbook__recall"}},
		"none":     {nil, nil},
		"bad glob": {[]string{"["}, nil},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tools, err := s.List(t.Context(), tt.globs)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, tool := range tools {
				names = append(names, tool.Name)
				if tool.Description != tool.Name+" tool" {
					t.Errorf("description = %q", tool.Description)
				}
				var schema map[string]any
				if err := json.Unmarshal(tool.Schema, &schema); err != nil || schema["type"] != "object" {
					t.Errorf("schema = %s", tool.Schema)
				}
			}
			slices.Sort(names)
			if !reflect.DeepEqual(names, tt.want) {
				t.Errorf("tools = %v, want %v", names, tt.want)
			}
		})
	}
}

func TestCall(t *testing.T) {
	src, _ := start(t)
	s := open(t, src, nil)
	tests := map[string]struct {
		tool    string
		text    string
		isError bool
	}{
		"text":       {"github__search", "found go", false},
		"error":      {"github__issue", "no access", true},
		"structured": {"logbook__recall", `{"n":1}`, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			text, isError, err := s.Call(t.Context(), tt.tool, json.RawMessage(`{"q":"go"}`))
			if err != nil {
				t.Fatal(err)
			}
			if text != tt.text || isError != tt.isError {
				t.Errorf("got %q %v, want %q %v", text, isError, tt.text, tt.isError)
			}
		})
	}
	if _, _, err := s.Call(t.Context(), "github__search", json.RawMessage(`not json`)); err == nil {
		t.Error("want an error for bad arguments")
	}
}

func TestHeaders(t *testing.T) {
	tests := map[string]struct {
		key    string
		header http.Header
		want   string
	}{
		"caller":   {"key", http.Header{"Authorization": {"Bearer tok"}, "X-Project": {"p"}}, "Bearer tok"},
		"fallback": {"key", http.Header{"X-Project": {"p"}}, "Bearer key"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			src, s := start(t)
			src.APIKey = tt.key
			sess := open(t, src, tt.header)
			if _, err := sess.List(t.Context(), []string{"*"}); err != nil {
				t.Fatal(err)
			}
			all := s.all()
			if len(all) == 0 {
				t.Fatal("no requests")
			}
			for _, h := range all {
				if h.Get("Authorization") != tt.want || h.Get("X-Project") != "p" {
					t.Errorf("headers = %v", h)
				}
			}
		})
	}
}

func TestUnreachable(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	ts.Close()
	_, err := (&Source{URL: ts.URL}).Open(t.Context(), nil)
	if err == nil || !strings.HasPrefix(err.Error(), "airlock: ") {
		t.Errorf("err = %v", err)
	}
}
