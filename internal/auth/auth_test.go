package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raven-clown/rosetta/store"
)

func TestMiddleware(t *testing.T) {
	keys := []Key{
		{Name: "ops", Key: "admin-key", Role: "admin"},
		{Name: "ci", Key: "member-key", Role: "member"},
		{Name: "dash", Key: "viewer-key", Role: "viewer"},
	}
	h := Middleware(NewKeys(keys), Options{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
		"admin api viewer": {"GET", "/admin/v1/agents", "Bearer viewer-key", http.StatusTeapot, ""},
		"admin api no key": {"GET", "/admin/v1/agents", "", http.StatusUnauthorized, "unauthorized"},
		"admin api wrong":  {"POST", "/admin/v1/agents", "Bearer nope", http.StatusUnauthorized, "unauthorized"},
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
		"key replaced by caller": {[]Key{{Name: "ci", Key: "k", Role: "member"}}, "/mcp", "Bearer k", callerID("k"), ""},
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
			Middleware(NewKeys(tt.keys), Options{}, next).ServeHTTP(httptest.NewRecorder(), req)
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
	h := Middleware(NewKeys(keys), Options{}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
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

func serveAs(t *testing.T, h http.Handler, method, path, token, remote string) int {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	if remote != "" {
		req.RemoteAddr = remote
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestStoredKeys(t *testing.T) {
	keys := NewKeys([]Key{{Name: "ops", Key: "static", Role: "admin"}})
	var seen Principal
	h := Middleware(keys, Options{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = FromContext(r.Context())
		w.WriteHeader(http.StatusTeapot)
	}))
	keys.SetStored([]store.Key{
		{ID: "k1", Name: "ci", Role: "member", Hash: Hash("stored")},
		{ID: "k2", Name: "old", Role: "member", Hash: Hash("revoked"), Revoked: time.Now()},
		{ID: "k3", Name: "broken", Role: "member", Hash: "not hex"},
	})
	if keys.Len() != 2 {
		t.Errorf("len = %d", keys.Len())
	}
	if code := serveAs(t, h, "POST", "/mcp", "stored", ""); code != http.StatusTeapot {
		t.Errorf("stored key: %d", code)
	}
	if seen.ID != "key:k1" || seen.Name != "ci" || seen.Role != "member" {
		t.Errorf("principal = %+v", seen)
	}
	if code := serveAs(t, h, "POST", "/mcp", "revoked", ""); code != http.StatusUnauthorized {
		t.Errorf("revoked key: %d", code)
	}
	keys.SetStored(nil)
	if code := serveAs(t, h, "POST", "/mcp", "stored", ""); code != http.StatusUnauthorized {
		t.Errorf("key after removal: %d", code)
	}
	if code := serveAs(t, h, "POST", "/mcp", "static", ""); code != http.StatusTeapot || seen.Name != "ops" || seen.Role != "admin" {
		t.Errorf("static key: %d, %+v", code, seen)
	}
}

func TestNoKeys(t *testing.T) {
	var seen Principal
	var reached bool
	h := Middleware(nil, Options{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, reached = FromContext(r.Context())
		w.WriteHeader(http.StatusTeapot)
	}))
	tests := map[string]struct {
		path, remote string
		code         int
		admin        bool
	}{
		"protocol open":     {"/mcp", "203.0.113.9:4000", http.StatusTeapot, false},
		"admin loopback v4": {"/admin/v1/status", "127.0.0.1:4000", http.StatusTeapot, true},
		"admin loopback v6": {"/admin/v1/status", "[::1]:4000", http.StatusTeapot, true},
		"admin remote":      {"/admin/v1/status", "203.0.113.9:4000", http.StatusForbidden, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			seen, reached = Principal{}, false
			code := serveAs(t, h, "GET", tt.path, "", tt.remote)
			if code != tt.code {
				t.Fatalf("status %d, want %d", code, tt.code)
			}
			if tt.admin && (!reached || seen.Role != "admin") {
				t.Errorf("principal = %+v", seen)
			}
		})
	}
}

func TestCallerIDIsSlowHash(t *testing.T) {
	id := callerID("dev-key")
	plain := sha256.Sum256([]byte("dev-key"))
	if len(id) != len("key:")+16 || !strings.HasPrefix(id, "key:") || strings.Contains(id, hex.EncodeToString(plain[:8])) {
		t.Errorf("caller ID %q", id)
	}
	if callerID("dev-key") != id {
		t.Error("caller ID is not stable")
	}
}
