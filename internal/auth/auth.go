// Package auth checks static API keys on rosetta's HTTP endpoints.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/raven-clown/rosetta/internal/httpx"
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

var public = map[string]bool{"/healthz": true, "/readyz": true, "/metrics": true}

const cardSuffix = "/.well-known/agent-card.json"

// Middleware lets requests through to next only with a key whose role may
// run agents. Health, metrics and agent cards stay public.
func Middleware(keys []Key, next http.Handler) http.Handler {
	entries := make([]entry, len(keys))
	for i, k := range keys {
		entries[i] = entry{digest: sha256.Sum256([]byte(k.Key)), role: k.Role}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if public[r.URL.Path] || strings.HasSuffix(r.URL.Path, cardSuffix) {
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
