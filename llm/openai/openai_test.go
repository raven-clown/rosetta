package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type fake struct {
	status  int
	body    string
	chunks  []string
	trailer map[string]string
	got     *http.Request
	gotBody map[string]any
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.got = r
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &f.gotBody)
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.body)
		return
	}
	for k := range f.trailer {
		w.Header().Add("Trailer", k)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range f.chunks {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", c)
	}
	for k, v := range f.trailer {
		w.Header().Set(k, v)
	}
}

func start(t *testing.T, f *fake) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &Client{URL: srv.URL + "/v1", HTTP: srv.Client()}
}

func text(s string) string {
	b, _ := json.Marshal(s)
	return `{"choices":[{"index":0,"delta":{"content":` + string(b) + `}}]}`
}

func TestStreamText(t *testing.T) {
	f := &fake{chunks: []string{text("Hel"), text("lo"), text("!"), "[DONE]"}}
	c := start(t, f)
	var got []string
	res, err := c.Stream(t.Context(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, nil, func(s string) error {
		got = append(got, s)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"Hel", "lo", "!"}) {
		t.Errorf("deltas = %q", got)
	}
	if res.Message.Role != "assistant" || res.Message.Content != "Hello!" {
		t.Errorf("message = %+v", res.Message)
	}
	if f.got.URL.Path != "/v1/chat/completions" {
		t.Errorf("path = %s", f.got.URL.Path)
	}
}

func TestStreamToolCalls(t *testing.T) {
	f := &fake{chunks: []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"search","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"c2","type":"function","function":{"name":"time","arguments":"{}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"go\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"[DONE]",
	}}
	c := start(t, f)
	res, err := c.Stream(t.Context(), Request{Model: "m"}, nil, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []ToolCall{{ID: "c1", Name: "search", Arguments: `{"q":"go"}`}, {ID: "c2", Name: "time", Arguments: "{}"}}
	if !reflect.DeepEqual(res.Message.ToolCalls, want) {
		t.Errorf("tool calls = %+v", res.Message.ToolCalls)
	}
}

func TestStreamUsage(t *testing.T) {
	usage := `{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4}}`
	tests := map[string]struct {
		chunks  []string
		trailer map[string]string
		in, out int
	}{
		"trailers": {[]string{text("a"), "[DONE]"}, map[string]string{"X-Usage-Input-Tokens": "10", "X-Usage-Output-Tokens": "20"}, 10, 20},
		"chunk":    {[]string{text("a"), usage, "[DONE]"}, nil, 3, 4},
		"both":     {[]string{text("a"), usage, "[DONE]"}, map[string]string{"X-Usage-Input-Tokens": "10", "X-Usage-Output-Tokens": "20"}, 10, 20},
		"none":     {[]string{text("a"), "[DONE]"}, nil, 0, 0},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			c := start(t, &fake{chunks: tt.chunks, trailer: tt.trailer})
			res, err := c.Stream(t.Context(), Request{Model: "m"}, nil, func(string) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if res.InputTokens != tt.in || res.OutputTokens != tt.out {
				t.Errorf("usage = %d/%d, want %d/%d", res.InputTokens, res.OutputTokens, tt.in, tt.out)
			}
		})
	}
}

func TestStreamRequest(t *testing.T) {
	tests := map[string]struct {
		key    string
		header http.Header
		want   string
	}{
		"caller":   {"key", http.Header{"Authorization": {"Bearer tok"}, "X-Project": {"p"}}, "Bearer tok"},
		"fallback": {"key", http.Header{}, "Bearer key"},
		"none":     {"", nil, ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			f := &fake{chunks: []string{"[DONE]"}}
			c := start(t, f)
			c.APIKey = tt.key
			req := Request{
				Model:    "m",
				Messages: []Message{{Role: "tool", Content: "x", ToolCallID: "c1"}, {Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "n", Arguments: "{}"}}}},
				Tools:    []Tool{{Name: "n", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}},
			}
			if _, err := c.Stream(t.Context(), req, tt.header, func(string) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if got := f.got.Header.Get("Authorization"); got != tt.want {
				t.Errorf("Authorization = %q, want %q", got, tt.want)
			}
			if tt.header != nil && f.got.Header.Get("X-Project") != tt.header.Get("X-Project") {
				t.Errorf("X-Project = %q", f.got.Header.Get("X-Project"))
			}
			b, _ := json.Marshal(f.gotBody)
			for _, s := range []string{
				`"stream":true`, `"include_usage":true`, `"model":"m"`,
				`"tool_call_id":"c1"`,
				`"tool_calls":[{"function":{"arguments":"{}","name":"n"},"id":"c1","type":"function"}]`,
				`"tools":[{"function":{"description":"d","name":"n","parameters":{"type":"object"}},"type":"function"}]`,
			} {
				if !strings.Contains(string(b), s) {
					t.Errorf("body %s lacks %s", b, s)
				}
			}
		})
	}
}

func TestStreamErrors(t *testing.T) {
	errStop := errors.New("stop")
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	tests := map[string]struct {
		client func(t *testing.T) *Client
		onText func(string) error
		want   []string
		is     error
	}{
		"upstream error": {
			client: func(t *testing.T) *Client {
				return start(t, &fake{status: 500, body: `{"error":{"message":"model is down"}}`})
			},
			want: []string{"transponder:", "500", "model is down"},
		},
		"unauthorized": {
			client: func(t *testing.T) *Client { return start(t, &fake{status: 401, body: "nope"}) },
			want:   []string{"transponder:", "401"},
		},
		"broken json": {
			client: func(t *testing.T) *Client { return start(t, &fake{chunks: []string{text("a"), "{oops"}}) },
			want:   []string{"transponder:"},
		},
		"refused": {
			client: func(*testing.T) *Client { return &Client{URL: closed.URL} },
			want:   []string{"transponder:"},
		},
		"onText error": {
			client: func(t *testing.T) *Client { return start(t, &fake{chunks: []string{text("a"), text("b"), "[DONE]"}}) },
			onText: func(string) error { return errStop },
			is:     errStop,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			on := tt.onText
			if on == nil {
				on = func(string) error { return nil }
			}
			_, err := tt.client(t).Stream(t.Context(), Request{Model: "m"}, nil, on)
			if err == nil {
				t.Fatal("want an error")
			}
			for _, s := range tt.want {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("err %q lacks %q", err, s)
				}
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("err = %v, want %v", err, tt.is)
			}
		})
	}
}

func TestStreamEndsEarly(t *testing.T) {
	c := start(t, &fake{chunks: []string{text("partial")}})
	res, err := c.Stream(t.Context(), Request{Model: "m"}, nil, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.Message.Content != "partial" {
		t.Errorf("content = %q", res.Message.Content)
	}
}
