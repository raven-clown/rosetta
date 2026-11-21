package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/config"
	"github.com/raven-clown/rosetta/internal/auth"
	"github.com/raven-clown/rosetta/store"
	"github.com/raven-clown/rosetta/store/storetest"
)

type env struct {
	t       *testing.T
	h       *Handler
	st      *store.Store
	clock   *storetest.Clock
	reloads atomic.Int64
}

func validate(a config.Agent) error {
	switch a.Kind {
	case "echo", "a2a":
	case "llm":
		if a.Model == "" {
			return &config.FieldError{Field: "model", Message: "is required for kind llm"}
		}
	default:
		return &config.FieldError{Field: "kind", Message: "must be llm, a2a or echo"}
	}
	return nil
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clock := storetest.NewClock()
	st, err := store.OpenWith(t.Context(), "sqlite://:memory:", store.Options{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{t: t, st: st, clock: clock}
	e.h = New(Options{
		Store: st,
		Fixed: func() []FixedAgent {
			return []FixedAgent{
				{Spec: config.Agent{Name: "echo", Description: "Repeats", Kind: "echo"}, Source: SourceCode},
				{Spec: config.Agent{Name: "helper", Kind: "llm", Model: "m"}, Source: SourceConfig},
			}
		},
		Keys:     []FixedKey{{Name: "ops", Role: "admin"}},
		Validate: validate,
		Reload:   func(context.Context) { e.reloads.Add(1) },
		Status: func(context.Context) Status {
			return Status{Protocols: map[string]bool{"mcp": true, "acp": false}, Upstreams: map[string]string{"transponder": "ok"}}
		},
		Version: "test",
		Started: clock.Now(),
		Now:     clock.Now,
	})
	return e
}

type res struct {
	code   int
	header http.Header
	body   []byte
}

func (r res) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("body %q: %v", r.body, err)
	}
	return m
}

func (r res) errCode() (string, string) {
	var b struct {
		Error struct{ Code, Field string } `json:"error"`
	}
	_ = json.Unmarshal(r.body, &b)
	return b.Error.Code, b.Error.Field
}

