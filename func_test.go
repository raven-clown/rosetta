package rosetta

import (
	"context"
	"encoding/json"
	"testing"
)

func TestFunc(t *testing.T) {
	card := Card{Name: "echo", Description: "d", Skills: []Skill{{ID: "s"}}}
	req := &Request{SessionID: "s1"}
	var seen *Request
	a := Func(card, func(_ context.Context, r *Request, _ Stream) error {
		seen = r
		return nil
	})
	if got := a.Card(); got.Name != "echo" || got.Description != "d" || len(got.Skills) != 1 {
		t.Errorf("card = %#v", got)
	}
	if err := a.Run(context.Background(), req, NewStream(func(Event) error { return nil })); err != nil {
		t.Fatal(err)
	}
	if seen != req {
		t.Error("run got a different request")
	}
}

func TestRequestText(t *testing.T) {
	tests := []struct {
		name  string
		parts []Part
		want  string
	}{
		{"no parts", nil, ""},
		{"text and data", []Part{{Text: "a"}, {Data: json.RawMessage(`1`)}, {Text: "b"}}, "a\nb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Request{Input: tt.parts}
			if got := r.Text(); got != tt.want {
				t.Errorf("Text() = %q, want %q", got, tt.want)
			}
		})
	}
}
