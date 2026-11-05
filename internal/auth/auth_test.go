package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