// do sends a request as a key with role and checks the answer against
// openapi.yaml. headers are name, value pairs.
func (e *env) do(role, method, path, body string, headers ...string) res {
	e.t.Helper()
	ctx := auth.WithPrincipal(e.t.Context(), auth.Principal{ID: "key:" + role, Name: role + "-key", Role: role})
	req := httptest.NewRequestWithContext(ctx, method, Prefix+path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	out := res{rec.Code, rec.Header(), rec.Body.Bytes()}
	if _, pattern := e.h.mux.Handler(req); pattern != Prefix+"/" {
		m, p, _ := strings.Cut(pattern, " ")
		loadSpec(e.t).check(e.t, m+" "+strings.TrimPrefix(p, Prefix), out.code, out.header, out.body)
	}
	return out
}

func (e *env) want(r res, code int, errCode, field string) {
	e.t.Helper()
	if r.code != code {
		e.t.Fatalf("status %d, want %d: %s", r.code, code, r.body)
	}
	if errCode == "" {
		return
	}
	if c, f := r.errCode(); c != errCode || f != field {
		e.t.Errorf("error %s/%s, want %s/%s: %s", c, f, errCode, field, r.body)
	}
}

func TestStatus(t *testing.T) {
	e := newEnv(t)
	e.clock.Add(90 * time.Second)
	r := e.do("viewer", "GET", "/status", "")
	e.want(r, http.StatusOK, "", "")
	m := r.json(t)
	if m["version"] != "test" || m["uptime_seconds"] != float64(90) || m["agents"] != float64(2) {
		t.Errorf("status = %v", m)
	}
	if m["storage"].(map[string]any)["dialect"] != "sqlite" || m["upstreams"].(map[string]any)["storage"] != "ok" {
		t.Errorf("status = %v", m)
	}
}

func TestOpenAPIServed(t *testing.T) {
	e := newEnv(t)
	r := e.do("viewer", "GET", "/openapi.yaml", "")
	if r.code != http.StatusOK || !strings.HasPrefix(string(r.body), "openapi: 3.1.0") {
		t.Errorf("status %d", r.code)
	}
}

func TestUnknownEndpoint(t *testing.T) {
	e := newEnv(t)
	e.want(e.do("admin", "GET", "/nope", ""), http.StatusNotFound, "not_found", "")
}

func TestAgentLifecycle(t *testing.T) {
	e := newEnv(t)
	r := e.do("member", "POST", "/agents", `{"name":"bot","kind":"llm","model":"fast","tools":["github__*"]}`)
	e.want(r, http.StatusCreated, "", "")
	if r.header.Get("ETag") != `"1"` || e.reloads.Load() != 1 {
		t.Errorf("etag %q, reloads %d", r.header.Get("ETag"), e.reloads.Load())
	}
	if m := r.json(t); m["source"] != "api" || m["updated_by"] != "member-key" || m["model"] != "fast" {
		t.Errorf("created = %v", m)
	}

	list := e.do("viewer", "GET", "/agents", "").json(t)["items"].([]any)
	var names []string
	for _, it := range list {
		a := it.(map[string]any)
		names = append(names, fmt.Sprint(a["name"], ":", a["source"]))
	}
	if strings.Join(names, ",") != "bot:api,echo:code,helper:config" {
		t.Errorf("agents = %v", names)
	}

	e.want(e.do("member", "PATCH", "/agents/bot", `{"description":"files issues"}`, "If-Match", `"1"`), http.StatusOK, "", "")
	r = e.do("member", "PATCH", "/agents/bot", `{"description":"stale"}`, "If-Match", `"1"`)
	e.want(r, http.StatusConflict, "version_conflict", "If-Match")
	got := e.do("viewer", "GET", "/agents/bot", "")
	e.want(got, http.StatusOK, "", "")
	if m := got.json(t); m["description"] != "files issues" || m["model"] != "fast" || m["version"] != float64(2) {
		t.Errorf("after patch = %v", m)
	}
	if got.header.Get("ETag") != `"2"` {
		t.Errorf("etag = %q", got.header.Get("ETag"))
	}
	e.want(e.do("member", "DELETE", "/agents/bot", "", "If-Match", "1"), http.StatusConflict, "version_conflict", "If-Match")
	e.want(e.do("member", "DELETE", "/agents/bot", "", "If-Match", "2"), http.StatusNoContent, "", "")
	e.want(e.do("viewer", "GET", "/agents/bot", ""), http.StatusNotFound, "not_found", "")
	if e.reloads.Load() != 3 {
		t.Errorf("reloads = %d, want 3", e.reloads.Load())
	}
}

func TestAgentErrors(t *testing.T) {
	e := newEnv(t)
	e.want(e.do("member", "POST", "/agents", `{"name":"taken","kind":"echo"}`), http.StatusCreated, "", "")
	tests := map[string]struct {
		role, method, path, body string
		headers                  []string
		code                     int
		errCode, field           string
	}{
		"bad name":         {"member", "POST", "/agents", `{"name":"Bad Name","kind":"echo"}`, nil, 400, "invalid", "name"},
		"bad kind":         {"member", "POST", "/agents", `{"name":"x","kind":"shell"}`, nil, 400, "invalid", "kind"},
		"llm needs model":  {"member", "POST", "/agents", `{"name":"x","kind":"llm"}`, nil, 400, "invalid", "model"},
		"unknown field":    {"member", "POST", "/agents", `{"name":"x","kind":"echo","colour":"red"}`, nil, 400, "invalid", "colour"},
		"wrong type":       {"member", "POST", "/agents", `{"name":"x","kind":"echo","max_steps":"many"}`, nil, 400, "invalid", "max_steps"},
		"not json":         {"member", "POST", "/agents", `{`, nil, 400, "invalid", ""},
		"two values":       {"member", "POST", "/agents", `{"name":"x","kind":"echo"} {}`, nil, 400, "invalid", ""},
		"config name":      {"member", "POST", "/agents", `{"name":"helper","kind":"echo"}`, nil, 409, "conflict", "name"},
		"code name":        {"member", "POST", "/agents", `{"name":"echo","kind":"echo"}`, nil, 409, "conflict", "name"},
		"stored name":      {"member", "POST", "/agents", `{"name":"taken","kind":"echo"}`, nil, 409, "conflict", "name"},
		"viewer create":    {"viewer", "POST", "/agents", `{"name":"x","kind":"echo"}`, nil, 403, "forbidden", ""},
		"patch config":     {"member", "PATCH", "/agents/helper", `{"description":"x"}`, nil, 409, "read_only", "name"},
		"patch code":       {"member", "PATCH", "/agents/echo", `{"description":"x"}`, nil, 409, "read_only", "name"},
		"delete config":    {"admin", "DELETE", "/agents/helper", "", nil, 409, "read_only", "name"},
		"patch missing":    {"member", "PATCH", "/agents/ghost", `{"description":"x"}`, nil, 404, "not_found", ""},
		"delete missing":   {"member", "DELETE", "/agents/ghost", "", nil, 404, "not_found", ""},
		"rename":           {"member", "PATCH", "/agents/taken", `{"name":"other"}`, nil, 400, "invalid", "name"},
		"patch bad kind":   {"member", "PATCH", "/agents/taken", `{"kind":"shell"}`, nil, 400, "invalid", "kind"},
		"patch unknown":    {"member", "PATCH", "/agents/taken", `{"colour":"red"}`, nil, 400, "invalid", "colour"},
		"bad if-match":     {"member", "PATCH", "/agents/taken", `{}`, []string{"If-Match", "abc"}, 400, "invalid", "If-Match"},
		"viewer patch":     {"viewer", "PATCH", "/agents/taken", `{}`, nil, 403, "forbidden", ""},
		"viewer delete":    {"viewer", "DELETE", "/agents/taken", "", nil, 403, "forbidden", ""},
		"get fixed":        {"viewer", "GET", "/agents/helper", "", nil, 200, "", ""},
		"list bad cursor":  {"viewer", "GET", "/agents?cursor=%25%25", "", nil, 400, "invalid", "cursor"},
		"list limit large": {"viewer", "GET", "/agents?limit=501", "", nil, 400, "invalid", "limit"},
		"list limit zero":  {"viewer", "GET", "/agents?limit=0", "", nil, 400, "invalid", "limit"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			e.t = t
			e.want(e.do(tt.role, tt.method, tt.path, tt.body, tt.headers...), tt.code, tt.errCode, tt.field)
		})
	}
	e.t = t
	if n := len(e.do("viewer", "GET", "/audit", "").json(t)["items"].([]any)); n != 1 {
		t.Errorf("audit records = %d, want only the one create", n)
	}
}

