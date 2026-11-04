package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/raven-clown/rosetta"
)

func agent(name string) rosetta.Agent {
	return rosetta.Func(rosetta.Card{Name: name}, func(context.Context, *rosetta.Request, rosetta.Stream) error { return nil })
}

func TestNewRejectsBadNames(t *testing.T) {
	for _, name := range []string{"", "Echo", "has space", "a/b", strings.Repeat("a", 65), "-lead"} {
		t.Run(name, func(t *testing.T) {
			_, err := New(agent(name))
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), "\""+name+"\"") {
				t.Errorf("error %q does not name the agent", err)
			}
		})
	}
}

func TestValidName(t *testing.T) {
	for _, name := range []string{"echo", "a", "web-search_2", strings.Repeat("a", 64)} {
		if !ValidName(name) {
			t.Errorf("ValidName(%q) = false", name)
		}
	}
}

func TestNewRejectsDuplicates(t *testing.T) {
	_, err := New(agent("echo"), agent("echo"))
	if err == nil || !strings.Contains(err.Error(), "echo") {
		t.Errorf("err = %v", err)
	}
}

func TestGetAndList(t *testing.T) {
	r, err := New(agent("b"), agent("a"))
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := r.Get("a"); !ok || a.Card().Name != "a" {
		t.Error("Get(a) failed")
	}
	if _, ok := r.Get("zzz"); ok {
		t.Error("Get(zzz) found something")
	}
	list := r.List()
	if len(list) != 2 || list[0].Card().Name != "b" || list[1].Card().Name != "a" {
		t.Errorf("List() order wrong")
	}
	if r.Changes() == nil {
		t.Error("Changes() is nil")
	}
}
