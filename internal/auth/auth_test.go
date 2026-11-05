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
	h := Middleware(keys, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	tests := map[string]struct {
		path, header string
		code         int
		errCode      string
	}{
		"no key":          {"/mcp", "", http.StatusUnauthorized, "unauthorized"},
		"wrong key":       {"/mcp", "Bearer nope", http.StatusUnauthorized, "unauthorized"},
		"basic scheme":    {"/mcp", "Basic admin-key", http.StatusUnauthorized, "unauthorized"},
		"key prefix":      {"/mcp", "Bearer admin-ke", http.StatusUnauthorized, "unauthorized"},
		"admin":           {"/mcp", "Bearer admin-key", http.StatusTeapot, ""},
		"member":          {"/agui/echo", "bearer member-key", http.StatusTeapot, ""},
		"viewer":          {"/a2a/echo", "Bearer viewer-key", http.StatusForbidden, "forbidden"},
		"healthz":         {"/healthz", "", http.StatusTeapot, ""},
		"readyz":          {"/readyz", "", http.StatusTeapot, ""},
		"metrics":         {"/metrics", "", http.StatusTeapot, ""},
		"agent card":      {"/a2a/echo/.well-known/agent-card.json", "", http.StatusTeapot, ""},
		"default card":    {"/.well-known/agent-card.json", "", http.StatusTeapot, ""},
		"card lookalike":  {"/a2a/echo/.well-known/agent-card.json/x", "", http.StatusUnauthorized, "unauthorized"},
		"healthz subpath": {"/healthz/x", "", http.StatusUnauthorized, "unauthorized"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, tt.path, nil)
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
