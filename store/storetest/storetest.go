// Package storetest is the test suite every store dialect passes.
package storetest

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/store"
)

// Clock is a settable time source for Options.Now.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// NewClock returns a clock at a fixed instant.
func NewClock() *Clock { return &Clock{t: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)} }

// Now returns the clock's time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Add moves the clock forward.
func (c *Clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// Opener returns an empty, migrated store with opts.
type Opener func(t *testing.T, opts store.Options) *store.Store

// Run runs the suite against stores from open.
func Run(t *testing.T, open Opener) {
	tests := map[string]func(*testing.T, Opener){
		"migrate twice":       migrateTwice,
		"history":             history,
		"history cap":         historyCap,
		"history expiry":      historyExpiry,
		"sessions admin":      sessionsAdmin,
		"runs":                runs,
		"runs paging":         runsPaging,
		"client run ids":      clientRunIDs,
		"agents":              agents,
		"keys":                keys,
		"audit on writes":     auditOnWrites,
		"failed write audits": failedWriteAudits,
		"revision":            revision,
		"prune":               prune,
		"bad cursor":          badCursor,
		"concurrent if-match": concurrentIfMatch,
		"concurrent append":   concurrentAppend,
	}
	for name, fn := range tests {
		t.Run(name, func(t *testing.T) { fn(t, open) })
	}
}

func user(s string) rosetta.Message {
	return rosetta.Message{Role: rosetta.RoleUser, Parts: []rosetta.Part{{Text: s}}}
}

func au(action, target string) store.Audit {
	return store.Audit{Actor: "key:test", Action: action, Target: target}
}

func migrateTwice(t *testing.T, open Opener) {
	s := open(t, store.Options{})
	n, err := s.Migrate(t.Context())
	if err != nil || n != 0 {
		t.Errorf("second migrate = %d, %v, want 0, nil", n, err)
	}
	if s.Latest() < 1 {
		t.Errorf("latest = %d", s.Latest())
	}
	if err := s.Ping(t.Context()); err != nil {
		t.Error(err)
	}
}

func history(t *testing.T, open Opener) {
	s := open(t, store.Options{})
	ctx := t.Context()
	got, err := s.History(ctx, "a", "caller:abc/s1")
	if err != nil || len(got) != 0 || got == nil {
		t.Fatalf("empty history = %#v, %v", got, err)
	}
	data := rosetta.Message{Role: rosetta.RoleAgent, Parts: []rosetta.Part{{Data: json.RawMessage(`{"n":1}`)}}}
	if err := s.Append(ctx, "a", "caller:abc/s1", user("hi"), data); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, "a", "caller:abc/s1", user("again")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, "a", "caller:xyz/s1", user("other caller")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, "b", "caller:abc/s1", user("other agent")); err != nil {
		t.Fatal(err)
	}
	got, err = s.History(ctx, "a", "caller:abc/s1")
	if err != nil {
		t.Fatal(err)
	}
	want := []rosetta.Message{user("hi"), data, user("again")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("history = %+v, want %+v", got, want)
	}
	if err := s.Delete(ctx, "a", "caller:abc/s1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.History(ctx, "a", "caller:abc/s1"); len(got) != 0 {
		t.Errorf("history after delete = %+v", got)
	}
	if got, _ := s.History(ctx, "a", "caller:xyz/s1"); len(got) != 1 {
		t.Errorf("other caller's history = %+v", got)
	}
}

