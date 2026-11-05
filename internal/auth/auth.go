// Package auth checks static API keys on rosetta's HTTP endpoints.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/raven-clown/rosetta/internal/httpx"
	"github.com/raven-clown/rosetta/registry"
)

// Key is one API key and its role: admin, member or viewer.
type Key struct {
	Name string
	Key  string
	Role string
}

type entry struct {
	digest [sha256.Size]byte
	role   string
}

var public = map[string]bool{"/healthz": true, "/readyz": true, "/metrics": true, cardPath: true}

const cardPath = "/.well-known/agent-card.json"

// isPublic allows only reads of health, metrics and agent cards, matched
// exactly so no protocol route can pass for one.
func isPublic(r *http.Request, a2aPath string) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if public[r.URL.Path] {
		return true
	}
	rest, ok := strings.CutPrefix(r.URL.Path, a2aPath+"/")
	if !ok {
		return false
	}
	agent, ok := strings.CutSuffix(rest, cardPath)
	return ok && registry.ValidName(agent)
}

// Middleware lets requests through to next only with a key whose role may
// run agents. Health, metrics and agent cards under a2aPath stay public.
func Middleware(keys []Key, a2aPath string, next http.Handler) http.Handler {
	entries := make([]entry, len(keys))
	for i, k := range keys {
		entries[i] = entry{digest: sha256.Sum256([]byte(k.Key)), role: k.Role}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublic(r, a2aPath) {
			next.ServeHTTP(w, r)
			return
		}
		role := lookup(entries, httpx.BearerToken(r.Header.Get("Authorization")))
		switch role {
		case "":
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(w, http.StatusUnauthorized, "unauthorized", "send a valid API key as Authorization: Bearer <key>")
		case "admin", "member":
			next.ServeHTTP(w, r)
		default:
			fail(w, http.StatusForbidden, "forbidden", "this key's role cannot run agents")
		}
	})
}

// lookup compares against every key, so the time taken does not depend on
// which key matched.
func lookup(entries []entry, token string) string {
	if token == "" {
		return ""
	}
	d := sha256.Sum256([]byte(token))
	role := ""
	for _, e := range entries {
		if subtle.ConstantTimeCompare(d[:], e.digest[:]) == 1 {
			role = e.role
		}
	}
	return role
}

func fail(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
