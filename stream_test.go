package rosetta

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func collect(t *testing.T) (Stream, *[]Event) {
	t.Helper()
	var got []Event
	return NewStream(func(e Event) error {
		got = append(got, e)
		return nil
	}), &got
}

func TestStreamHelpers(t *testing.T) {
	tests := []struct {
		name string
		call func(Stream) error
		want Event
	}{
		{"text", func(s Stream) error { return s.Text("hi") }, TextDelta{Text: "hi"}},
		{"reasoning", func(s Stream) error { return s.Reasoning("hmm") }, ReasoningDelta{Text: "hmm"}},
		{"tool call", func(s Stream) error { return s.ToolCall("t1", "search", json.RawMessage(`{"q":1}`)) },
			ToolCall{ID: "t1", Name: "search", Args: json.RawMessage(`{"q":1}`)}},
		{"tool result", func(s Stream) error { return s.ToolResult("t1", json.RawMessage(`{"ok":true}`), false) },
			ToolResult{ID: "t1", Result: json.RawMessage(`{"ok":true}`)}},
		{"send", func(s Stream) error { return s.Send(Step{Name: "plan"}) }, Step{Name: "plan"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, got := collect(t)
			if err := tt.call(s); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(*got) != 1 {
				t.Fatalf("got %d events, want 1", len(*got))
			}
			if !reflect.DeepEqual((*got)[0], tt.want) {
				t.Errorf("got %#v, want %#v", (*got)[0], tt.want)
			}
		})
	}
}

func TestStreamSendError(t *testing.T) {
	errBoom := errors.New("boom")
	s := NewStream(func(Event) error { return errBoom })
	calls := map[string]func() error{
		"send":        func() error { return s.Send(TextDelta{}) },
		"text":        func() error { return s.Text("x") },
		"reasoning":   func() error { return s.Reasoning("x") },
		"tool call":   func() error { return s.ToolCall("1", "n", nil) },
		"tool result": func() error { return s.ToolResult("1", nil, true) },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, errBoom) {
			t.Errorf("%s: got %v, want errBoom", name, err)
		}
	}
}
