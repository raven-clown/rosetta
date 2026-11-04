package session

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/raven-clown/rosetta"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func msg(text string) rosetta.Message {
	return rosetta.Message{Role: rosetta.RoleUser, Parts: []rosetta.Part{{Text: text}}}
}

func TestMemoryAppendAndHistory(t *testing.T) {
	tests := []struct {
		name    string
		appends [][]rosetta.Message
		want    []rosetta.Message
	}{
		{"one append", [][]rosetta.Message{{msg("a")}}, []rosetta.Message{msg("a")}},
		{"two appends keep order", [][]rosetta.Message{{msg("a")}, {msg("b"), msg("c")}}, []rosetta.Message{msg("a"), msg("b"), msg("c")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			m := NewMemory(time.Hour, nil)
			for _, a := range tt.appends {
				if err := m.Append(ctx, "echo", "s1", a...); err != nil {
					t.Fatal(err)
				}
			}
			got, err := m.History(ctx, "echo", "s1")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMemorySeparatesAgents(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(time.Hour, nil)
	_ = m.Append(ctx, "a", "s1", msg("for a"))
	_ = m.Append(ctx, "b", "s1", msg("for b"))
	got, _ := m.History(ctx, "a", "s1")
	if len(got) != 1 || got[0].Parts[0].Text != "for a" {
		t.Errorf("agent a history = %v", got)
	}
}

func TestMemoryExpiry(t *testing.T) {
	ctx := context.Background()
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	m := NewMemory(time.Hour, c.now)
	_ = m.Append(ctx, "echo", "s1", msg("a"))
	tests := []struct {
		name  string
		after time.Duration
		want  int
	}{
		{"before ttl", 59 * time.Minute, 1},
		{"after ttl", 61 * time.Minute, 0},
	}
	for _, tt := range tests {
		c.t = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(tt.after)
		got, err := m.History(ctx, "echo", "s1")
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if len(got) != tt.want {
			t.Errorf("%s: got %d messages, want %d", tt.name, len(got), tt.want)
		}
	}
}

func TestMemoryUnknownSession(t *testing.T) {
	got, err := NewMemory(time.Hour, nil).History(context.Background(), "echo", "nope")
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("got %v, %v; want empty slice and nil error", got, err)
	}
}

func TestMemoryHistoryIsACopy(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(time.Hour, nil)
	_ = m.Append(ctx, "echo", "s1", msg("a"))
	got, _ := m.History(ctx, "echo", "s1")
	got[0] = msg("changed")
	again, _ := m.History(ctx, "echo", "s1")
	if again[0].Parts[0].Text != "a" {
		t.Error("store changed through the returned slice")
	}
}

func TestMemoryConcurrentAppends(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(time.Hour, nil)
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Append(ctx, "echo", "s1", msg("x"))
		}()
	}
	wg.Wait()
	got, _ := m.History(ctx, "echo", "s1")
	if len(got) != 50 {
		t.Errorf("got %d messages, want 50", len(got))
	}
}

func TestMemoryDelete(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(time.Hour, nil)
	_ = m.Append(ctx, "echo", "s1", msg("a"))
	if err := m.Delete(ctx, "echo", "s1"); err != nil {
		t.Fatal(err)
	}
	got, _ := m.History(ctx, "echo", "s1")
	if len(got) != 0 {
		t.Errorf("got %v after delete", got)
	}
}

func TestMemoryMaxSessionsEvictsOldest(t *testing.T) {
	ctx := context.Background()
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	m := NewMemory(time.Hour, c.now, MaxSessions(2))
	for _, s := range []string{"s1", "s2", "s3"} {
		c.t = c.t.Add(time.Minute)
		_ = m.Append(ctx, "echo", s, msg(s))
	}
	if m.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", m.Len())
	}
	if got, _ := m.History(ctx, "echo", "s1"); len(got) != 0 {
		t.Errorf("oldest session s1 still has %v", got)
	}
	if got, _ := m.History(ctx, "echo", "s3"); len(got) != 1 {
		t.Errorf("newest session s3 = %v", got)
	}
}

func TestMemoryFullStoreDropsExpiredFirst(t *testing.T) {
	ctx := context.Background()
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	m := NewMemory(time.Hour, c.now, MaxSessions(2))
	_ = m.Append(ctx, "echo", "old", msg("x"))
	c.t = c.t.Add(30 * time.Minute)
	_ = m.Append(ctx, "echo", "keep", msg("x"))
	c.t = c.t.Add(31 * time.Minute)
	_ = m.Append(ctx, "echo", "new", msg("x"))
	if got, _ := m.History(ctx, "echo", "keep"); len(got) != 1 {
		t.Errorf("live session evicted while an expired one existed")
	}
}

func TestMemoryMaxMessagesKeepsNewest(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(time.Hour, nil, MaxMessages(2))
	_ = m.Append(ctx, "echo", "s1", msg("a"), msg("b"))
	_ = m.Append(ctx, "echo", "s1", msg("c"))
	got, _ := m.History(ctx, "echo", "s1")
	if len(got) != 2 || got[0].Parts[0].Text != "b" || got[1].Parts[0].Text != "c" {
		t.Errorf("got %v, want [b c]", got)
	}
}

func TestMemoryDefaultLimits(t *testing.T) {
	m := NewMemory(time.Hour, nil)
	if m.maxSessions != DefaultMaxSessions || m.maxMessages != DefaultMaxMessages {
		t.Errorf("limits = %d, %d", m.maxSessions, m.maxMessages)
	}
}
