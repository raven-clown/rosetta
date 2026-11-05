package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raven-clown/rosetta/agents/echo"
)

func TestAPIKeys(t *testing.T) {
	h := New(Options{APIKeys: []APIKey{{Name: "ci", Key: "k1", Role: "member"}}}, echo.New("echo")).Handler()
	requests := []struct {
		name, method, path, body string
	}{
		{"mcp", http.MethodPost, "/mcp", mcpInit},
		{"a2a", http.MethodPost, "/a2a/echo", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hi"}]}}}`},
		{"agui", http.MethodPost, "/agui/echo", aguiBody},
	}
	for _, r := range requests {
		t.Run(r.name, func(t *testing.T) {
			for _, key := range []string{"", "k1"} {
				req := httptest.NewRequestWithContext(t.Context(), r.method, r.path, strings.NewReader(r.body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				if key != "" {
					req.Header.Set("Authorization", "Bearer "+key)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				switch {
				case key == "" && rec.Code != http.StatusUnauthorized:
					t.Errorf("no key: status %d", rec.Code)
				case key != "" && rec.Code != http.StatusOK:
					t.Errorf("member key: status %d, body %s", rec.Code, rec.Body.String())
				}
			}
		})
	}
	if rec := do(t, h, http.MethodGet, "/a2a/echo/.well-known/agent-card.json", ""); rec.Code != http.StatusOK {
		t.Errorf("card: status %d", rec.Code)
	}
}
