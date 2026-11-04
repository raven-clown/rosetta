package echo

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/raven-clown/rosetta"
)

func run(t *testing.T, parts ...rosetta.Part) ([]rosetta.Event, error) {
	t.Helper()
	var got []rosetta.Event
	err := New("echo").Run(context.Background(), &rosetta.Request{Input: parts}, rosetta.NewStream(func(e rosetta.Event) error {
		got = append(got, e)
		return nil
	}))
	return got, err
}

func TestEcho(t *testing.T) {
	tests := []struct {
		name  string
		parts []rosetta.Part
		text  string
	}{
		{"one part", []rosetta.Part{{Text: "hi"}}, "hi"},
		{"two parts", []rosetta.Part{{Text: "a"}, {Text: "b"}}, "a\nb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := run(t, tt.parts...)
			if err != nil {
				t.Fatal(err)
			}
			want := []rosetta.Event{rosetta.Step{Name: "echo"}, rosetta.TextDelta{Text: tt.text}, rosetta.Step{Name: "echo", Finished: true}}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

func TestEchoEmptyInput(t *testing.T) {
	for name, parts := range map[string][]rosetta.Part{"no parts": nil, "data only": {{Data: json.RawMessage(`1`)}}} {
		if _, err := run(t, parts...); err == nil || err.Error() != "echo: no text in input" {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestEchoCard(t *testing.T) {
	c := New("parrot").Card()
	if c.Name != "parrot" || c.Description != "Repeats what you send." {
		t.Errorf("card = %#v", c)
	}
}