func TestFixedAgentReadOnlyFlags(t *testing.T) {
	e := newEnv(t)
	m := e.do("viewer", "GET", "/agents/helper", "").json(t)
	if m["managed_by_file"] != true || m["managed_by_code"] != nil || m["version"] != float64(1) {
		t.Errorf("config agent = %v", m)
	}
	m = e.do("viewer", "GET", "/agents/echo", "").json(t)
	if m["managed_by_code"] != true || m["managed_by_file"] != nil {
		t.Errorf("code agent = %v", m)
	}
}

func TestAgentPaging(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"a1", "z9"} {
		e.want(e.do("member", "POST", "/agents", `{"name":"`+n+`","kind":"echo"}`), http.StatusCreated, "", "")
	}
	var names []string
	cursor := ""
	for range 10 {
		m := e.do("viewer", "GET", "/agents?limit=3&cursor="+cursor, "").json(t)
		for _, it := range m["items"].([]any) {
			names = append(names, it.(map[string]any)["name"].(string))
		}
		next, ok := m["next_cursor"].(string)
		if !ok {
			break
		}
		cursor = next
	}
	if strings.Join(names, ",") != "a1,echo,helper,z9" {
		t.Errorf("names = %v", names)
	}
}

func TestKeys(t *testing.T) {
	e := newEnv(t)
	r := e.do("admin", "POST", "/api-keys", `{"name":"ci","role":"member"}`)
	e.want(r, http.StatusCreated, "", "")
	m := r.json(t)
	key, id := m["key"].(string), m["id"].(string)
	if !strings.HasPrefix(key, "rsk_") || len(key) < 40 || m["prefix"] != key[:8] || m["created_by"] != "admin-key" {
		t.Errorf("created = %v", m)
	}
	stored, _ := e.st.ListKeys(t.Context())
	if len(stored) != 1 || stored[0].Hash != auth.Hash(key) || strings.Contains(fmt.Sprint(stored), key) {
		t.Errorf("stored = %+v", stored)
	}
	list := e.do("viewer", "GET", "/api-keys", "")
	e.want(list, http.StatusOK, "", "")
	if strings.Contains(string(list.body), key) {
		t.Error("list shows the key")
	}
	items := list.json(t)["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["name"] != "ci" || items[1].(map[string]any)["managed_by_file"] != true {
		t.Errorf("keys = %v", items)
	}

	tests := map[string]struct {
		role, method, path, body string
		headers                  []string
		code                     int
		errCode, field           string
	}{
		"member create":   {"member", "POST", "/api-keys", `{"name":"x","role":"member"}`, nil, 403, "forbidden", ""},
		"bad role":        {"admin", "POST", "/api-keys", `{"name":"x","role":"owner"}`, nil, 400, "invalid", "role"},
		"no name":         {"admin", "POST", "/api-keys", `{"role":"viewer"}`, nil, 400, "invalid", "name"},
		"long name":       {"admin", "POST", "/api-keys", `{"name":"` + strings.Repeat("n", 65) + `","role":"viewer"}`, nil, 400, "invalid", "name"},
		"revoke file key": {"admin", "DELETE", "/api-keys/file:ops", "", nil, 409, "read_only", "id"},
		"revoke missing":  {"admin", "DELETE", "/api-keys/k_nope", "", nil, 404, "not_found", ""},
		"revoke stale":    {"admin", "DELETE", "/api-keys/" + id, "", []string{"If-Match", "9"}, 409, "version_conflict", "If-Match"},
		"member revoke":   {"member", "DELETE", "/api-keys/" + id, "", nil, 403, "forbidden", ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			e.t = t
			e.want(e.do(tt.role, tt.method, tt.path, tt.body, tt.headers...), tt.code, tt.errCode, tt.field)
		})
	}
	e.t = t
	reloads := e.reloads.Load()
	e.want(e.do("admin", "DELETE", "/api-keys/"+id, "", "If-Match", "1"), http.StatusNoContent, "", "")
	if e.reloads.Load() != reloads+1 {
		t.Error("revoke did not reload")
	}
	items = e.do("viewer", "GET", "/api-keys", "").json(t)["items"].([]any)
	if items[0].(map[string]any)["revoked"] == nil {
		t.Errorf("keys = %v", items)
	}
}

