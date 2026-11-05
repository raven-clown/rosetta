package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiddleware(t *testing.T) {
	keys := []Key{
		{Name: "ops", Key: "admin-key", Role: "admin"},
		{Name: "ci", Key: "member-key", Role: "member"},
		{Name: "dash", Key: "viewer-key", Role: "viewer"},
	}
	h := Middleware(keys, "/a2a", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	tests := map[string]struct {
		method, path, header string
		code                 int
		errCode              string
	}{
		"no key":          {"POST", "/mcp", "", http.StatusUnauthorized, "unauthorized"},
		"wrong key":       {"POST", "/mcp", "Bearer nope", http.StatusUnauthorized, "unauthorized"},
		"basic scheme":    {"POST", "/mcp", "Basic admin-key", http.StatusUnauthorized, "unauthorized"},
		"key prefix":      {"POST", "/mcp", "Bearer admin-ke", http.StatusUnauthorized, "unauthorized"},
		"admin":           {"POST", "/mcp", "Bearer admin-key", http.StatusTeapot, ""},
		"member":          {"POST", "/agui/echo", "bearer member-key", http.StatusTeapot, ""},
		"viewer":          {"POST", "/a2a/echo", "Bearer viewer-key", http.StatusForbidden, "forbidden"},
		"healthz":         {"GET", "/healthz", "", http.StatusTeapot, ""},
		"readyz":          {"GET", "/readyz", "", http.StatusTeapot, ""},
		"metrics":         {"GET", "/metrics", "", http.StatusTeapot, ""},
		"agent card":      {"GET", "/a2a/echo/.well-known/agent-card.json", "", http.StatusTeapot, ""},
		"card head":       {"HEAD", "/a2a/echo/.well-known/agent-card.json", "", http.StatusTeapot, ""},
		"default card":    {"GET", "/.well-known/agent-card.json", "", http.StatusTeapot, ""},
		"card post":       {"POST", "/a2a/echo/.well-known/agent-card.json", "", http.StatusUnauthorized, "unauthorized"},
		"card deeper":     {"GET", "/a2a/echo/v1/tasks/.well-known/agent-card.json", "", http.StatusUnauthorized, "unauthorized"},
		"card elsewhere":  {"GET", "/agui/echo/.well-known/agent-card.json", "", http.StatusUnauthorized, "unauthorized"},
		"card lookalike":  {"GET", "/a2a/echo/.well-known/agent-card.json/x", "", http.StatusUnauthorized, "unauthorized"},
		"card bad name":   {"GET", "/a2a/Echo/.well-known/agent-card.json", "", http.StatusUnauthorized, "unauthorized"},
		"healthz post":    {"POST", "/healthz", "", http.StatusUnauthorized, "unauthorized"},
		"healthz subpath": {"GET", "/healthz/x", "", http.StatusUnauthorized, "unauthorized"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.code {
				t.Fatalf("status %d, want %d", rec.Code, tt.code)
			}
			if tt.errCode == "" {
				return
			}
			var body struct {
				Error struct{ Code, Message string } `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != tt.errCode || body.Error.Message == "" {
				t.Errorf("body = %s", rec.Body.String())
			}
			if tt.code == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("WWW-Authenticate = %q", rec.Header().Get("WWW-Authenticate"))
			}
		})
	}
}

func TestCallerHeader(t *testing.T) {
	var got http.Header
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Header.Clone() })
	tests := map[string]struct {
		keys       []Key
		path, auth string
		wantCaller string
		wantAuth   string
	}{
		"key replaced by caller": {[]Key{{Name: "ci", Key: "k", Role: "member"}}, "/mcp", "Bearer k", "key:8254c329a92850f6", ""},
		"public strips spoof":    {[]Key{{Name: "ci", Key: "k", Role: "member"}}, "/healthz", "", "", ""},
		"no keys strips spoof":   {nil, "/mcp", "Bearer outside-token", "", "Bearer outside-token"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got = nil
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil)
			req.Header.Set(CallerHeader, "key:admin")
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			Middleware(tt.keys, "/a2a", next).ServeHTTP(httptest.NewRecorder(), req)
			if got == nil {
				t.Fatal("request did not reach the handler")
			}
			if got.Get(CallerHeader) != tt.wantCaller || got.Get("Authorization") != tt.wantAuth {
				t.Errorf("caller %q, authorization %q", got.Get(CallerHeader), got.Get("Authorization"))
			}
		})
	}
}

func TestCallerIDsDifferForSameName(t *testing.T) {
	keys := []Key{{Name: "ci", Key: "k1", Role: "member"}, {Name: "ci", Key: "k2", Role: "member"}}
	var ids []string
	h := Middleware(keys, "/a2a", http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ids = append(ids, r.Header.Get(CallerHeader))
	}))
	for _, k := range []string{"k1", "k2"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+k)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if len(ids) != 2 || ids[0] == ids[1] || strings.Contains(ids[0], "ci") {
		t.Errorf("caller IDs = %q", ids)
	}
}
