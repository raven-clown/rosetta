// Package auth checks API keys on rosetta's HTTP endpoints: static keys
// from config and keys stored through the admin API.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/raven-clown/rosetta/internal/httpx"
	"github.com/raven-clown/rosetta/registry"
	"github.com/raven-clown/rosetta/store"
)

// Key is one static API key and its role: admin, member or viewer.
type Key struct {
	Name string
	Key  string
	Role string
}

// Principal is who made a request.
type Principal struct {
	ID   string
	Name string
	Role string
}

// CanRun reports whether the role may run agents.
func (p Principal) CanRun() bool { return p.Role == "admin" || p.Role == "member" }

type principalKey struct{}

// FromContext returns the request's principal, set by Middleware.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// WithPrincipal returns ctx carrying p, for tests of handlers behind
// Middleware.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

type entry struct {
	hash []byte
	p    Principal
}

// macKey is random per process. Static keys from the config are compared
// as HMACs under it, so their digests mean nothing outside this process
// and are never stored anywhere.
var macKey = func() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}()

func mac(s string) []byte {
	h := hmac.New(sha256.New, macKey)
	_, _ = h.Write([]byte(s))
	return h.Sum(nil)
}

// Keys is the set of keys a server accepts. Static keys are fixed; stored
// keys are swapped in by SetStored on every reload, so serving a request
// reads no SQL.
type Keys struct {
	static []entry
	stored atomic.Pointer[[]entry]
}

const (
	idSalt  = "rosetta caller id"
	idIters = 100_000
)

// callerID derives a stable ID from a static key with a slow hash, so the
// ID can be stored with sessions without making weak keys easy to recover.
func callerID(key string) string {
	b, err := pbkdf2.Key(sha256.New, key, []byte(idSalt), idIters, 8)
	if err != nil {
		panic(err)
	}
	return "key:" + hex.EncodeToString(b)
}

// MinKeyLength is the shortest key a person may choose for storing, such
// as ROSETTA_BOOTSTRAP_ADMIN_KEY.
const MinKeyLength = 32

// Hash returns the hex SHA-256 of a key, as stored. Stored keys are made
// by rosetta from 32 random bytes, or are a bootstrap key of at least
// MinKeyLength characters, so a fast hash is enough.
func Hash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// NewKeys returns a key set holding the static keys.
func NewKeys(static []Key) *Keys {
	k := &Keys{}
	for _, s := range static {
		k.static = append(k.static, entry{hash: mac(s.Key), p: Principal{ID: callerID(s.Key), Name: s.Name, Role: s.Role}})
	}
	empty := []entry{}
	k.stored.Store(&empty)
	return k
}

// SetStored replaces the stored keys. Revoked keys are left out.
func (k *Keys) SetStored(keys []store.Key) {
	var out []entry
	for _, s := range keys {
		if !s.Revoked.IsZero() {
			continue
		}
		b, err := hex.DecodeString(s.Hash)
		if err != nil || len(b) != sha256.Size {
			continue
		}
		out = append(out, entry{hash: b, p: Principal{ID: "key:" + s.ID, Name: s.Name, Role: s.Role}})
	}
	k.stored.Store(&out)
}

// Len returns how many keys are accepted.
func (k *Keys) Len() int {
	if k == nil {
		return 0
	}
	return len(k.static) + len(*k.stored.Load())
}

// lookup compares against every key, so the time taken does not depend on
// which key matched.
func (k *Keys) lookup(token string) (Principal, bool) {
	if k == nil || token == "" {
		return Principal{}, false
	}
	var found Principal
	ok := false
	staticMAC := mac(token)
	for _, e := range k.static {
		if subtle.ConstantTimeCompare(staticMAC, e.hash) == 1 {
			found, ok = e.p, true
		}
	}
	stored, _ := hex.DecodeString(Hash(token))
	for _, e := range *k.stored.Load() {
		if subtle.ConstantTimeCompare(stored, e.hash) == 1 {
			found, ok = e.p, true
		}
	}
	return found, ok
}

// Options says where the protocol and admin routes are, and whether auth
// is off. Open is fixed when the server is built, so revoking the last key
// never opens the server.
type Options struct {
	A2APath   string
	AdminPath string
	Open      bool
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

// CallerHeader is set to the accepted key's caller ID, which is unique per
// key and cannot be guessed from the key's name.
const CallerHeader = httpx.CallerHeader

// Middleware lets requests through to next with a valid key. Protocol
// routes need a role that may run agents; the admin API takes any role
// and leaves write rules to its handlers. Health, metrics and agent cards
// stay public. An accepted key is removed from the request, so it is never
// forwarded upstream, and replaced by CallerHeader and a Principal in the
// context.
//
// With Options.Open, protocol routes are open and the admin API answers
// loopback callers that did not come through a proxy, as an admin.
// Otherwise a request without a valid key is refused, even when the set
// is empty.
func Middleware(keys *Keys, opts Options, next http.Handler) http.Handler {
	if opts.A2APath == "" {
		opts.A2APath = "/a2a"
	}
	if opts.AdminPath == "" {
		opts.AdminPath = "/admin/v1/"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del(CallerHeader)
		admin := strings.HasPrefix(r.URL.Path, opts.AdminPath)
		if opts.Open {
			if !admin {
				next.ServeHTTP(w, r)
				return
			}
			if !loopback(r) || !localName(r) {
				fail(w, http.StatusForbidden, "forbidden", "auth is off, so the admin API only answers on this machine")
				return
			}
			r.Header.Del("Authorization")
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), Principal{ID: "local", Name: "local", Role: "admin"})))
			return
		}
		if isPublic(r, opts.A2APath) {
			next.ServeHTTP(w, r)
			return
		}
		p, ok := keys.lookup(httpx.BearerToken(r.Header.Get("Authorization")))
		switch {
		case !ok:
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(w, http.StatusUnauthorized, "unauthorized", "send a valid API key as Authorization: Bearer <key>")
			return
		case !admin && !p.CanRun():
			fail(w, http.StatusForbidden, "forbidden", "this key's role cannot run agents")
			return
		}
		r.Header.Del("Authorization")
		r.Header.Set(CallerHeader, p.ID)
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// loopback trusts the connection's address, so a request that names a
// forwarding proxy is not local even when the proxy runs on this machine.
func loopback(r *http.Request) bool {
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Real-Ip"} {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// localName checks the Host and any Origin, so a web page the operator
// opens cannot reach the open admin API, by DNS rebinding or by sending
// a request from its own origin.
func localName(r *http.Request) bool {
	if !localHost(r.Host) {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && localHost(u.Host)
}

func localHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func fail(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
