package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/llm/openai"
	"github.com/raven-clown/rosetta/tools/mcp"
)

type fakeChat struct {
	replies []openai.Response
	err     error
	reqs    []openai.Request
	headers []http.Header
}

func (f *fakeChat) Stream(_ context.Context, req openai.Request, h http.Header, onText func(string) error) (openai.Response, error) {
	f.reqs = append(f.reqs, req)
	f.headers = append(f.headers, h)
	if f.err != nil {
		return openai.Response{}, f.err
	}
	r := f.replies[0]
	if len(f.replies) > 1 {
		f.replies = f.replies[1:]
	}
	if r.Message.Content != "" {
		if err := onText(r.Message.Content); err != nil {
			return openai.Response{}, err
		}
	}
	return r, nil
}

type fakeTools struct {
	opened  int
	globs   []string
	calls   []string
	isError bool
	callErr error
	openErr error
	closed  bool
}

func (f *fakeTools) Open(context.Context, http.Header) (ToolSession, error) {
	f.opened++
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f, nil
}

func (f *fakeTools) List(_ context.Context, globs []string) ([]mcp.Tool, error) {
	f.globs = globs
	return []mcp.Tool{{Name: "search", Description: "find things", Schema: json.RawMessage(`{"type":"object"}`)}}, nil
}

func (f *fakeTools) Call(_ context.Context, name string, args json.RawMessage) (string, bool, error) {
	f.calls = append(f.calls, name+string(args))
	return "result for " + string(args), f.isError, f.callErr
}

func (f *fakeTools) Close() error {
	f.closed = true
	return nil
}

func answer(text string, in, out int) openai.Response {
	return openai.Response{Message: openai.Message{Role: "assistant", Content: text}, InputTokens: in, OutputTokens: out}
}

func toolCall(args string) openai.Response {
	return openai.Response{
		Message:     openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "c1", Name: "search", Arguments: args}}},
		InputTokens: 1, OutputTokens: 2,
	}
}

func runAgent(t *testing.T, a rosetta.Agent, req *rosetta.Request) ([]rosetta.Event, error) {
	t.Helper()
	var events []rosetta.Event
	err := a.Run(t.Context(), req, rosetta.NewStream(func(e rosetta.Event) error {
		events = append(events, e)
		return nil
	}))
	return events, err
}

func input(text string) *rosetta.Request {
	return &rosetta.Request{Input: []rosetta.Part{{Text: text}}}
}

func TestPlainAnswer(t *testing.T) {
	chat := &fakeChat{replies: []openai.Response{answer("hello", 3, 4)}}
	tools := &fakeTools{}
	a := New(Config{Name: "helper", Description: "helps", Model: "m"}, chat, tools)
	if c := a.Card(); c.Name != "helper" || c.Description != "helps" {
		t.Errorf("card = %+v", c)
	}
	events, err := runAgent(t, a, input("hi"))
	if err != nil {
		t.Fatal(err)
	}
	want := []rosetta.Event{rosetta.TextDelta{Text: "hello"}, rosetta.Usage{InputTokens: 3, OutputTokens: 4}}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events = %+v", events)
	}
	if tools.opened != 0 {
		t.Error("tools opened without tool globs")
	}
	if len(chat.reqs[0].Tools) != 0 {
		t.Error("tools sent without tool globs")
	}
}

func TestToolLoop(t *testing.T) {
	chat := &fakeChat{replies: []openai.Response{toolCall(`{"q":"go"}`), answer("done", 5, 6)}}
	tools := &fakeTools{}
	a := New(Config{Name: "helper", Model: "m", Tools: []string{"search*"}}, chat, tools)
	events, err := runAgent(t, a, input("find go"))
	if err != nil {
		t.Fatal(err)
	}
	want := []rosetta.Event{
		rosetta.Usage{InputTokens: 1, OutputTokens: 2},
		rosetta.ToolCall{ID: "c1", Name: "search", Args: json.RawMessage(`{"q":"go"}`)},
		rosetta.ToolResult{ID: "c1", Result: json.RawMessage(`{"text":"result for {\"q\":\"go\"}"}`)},
		rosetta.TextDelta{Text: "done"},
		rosetta.Usage{InputTokens: 5, OutputTokens: 6},
	}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events =\n%+v\nwant\n%+v", events, want)
	}
	if !reflect.DeepEqual(tools.globs, []string{"search*"}) || !tools.closed || tools.opened != 1 {
		t.Errorf("tools = %+v", tools)
	}
	if got := chat.reqs[0].Tools; len(got) != 1 || got[0].Name != "search" || string(got[0].Parameters) != `{"type":"object"}` {
		t.Errorf("tools sent = %+v", got)
	}
	second := chat.reqs[1].Messages
	last := second[len(second)-2:]
	if last[0].Role != "assistant" || len(last[0].ToolCalls) != 1 || last[1].Role != "tool" || last[1].ToolCallID != "c1" || last[1].Content != `result for {"q":"go"}` {
		t.Errorf("messages after tool call = %+v", last)
	}
}

func TestMessagesBuilt(t *testing.T) {
	chat := &fakeChat{replies: []openai.Response{answer("ok", 0, 0)}}
	a := New(Config{Name: "a", Model: "m", SystemPrompt: "be brief"}, chat, nil)
	req := &rosetta.Request{
		History: []rosetta.Message{
			{Role: rosetta.RoleSystem, Parts: []rosetta.Part{{Text: "earlier rule"}}},
			{Role: rosetta.RoleUser, Parts: []rosetta.Part{{Text: "one"}, {Data: json.RawMessage(`{"x":1}`)}}},
			{Role: rosetta.RoleAgent, Parts: []rosetta.Part{{Text: "two"}}},
		},
		Input: []rosetta.Part{{Text: "three"}},
	}
	events, err := runAgent(t, a, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("events = %+v, want no usage event for zero counts", events)
	}
	want := []openai.Message{
		{Role: "system", Content: "be brief"},
		{Role: "system", Content: "earlier rule"},
		{Role: "user", Content: "one\n{\"x\":1}"},
		{Role: "assistant", Content: "two"},
		{Role: "user", Content: "three"},
	}
	if got := chat.reqs[0].Messages; !reflect.DeepEqual(got, want) {
		t.Errorf("messages =\n%+v\nwant\n%+v", got, want)
	}
	if chat.reqs[0].Model != "m" {
		t.Errorf("model = %q", chat.reqs[0].Model)
	}
}