func historyCap(t *testing.T, open Opener) {
	s := open(t, store.Options{MaxMessages: 3})
	for i := range 5 {
		if err := s.Append(t.Context(), "a", "anon:s", user(fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.History(t.Context(), "a", "anon:s")
	want := []rosetta.Message{user("2"), user("3"), user("4")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("history = %+v", got)
	}
	list, _, _ := s.ListSessions(t.Context(), store.SessionFilter{}, store.Page{})
	if len(list) != 1 || list[0].Messages != 3 {
		t.Errorf("sessions = %+v", list)
	}
}

func historyExpiry(t *testing.T, open Opener) {
	clock := NewClock()
	s := open(t, store.Options{SessionTTL: time.Hour, Now: clock.Now})
	ctx := t.Context()
	_ = s.Append(ctx, "a", "anon:s", user("old"))
	clock.Add(30 * time.Minute)
	_ = s.Append(ctx, "a", "anon:s", user("kept alive"))
	clock.Add(45 * time.Minute)
	if got, _ := s.History(ctx, "a", "anon:s"); len(got) != 2 {
		t.Fatalf("history before expiry = %+v", got)
	}
	clock.Add(2 * time.Hour)
	if got, _ := s.History(ctx, "a", "anon:s"); len(got) != 0 {
		t.Errorf("expired history = %+v", got)
	}
	if list, _, _ := s.ListSessions(ctx, store.SessionFilter{}, store.Page{}); len(list) != 0 {
		t.Errorf("expired sessions listed: %+v", list)
	}
	_ = s.Append(ctx, "a", "anon:s", user("fresh"))
	if got, _ := s.History(ctx, "a", "anon:s"); !reflect.DeepEqual(got, []rosetta.Message{user("fresh")}) {
		t.Errorf("restarted history = %+v", got)
	}
}

func sessionsAdmin(t *testing.T, open Opener) {
	clock := NewClock()
	s := open(t, store.Options{Now: clock.Now})
	ctx := t.Context()
	_ = s.Append(ctx, "a", "caller:abc/s1", user("one"))
	clock.Add(time.Second)
	_ = s.Append(ctx, "b", "anon:s2", user("two"))
	list, cur, err := s.ListSessions(ctx, store.SessionFilter{}, store.Page{})
	if err != nil || cur != "" || len(list) != 2 {
		t.Fatalf("list = %+v, %q, %v", list, cur, err)
	}
	if list[0].Agent != "b" || list[0].SessionID != "s2" || list[0].Caller != "" {
		t.Errorf("newest = %+v", list[0])
	}
	first := list[1]
	if first.Agent != "a" || first.SessionID != "s1" || first.Caller != "caller:abc" || first.Messages != 1 || first.Version < 1 {
		t.Errorf("oldest = %+v", first)
	}
	if only, _, _ := s.ListSessions(ctx, store.SessionFilter{Agent: "a"}, store.Page{}); len(only) != 1 || only[0].ID != first.ID {
		t.Errorf("filtered = %+v", only)
	}
	got, msgs, err := s.GetSession(ctx, first.ID)
	if err != nil || got.ID != first.ID || len(msgs) != 1 {
		t.Errorf("get = %+v, %+v, %v", got, msgs, err)
	}
	if _, _, err := s.GetSession(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("get missing = %v", err)
	}
	if err := s.DeleteSession(ctx, first.ID, first.Version+1, au("session.delete", first.ID)); !errors.Is(err, store.ErrVersion) {
		t.Errorf("delete with wrong version = %v", err)
	}
	if err := s.DeleteSession(ctx, first.ID, first.Version, au("session.delete", first.ID)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, first.ID, 0, au("session.delete", first.ID)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("delete twice = %v", err)
	}
	if got, _ := s.History(ctx, "a", "caller:abc/s1"); len(got) != 0 {
		t.Errorf("history after admin delete = %+v", got)
	}
}

func run(id, agent string, started time.Time) store.Run {
	return store.Run{
		ID: id, Agent: agent, RunID: "client-run", Protocol: "mcp", SessionID: "s", Caller: "key:1", Status: store.RunCompleted,
		Started: started, Ended: started.Add(time.Second), InputTokens: 1, OutputTokens: 2, ReasoningTokens: 3, RequestID: "req",
	}
}

func runs(t *testing.T, open Opener) {
	s := open(t, store.Options{})
	ctx := t.Context()
	base := NewClock().Now()
	r1 := run("r1", "a", base)
	r2 := run("r2", "b", base.Add(time.Minute))
	r2.Protocol, r2.Status, r2.Error = "a2a", store.RunFailed, "boom"
	for _, r := range []store.Run{r1, r2} {
		if err := s.SaveRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveRun(ctx, r1); !errors.Is(err, store.ErrConflict) {
		t.Errorf("saving an ID twice = %v, want ErrConflict", err)
	}
	got, err := s.GetRun(ctx, "r2")
	if err != nil || !reflect.DeepEqual(got, r2) {
		t.Errorf("get = %+v, %v\nwant %+v", got, err, r2)
	}
	if _, err := s.GetRun(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("get missing = %v", err)
	}
	tests := map[string]struct {
		f    store.RunFilter
		want []string
	}{
		"all":      {store.RunFilter{}, []string{"r2", "r1"}},
		"agent":    {store.RunFilter{Agent: "a"}, []string{"r1"}},
		"protocol": {store.RunFilter{Protocol: "a2a"}, []string{"r2"}},
		"status":   {store.RunFilter{Status: store.RunCompleted}, []string{"r1"}},
		"from":     {store.RunFilter{From: base.Add(30 * time.Second)}, []string{"r2"}},
		"to":       {store.RunFilter{To: base.Add(30 * time.Second)}, []string{"r1"}},
		"none":     {store.RunFilter{Agent: "zzz"}, nil},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			list, _, err := s.ListRuns(ctx, tt.f, store.Page{})
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, r := range list {
				ids = append(ids, r.ID)
			}
			if !reflect.DeepEqual(ids, tt.want) {
				t.Errorf("ids = %v, want %v", ids, tt.want)
			}
		})
	}
}

// clientRunIDs checks that a run ID chosen by a client never stops another
// run from being recorded.
func clientRunIDs(t *testing.T, open Opener) {
	s := open(t, store.Options{})
	ctx := t.Context()
	for range 2 {
		if err := s.SaveRun(ctx, store.Run{Agent: "a", RunID: "same", Status: store.RunCompleted, Started: NewClock().Now()}); err != nil {
			t.Fatal(err)
		}
	}
	list, _, _ := s.ListRuns(ctx, store.RunFilter{}, store.Page{})
	if len(list) != 2 || list[0].ID == "" || list[0].ID == list[1].ID || list[0].RunID != "same" {
		t.Errorf("runs = %+v", list)
	}
}

func runsPaging(t *testing.T, open Opener) {
	s := open(t, store.Options{})
	ctx := t.Context()
	base := NewClock().Now()
	for i := range 5 {
		_ = s.SaveRun(ctx, run(fmt.Sprintf("r%d", i), "a", base.Add(time.Duration(i/2)*time.Minute)))
	}
	var ids []string
	cur := ""
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("paging does not end")
		}
		list, next, err := s.ListRuns(ctx, store.RunFilter{}, store.Page{Cursor: cur, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range list {
			ids = append(ids, r.ID)
		}
		if next == "" {
			break
		}
		cur = next
	}
	if want := []string{"r4", "r3", "r2", "r1", "r0"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}

func agents(t *testing.T, open Opener) {
	s := open(t, store.Options{})
	ctx := t.Context()
	a, err := s.CreateAgent(ctx, store.Agent{Name: "helper", Spec: json.RawMessage(`{"kind":"echo"}`), UpdatedBy: "key:1"}, au("agent.create", "helper"))
	if err != nil || a.Version != 1 || a.Created.IsZero() {
		t.Fatalf("create = %+v, %v", a, err)
	}
	if _, err := s.CreateAgent(ctx, store.Agent{Name: "helper", Spec: json.RawMessage(`{}`)}, au("agent.create", "helper")); !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate = %v", err)
	}
	if _, err := s.UpdateAgent(ctx, store.Agent{Name: "helper", Spec: json.RawMessage(`{}`)}, 7, au("agent.update", "helper")); !errors.Is(err, store.ErrVersion) {
		t.Errorf("stale update = %v", err)
	}
	if _, err := s.UpdateAgent(ctx, store.Agent{Name: "nope", Spec: json.RawMessage(`{}`)}, 0, au("agent.update", "nope")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("update missing = %v", err)
	}
	u, err := s.UpdateAgent(ctx, store.Agent{Name: "helper", Spec: json.RawMessage(`{"kind":"llm"}`), UpdatedBy: "key:2"}, 1, au("agent.update", "helper"))
	if err != nil || u.Version != 2 || string(u.Spec) != `{"kind":"llm"}` || u.UpdatedBy != "key:2" {
		t.Errorf("update = %+v, %v", u, err)
	}
	got, err := s.GetAgent(ctx, "helper")
	if err != nil || got.Version != 2 {
		t.Errorf("get = %+v, %v", got, err)
	}
	_, _ = s.CreateAgent(ctx, store.Agent{Name: "alpha", Spec: json.RawMessage(`{}`)}, au("agent.create", "alpha"))
	list, err := s.ListAgents(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "alpha" {
		t.Errorf("list = %+v, %v", list, err)
	}
	if err := s.DeleteAgent(ctx, "helper", 1, au("agent.delete", "helper")); !errors.Is(err, store.ErrVersion) {
		t.Errorf("stale delete = %v", err)
	}
	if err := s.DeleteAgent(ctx, "helper", 2, au("agent.delete", "helper")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAgent(ctx, "helper"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("get deleted = %v", err)
	}
	if err := s.DeleteAgent(ctx, "helper", 0, au("agent.delete", "helper")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("delete twice = %v", err)
	}
}

func keys(t *testing.T, open Opener) {
	clock := NewClock()
	s := open(t, store.Options{Now: clock.Now})
	ctx := t.Context()
	if n, _ := s.CountKeys(ctx); n != 0 {
		t.Fatalf("count = %d", n)
	}
	k, err := s.CreateKey(ctx, store.Key{Name: "ci", Role: "member", Prefix: "rsk_ab", Hash: "h1", CreatedBy: "bootstrap"}, au("api_key.create", "ci"))
	if err != nil || k.ID == "" || k.Version != 1 {
		t.Fatalf("create = %+v, %v", k, err)
	}
	if _, err := s.CreateKey(ctx, store.Key{Name: "other", Role: "member", Hash: "h1"}, au("api_key.create", "other")); !errors.Is(err, store.ErrConflict) {
		t.Errorf("same hash = %v", err)
	}
	clock.Add(time.Second)
	k2, _ := s.CreateKey(ctx, store.Key{Name: "admin", Role: "admin", Hash: "h2"}, au("api_key.create", "admin"))
	if n, _ := s.CountKeys(ctx); n != 2 {
		t.Errorf("count = %d", n)
	}
	if err := s.RevokeKey(ctx, k.ID, 5, au("api_key.revoke", k.ID)); !errors.Is(err, store.ErrVersion) {
		t.Errorf("stale revoke = %v", err)
	}
	if err := s.RevokeKey(ctx, k.ID, 1, au("api_key.revoke", k.ID)); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeKey(ctx, "missing", 0, au("api_key.revoke", "missing")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("revoke missing = %v", err)
	}
	list, err := s.ListKeys(ctx)
	if err != nil || len(list) != 2 || list[0].ID != k2.ID || list[1].Revoked.IsZero() || list[1].Version != 2 {
		t.Errorf("list = %+v, %v", list, err)
	}
	got, err := s.GetKey(ctx, k.ID)
	if err != nil || got.Revoked.IsZero() || got.CreatedBy != "bootstrap" || got.Prefix != "rsk_ab" {
		t.Errorf("get = %+v, %v", got, err)
	}
	if n, _ := s.CountKeys(ctx); n != 1 {
		t.Errorf("count after revoke = %d", n)
	}
}

func auditOnWrites(t *testing.T, open Opener) {
	clock := NewClock()
	s := open(t, store.Options{Now: clock.Now})
	ctx := t.Context()
	_, _ = s.CreateAgent(ctx, store.Agent{Name: "x", Spec: json.RawMessage(`{}`)}, store.Audit{Actor: "key:a", Action: "agent.create", Target: "x", Details: json.RawMessage(`{"kind":"echo"}`)})
	clock.Add(time.Second)
	k, _ := s.CreateKey(ctx, store.Key{Name: "k", Role: "viewer", Hash: "h"}, au("api_key.create", "k"))
	clock.Add(time.Second)
	_ = s.RevokeKey(ctx, k.ID, 0, au("api_key.revoke", k.ID))
	clock.Add(time.Second)
	_ = s.DeleteAgent(ctx, "x", 0, au("agent.delete", "x"))

	list, _, err := s.ListAudit(ctx, store.AuditFilter{}, store.Page{})
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, a := range list {
		actions = append(actions, a.Action)
	}
	if want := []string{"agent.delete", "api_key.revoke", "api_key.create", "agent.create"}; !reflect.DeepEqual(actions, want) {
		t.Errorf("actions = %v, want %v", actions, want)
	}
	last := list[3]
	if last.Actor != "key:a" || last.Target != "x" || string(last.Details) != `{"kind":"echo"}` || !last.Time.Equal(NewClock().Now()) {
		t.Errorf("record = %+v", last)
	}
	for name, tt := range map[string]struct {
		f    store.AuditFilter
		want int
	}{
		"action": {store.AuditFilter{Action: "agent.create"}, 1},
		"target": {store.AuditFilter{Target: "x"}, 2},
		"from":   {store.AuditFilter{From: NewClock().Now().Add(2 * time.Second)}, 2},
		"to":     {store.AuditFilter{To: NewClock().Now().Add(time.Second)}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			got, _, _ := s.ListAudit(ctx, tt.f, store.Page{})
			if len(got) != tt.want {
				t.Errorf("got %d records, want %d", len(got), tt.want)
			}
		})
	}
	page, cur, _ := s.ListAudit(ctx, store.AuditFilter{}, store.Page{Limit: 3})
	rest, end, _ := s.ListAudit(ctx, store.AuditFilter{}, store.Page{Cursor: cur, Limit: 3})
	if len(page) != 3 || cur == "" || len(rest) != 1 || end != "" {
		t.Errorf("pages = %d %q %d %q", len(page), cur, len(rest), end)
	}
}

func failedWriteAudits(t *testing.T, open Opener) {
	s := open(t, store.Options{})
	ctx := t.Context()
	_, _ = s.CreateAgent(ctx, store.Agent{Name: "x", Spec: json.RawMessage(`{}`)}, au("agent.create", "x"))
	_, _ = s.CreateAgent(ctx, store.Agent{Name: "x", Spec: json.RawMessage(`{}`)}, au("agent.create", "x"))
	_, _ = s.UpdateAgent(ctx, store.Agent{Name: "x", Spec: json.RawMessage(`{}`)}, 9, au("agent.update", "x"))
	_ = s.DeleteAgent(ctx, "y", 0, au("agent.delete", "y"))
	list, _, _ := s.ListAudit(ctx, store.AuditFilter{}, store.Page{})
	if len(list) != 1 {
		t.Errorf("audit records = %+v, want only the create", list)
	}
}

func revision(t *testing.T, open Opener) {
	s := open(t, store.Options{})
	ctx := t.Context()
	rev := func() int64 {
		v, err := s.Revision(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	start := rev()
	_ = s.Append(ctx, "a", "anon:s", user("hi"))
	_ = s.SaveRun(ctx, run("r", "a", NewClock().Now()))
	if rev() != start {
		t.Error("sessions or runs moved the revision")
	}
	_, _ = s.CreateAgent(ctx, store.Agent{Name: "x", Spec: json.RawMessage(`{}`)}, au("agent.create", "x"))
	if rev() != start+1 {
		t.Error("agent create did not move the revision")
	}
	k, _ := s.CreateKey(ctx, store.Key{Name: "k", Role: "member", Hash: "h"}, au("api_key.create", "k"))
	_ = s.RevokeKey(ctx, k.ID, 0, au("api_key.revoke", k.ID))
	if rev() != start+3 {
		t.Errorf("revision = %d, want %d", rev(), start+3)
	}
}

func prune(t *testing.T, open Opener) {
	clock := NewClock()
	s := open(t, store.Options{SessionTTL: time.Hour, Now: clock.Now})
	ctx := t.Context()
	_ = s.SaveRun(ctx, run("old", "a", clock.Now()))
	_ = s.Append(ctx, "a", "anon:old", user("x"))
	clock.Add(48 * time.Hour)
	_ = s.SaveRun(ctx, run("new", "a", clock.Now()))
	_ = s.Append(ctx, "a", "anon:new", user("x"))
	n, err := s.Prune(ctx, clock.Now(), 24*time.Hour)
	if err != nil || n != 2 {
		t.Errorf("pruned = %d, %v, want 2", n, err)
	}
	if _, err := s.GetRun(ctx, "old"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("old run = %v", err)
	}
	if _, err := s.GetRun(ctx, "new"); err != nil {
		t.Errorf("new run = %v", err)
	}
	if n, _ := s.Prune(ctx, clock.Now(), 0); n != 0 {
		t.Errorf("second prune = %d", n)
	}
}

// concurrentIfMatch sends two writes based on the same version at once:
// exactly one may win, on every dialect.
func concurrentIfMatch(t *testing.T, open Opener) {
	s := open(t, store.Options{})
	ctx := t.Context()
	k, err := s.CreateKey(ctx, store.Key{Name: "k", Role: "member", Hash: "hk"}, au("api_key.create", "k"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.CreateAgent(ctx, store.Agent{Name: "x", Spec: json.RawMessage(`{}`)}, au("agent.create", "x"))
	_ = s.Append(ctx, "a", "anon:s", user("hi"))
	list, _, _ := s.ListSessions(ctx, store.SessionFilter{}, store.Page{})
	writes := map[string]func(i int) error{
		"update agent": func(i int) error {
			_, err := s.UpdateAgent(ctx, store.Agent{Name: "x", Spec: json.RawMessage(fmt.Sprintf(`{"n":%d}`, i))}, 1, au("agent.update", "x"))
			return err
		},
		"revoke key":     func(int) error { return s.RevokeKey(ctx, k.ID, 1, au("api_key.revoke", k.ID)) },
		"delete session": func(int) error { return s.DeleteSession(ctx, list[0].ID, list[0].Version, au("session.delete", "s")) },
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			const n = 4
			errs := make([]error, n)
			var wg sync.WaitGroup
			for i := range n {
				wg.Go(func() { errs[i] = write(i) })
			}
			wg.Wait()
			won := 0
			for _, err := range errs {
				switch {
				case err == nil:
					won++
				case !errors.Is(err, store.ErrVersion) && !errors.Is(err, store.ErrNotFound):
					t.Errorf("unexpected error: %v", err)
				}
			}
			if won != 1 {
				t.Errorf("%d writes won, want 1: %v", won, errs)
			}
		})
	}
}

// concurrentAppend appends to one session from several goroutines: no
// turn may be lost.
func concurrentAppend(t *testing.T, open Opener) {
	s := open(t, store.Options{})
	ctx := t.Context()
	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { errs[i] = s.Append(ctx, "a", "anon:busy", user(fmt.Sprint(i))) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.History(ctx, "a", "anon:busy"); len(got) != n {
		t.Errorf("history has %d messages, want %d", len(got), n)
	}
}

func badCursor(t *testing.T, open Opener) {
	s := open(t, store.Options{})
	for _, c := range []string{"!!!", "bm9jb2xvbg", "MTp4"} {
		_, _, err := s.ListRuns(t.Context(), store.RunFilter{}, store.Page{Cursor: c})
		if c == "MTp4" {
			if err != nil {
				t.Errorf("valid cursor %q: %v", c, err)
			}
			continue
		}
		if !errors.Is(err, store.ErrBadCursor) {
			t.Errorf("cursor %q: %v", c, err)
		}
	}
}
