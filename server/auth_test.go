package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raven-clown/rosetta/agents/echo"
	"github.com/raven-clown/rosetta/internal/auth"
	"github.com/raven-clown/rosetta/registry"
	"github.com/raven-clown/rosetta/store"
)

func authed(t *testing.T, h http.Handler, method, path, key string) int {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(aguiBody))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestKeysAdminAndRegistry(t *testing.T) {
	keys := NewKeys([]APIKey{{Name: "dash", Key: "viewer-key", Role: "viewer"}})
	keys.SetStored([]store.Key{{ID: "k1", Name: "ci", Role: "member", Hash: auth.Hash("stored-key")}})
	reg, _ := registry.New(echo.New("echo"))
	admin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.FromContext(r.Context())
		_, _ = io.WriteString(w, p.Name)
	})
	h := New(Options{Keys: keys, Admin: admin, Registry: reg}).Handler()

	if code := authed(t, h, http.MethodPost, "/agui/echo", "stored-key"); code != http.StatusOK {
		t.Errorf("stored key run: %d", code)
	}
	if code := authed(t, h, http.MethodPost, "/agui/echo", "viewer-key"); code != http.StatusForbidden {
		t.Errorf("viewer run: %d", code)
	}
	if code := authed(t, h, http.MethodGet, "/admin/v1/agents", "viewer-key"); code != http.StatusOK {
		t.Errorf("viewer admin read: %d", code)
	}
	if code := authed(t, h, http.MethodGet, "/admin/v1/agents", ""); code != http.StatusUnauthorized {
		t.Errorf("admin without key: %d", code)
	}
	_ = reg.Set(echo.New("other"))
	if code := authed(t, h, http.MethodPost, "/agui/other", "stored-key"); code != http.StatusOK {
		t.Errorf("agent added at runtime: %d", code)
	}
	if code := authed(t, h, http.MethodPost, "/agui/echo", "stored-key"); code != http.StatusNotFound {
		t.Errorf("agent removed at runtime: %d", code)
	}
	keys.SetStored(nil)
	if code := authed(t, h, http.MethodPost, "/agui/other", "stored-key"); code != http.StatusUnauthorized {
		t.Errorf("revoked key: %d", code)
	}
}

func TestEmptyKeySetIsNotOpen(t *testing.T) {
	keys := NewKeys(nil)
	h := New(Options{Keys: keys}, echo.New("echo")).Handler()
	if code := authed(t, h, http.MethodPost, "/agui/echo", ""); code != http.StatusUnauthorized {
		t.Errorf("no keys at all: %d, want 401", code)
	}
}

func TestProtocolUnderAdminPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("no panic for a protocol path under /admin")
		}
	}()
	New(Options{Protocols: Protocols{AGUI: Toggle{Path: "/admin/v1/chat"}}}, echo.New("echo"))
}

func TestRegistryAndAgentsPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	reg, _ := registry.New()
	New(Options{Registry: reg}, echo.New("echo"))
}

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