func TestStepLimit(t *testing.T) {
	chat := &fakeChat{replies: []openai.Response{toolCall(`{}`)}}
	a := New(Config{Name: "a", Model: "m", Tools: []string{"*"}, MaxSteps: 2}, chat, &fakeTools{})
	_, err := runAgent(t, a, input("x"))
	if err == nil || !strings.Contains(err.Error(), "step limit of 2") {
		t.Errorf("err = %v", err)
	}
	if len(chat.reqs) != 2 {
		t.Errorf("model calls = %d, want 2", len(chat.reqs))
	}
}

func TestDefaultStepLimit(t *testing.T) {
	chat := &fakeChat{replies: []openai.Response{toolCall(`{}`)}}
	a := New(Config{Name: "a", Model: "m", Tools: []string{"*"}}, chat, &fakeTools{})
	if _, err := runAgent(t, a, input("x")); err == nil {
		t.Fatal("want an error")
	}
	if len(chat.reqs) != 8 {
		t.Errorf("model calls = %d, want 8", len(chat.reqs))
	}
}

func TestToolResults(t *testing.T) {
	errDown := errors.New("airlock: down")
	tests := map[string]struct {
		args    string
		tools   *fakeTools
		wantErr error
		result  string
		isError bool
	}{
		"error result": {`{}`, &fakeTools{isError: true}, nil, `{"text":"result for {}"}`, true},
		"bad args":     {`{oops`, &fakeTools{}, nil, `{"text":"arguments are not valid JSON"}`, true},
		"call fails":   {`{}`, &fakeTools{callErr: errDown}, errDown, "", false},
		"open fails":   {`{}`, &fakeTools{openErr: errDown}, errDown, "", false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			chat := &fakeChat{replies: []openai.Response{toolCall(tt.args), answer("ok", 0, 0)}}
			a := New(Config{Name: "a", Model: "m", Tools: []string{"*"}}, chat, tt.tools)
			events, err := runAgent(t, a, input("x"))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var res rosetta.ToolResult
			var call rosetta.ToolCall
			for _, e := range events {
				switch e := e.(type) {
				case rosetta.ToolResult:
					res = e
				case rosetta.ToolCall:
					call = e
				}
			}
			if string(res.Result) != tt.result || res.IsError != tt.isError {
				t.Errorf("result = %s %v", res.Result, res.IsError)
			}
			if !json.Valid(call.Args) {
				t.Errorf("tool call args %q are not JSON", call.Args)
			}
		})
	}
}

func TestUpstreamError(t *testing.T) {
	errDown := errors.New("transponder: down")
	a := New(Config{Name: "a", Model: "m"}, &fakeChat{err: errDown}, nil)
	if _, err := runAgent(t, a, input("x")); !errors.Is(err, errDown) {
		t.Errorf("err = %v", err)
	}
}

func TestNilToolsWithGlobs(t *testing.T) {
	chat := &fakeChat{replies: []openai.Response{answer("ok", 0, 0)}}
	a := New(Config{Name: "a", Model: "m", Tools: []string{"*"}}, chat, nil)
	if _, err := runAgent(t, a, input("x")); err != nil {
		t.Fatal(err)
	}
}

func TestHeadersForwarded(t *testing.T) {
	chat := &fakeChat{replies: []openai.Response{answer("ok", 0, 0)}}
	a := New(Config{Name: "a", Model: "m"}, chat, nil)
	req := input("x")
	req.Caller = rosetta.Caller{Token: "tok", RequestID: "r1", Project: "p"}
	if _, err := runAgent(t, a, req); err != nil {
		t.Fatal(err)
	}
	h := chat.headers[0]
	if h.Get("Authorization") != "Bearer tok" || h.Get("X-Request-ID") != "r1" || h.Get("X-Project") != "p" {
		t.Errorf("headers = %v", h)
	}
}

func TestEndToEnd(t *testing.T) {
	step := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		step++
		if step == 1 {
			_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"github__search","arguments":"{\"q\":\"go\"}"}}]}}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"found it"}}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()

	srv := server.NewMCPServer("airlock", "1.0.0")
	srv.AddTool(mcpgo.NewTool("github__search", mcpgo.WithString("q")), func(_ context.Context, r mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultText("repo " + r.GetString("q", "")), nil
	})
	tools := httptest.NewServer(server.NewStreamableHTTPServer(srv))
	defer tools.Close()

	a := New(Config{Name: "helper", Model: "m", Tools: []string{"github__*"}},
		&openai.Client{URL: model.URL, HTTP: model.Client()},
		MCPTools(&mcp.Source{URL: tools.URL + "/mcp", HTTP: tools.Client()}))
	req := input("search go")
	req.Caller.Token = "tok"
	events, err := runAgent(t, a, req)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		switch e := e.(type) {
		case rosetta.ToolResult:
			got = append(got, string(e.Result))
		case rosetta.TextDelta:
			got = append(got, e.Text)
		}
	}
	if !reflect.DeepEqual(got, []string{`{"text":"repo go"}`, "found it"}) {
		t.Errorf("got %q", got)
	}
}