func TestRunsSessionsAudit(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	start := e.clock.Now()
	for i, r := range []store.Run{
		{Agent: "echo", Protocol: "mcp", Status: store.RunCompleted, Started: start, Ended: start.Add(1500 * time.Millisecond), InputTokens: 3},
		{Agent: "helper", Protocol: "agui", Status: store.RunFailed, Error: "boom", Started: start.Add(time.Minute), Ended: start.Add(time.Minute)},
	} {
		r.ID = fmt.Sprintf("run%d", i)
		if err := e.st.SaveRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	_ = e.st.Append(ctx, "echo", "caller:abc/s1", rosetta.Message{Role: rosetta.RoleUser, Parts: []rosetta.Part{{Text: "hi"}}},
		rosetta.Message{Role: rosetta.RoleAgent, Parts: []rosetta.Part{{Data: json.RawMessage(`{"n":1}`)}}})

	runs := e.do("viewer", "GET", "/runs?status=failed", "").json(t)["items"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["error"] != "boom" {
		t.Errorf("failed runs = %v", runs)
	}
	from := start.Add(30 * time.Second).Format(time.RFC3339)
	if n := len(e.do("viewer", "GET", "/runs?from="+from, "").json(t)["items"].([]any)); n != 1 {
		t.Errorf("runs from %s = %d", from, n)
	}
	r := e.do("viewer", "GET", "/runs/run0", "")
	e.want(r, http.StatusOK, "", "")
	if m := r.json(t); m["duration_ms"] != float64(1500) || m["input_tokens"] != float64(3) {
		t.Errorf("run = %v", m)
	}

	sessions := e.do("viewer", "GET", "/sessions?agent=echo", "").json(t)["items"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %v", sessions)
	}
	s := sessions[0].(map[string]any)
	if s["session_id"] != "s1" || s["caller"] != "caller:abc" || s["messages"] != float64(2) {
		t.Errorf("session = %v", s)
	}
	id := s["id"].(string)
	detail := e.do("viewer", "GET", "/sessions/"+id, "")
	e.want(detail, http.StatusOK, "", "")
	history := detail.json(t)["history"].([]any)
	if len(history) != 2 || history[1].(map[string]any)["parts"].([]any)[0].(map[string]any)["data"].(map[string]any)["n"] != float64(1) {
		t.Errorf("history = %v", history)
	}

	tests := map[string]struct {
		role, method, path string
		code               int
		errCode, field     string
	}{
		"bad from":       {"viewer", "GET", "/runs?from=yesterday", 400, "invalid", "from"},
		"bad to":         {"viewer", "GET", "/audit?to=soon", 400, "invalid", "to"},
		"bad cursor":     {"viewer", "GET", "/runs?cursor=bm9wZQ", 400, "invalid", "cursor"},
		"run missing":    {"viewer", "GET", "/runs/nope", 404, "not_found", ""},
		"session gone":   {"viewer", "GET", "/sessions/nope", 404, "not_found", ""},
		"viewer delete":  {"viewer", "DELETE", "/sessions/" + id, 403, "forbidden", ""},
		"delete missing": {"member", "DELETE", "/sessions/nope", 404, "not_found", ""},
		"limit":          {"viewer", "GET", "/sessions?limit=x", 400, "invalid", "limit"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			e.t = t
			e.want(e.do(tt.role, tt.method, tt.path, ""), tt.code, tt.errCode, tt.field)
		})
	}
	e.t = t
	e.want(e.do("member", "DELETE", "/sessions/"+id, "", "If-Match", "9"), http.StatusConflict, "version_conflict", "If-Match")
	e.want(e.do("member", "DELETE", "/sessions/"+id, ""), http.StatusNoContent, "", "")

	audit := e.do("viewer", "GET", "/audit?action=session.delete", "").json(t)["items"].([]any)
	if len(audit) != 1 || audit[0].(map[string]any)["actor"] != "member-key" || audit[0].(map[string]any)["target"] != id {
		t.Errorf("audit = %v", audit)
	}
}

func TestEveryWriteAudited(t *testing.T) {
	e := newEnv(t)
	k := e.do("admin", "POST", "/api-keys", `{"name":"ci","role":"viewer"}`).json(t)
	writes := []struct{ role, method, path, body string }{
		{"member", "POST", "/agents", `{"name":"a","kind":"echo"}`},
		{"admin", "PATCH", "/agents/a", `{"description":"d"}`},
		{"member", "DELETE", "/agents/a", ""},
		{"admin", "DELETE", "/api-keys/" + k["id"].(string), ""},
	}
	for _, w := range writes {
		e.clock.Add(time.Second)
		if r := e.do(w.role, w.method, w.path, w.body); r.code >= 300 {
			t.Fatalf("%s %s: %d %s", w.method, w.path, r.code, r.body)
		}
	}
	items := e.do("viewer", "GET", "/audit", "").json(t)["items"].([]any)
	var got []string
	for _, it := range items {
		a := it.(map[string]any)
		got = append(got, fmt.Sprint(a["action"], " by ", a["actor"]))
	}
	want := "api_key.revoke by admin-key,agent.delete by member-key,agent.update by admin-key,agent.create by member-key,api_key.create by admin-key"
	if strings.Join(got, ",") != want {
		t.Errorf("audit =\n%v\nwant\n%v", strings.Join(got, ","), want)
	}
}

func TestInternalErrorHidesDetails(t *testing.T) {
	e := newEnv(t)
	_ = e.st.Close()
	r := e.do("viewer", "GET", "/runs", "")
	e.want(r, http.StatusInternalServerError, "internal", "")
	if strings.Contains(string(r.body), "sql") {
		t.Errorf("body leaks details: %s", r.body)
	}
	_, _ = io.Discard.Write(r.body)
}
