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

func names(r *Registry) []string {
	var out []string
	for _, a := range r.List() {
		out = append(out, a.Card().Name)
	}
	return out
}

func TestSet(t *testing.T) {
	r, _ := New(agent("a"))
	first, second := r.Changes(), r.Changes()
	if err := r.Set(agent("b"), agent("c")); err != nil {
		t.Fatal(err)
	}
	if got := names(r); len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Errorf("list = %v", got)
	}
	if _, ok := r.Get("a"); ok {
		t.Error("old agent still there")
	}
	for i, ch := range []<-chan struct{}{first, second} {
		select {
		case <-ch:
		default:
			t.Errorf("subscriber %d not signalled", i)
		}
	}
}

func TestSetRejectsBadList(t *testing.T) {
	r, _ := New(agent("a"))
	ch := r.Changes()
	for name, list := range map[string][]rosetta.Agent{
		"duplicate": {agent("x"), agent("x")},
		"bad name":  {agent("Bad")},
	} {
		t.Run(name, func(t *testing.T) {
			if err := r.Set(list...); err == nil {
				t.Fatal("want an error")
			}
			if got := names(r); len(got) != 1 || got[0] != "a" {
				t.Errorf("list changed to %v", got)
			}
		})
	}
	select {
	case <-ch:
		t.Error("signalled for a rejected list")
	default:
	}
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	r, _ := New()
	_ = r.Changes()
	for range 5 {
		if err := r.Set(agent("a")); err != nil {
			t.Fatal(err)
		}
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
