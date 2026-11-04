package protocoltest

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/raven-clown/rosetta"
)

func TestScriptedAgentSendsEvents(t *testing.T) {
	want := AllEvents()
	a := ScriptedAgent("s", want...)
	var got []rosetta.Event
	err := a.Run(context.Background(), &rosetta.Request{}, rosetta.NewStream(func(e rosetta.Event) error {
		got = append(got, e)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if a.Card().Name != "s" {
		t.Errorf("name = %q", a.Card().Name)
	}
}

func TestAllEventsCoversEveryType(t *testing.T) {
	types := map[string]bool{}
	for _, e := range AllEvents() {
		types[fmt.Sprintf("%T", e)] = true
	}
	if len(types) != 9 {
		t.Errorf("got %d event types, want 9: %v", len(types), types)
	}
}

func TestStaticRegistry(t *testing.T) {
	r := StaticRegistry(ScriptedAgent("a"), ScriptedAgent("b"))
	if _, ok := r.Get("b"); !ok || len(r.List()) != 2 {
		t.Error("registry lookup failed")
	}
}

func TestNewRunnerUsesCountingIDs(t *testing.T) {
	var got []rosetta.Event
	a := ScriptedAgent("s", rosetta.TextDelta{Text: "x"})
	req := &rosetta.Request{}
	err := NewRunner().Run(context.Background(), a, req, func(e rosetta.Event) error {
		got = append(got, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.RunID != "id-1" || got[0].(rosetta.TextDelta).MessageID != "id-2" {
		t.Errorf("run %q, message %q", req.RunID, got[0].(rosetta.TextDelta).MessageID)
	}
}
