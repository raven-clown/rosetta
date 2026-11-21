// Package admin serves rosetta's admin API under /admin/v1/, described by
// api/admin/openapi.yaml.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	apispec "github.com/raven-clown/rosetta/api/admin"
	"github.com/raven-clown/rosetta/config"
	"github.com/raven-clown/rosetta/internal/auth"
	"github.com/raven-clown/rosetta/store"
)

// Prefix is where the API is mounted.
const Prefix = "/admin/v1"

// FixedAgent is an agent the API can show but not change: from config, or
// registered in Go.
type FixedAgent struct {
	Spec   config.Agent
	Source string
}

// Sources of an agent.
const (
	SourceCode   = "code"
	SourceConfig = "config"
	SourceAPI    = "api"
)

// FixedKey is a key from the config file, shown without its secret.
type FixedKey struct {
	Name string
	Role string
}

// Status is the part of GET /status the server knows: which protocols are
// on and how each upstream answers.
type Status struct {
	Protocols map[string]bool
	Upstreams map[string]string
}

// Options configures the API. Store is required. Validate checks an agent
// before it is stored and returns a *config.FieldError for a bad field.
// Reload is called after every write, so this process sees it at once.
type Options struct {
	Store    *store.Store
	Fixed    func() []FixedAgent
	Keys     []FixedKey
	Validate func(config.Agent) error
	Reload   func(context.Context)
	Status   func(context.Context) Status
	Version  string
	Started  time.Time
	Now      func() time.Time
	Logger   *slog.Logger
}

// Handler serves the API.
type Handler struct {
	opts Options
	mux  *http.ServeMux
}

type route struct {
	method, path string
	handle       func(*Handler, http.ResponseWriter, *http.Request) error
}

var routes = []route{
	{"GET", "/status", (*Handler).status},
	{"GET", "/openapi.yaml", (*Handler).openapi},
	{"GET", "/agents", (*Handler).listAgents},
	{"POST", "/agents", (*Handler).createAgent},
	{"GET", "/agents/{name}", (*Handler).getAgent},
	{"PATCH", "/agents/{name}", (*Handler).updateAgent},
	{"DELETE", "/agents/{name}", (*Handler).deleteAgent},
	{"GET", "/api-keys", (*Handler).listKeys},
	{"POST", "/api-keys", (*Handler).createKey},
	{"DELETE", "/api-keys/{id}", (*Handler).revokeKey},
	{"GET", "/runs", (*Handler).listRuns},
	{"GET", "/runs/{id}", (*Handler).getRun},
	{"GET", "/sessions", (*Handler).listSessions},
	{"GET", "/sessions/{id}", (*Handler).getSession},
	{"DELETE", "/sessions/{id}", (*Handler).deleteSession},
	{"GET", "/audit", (*Handler).listAudit},
}

// New returns the API handler.
func New(opts Options) *Handler {
	if opts.Fixed == nil {
		opts.Fixed = func() []FixedAgent { return nil }
	}
	if opts.Validate == nil {
		opts.Validate = func(config.Agent) error { return nil }
	}
	if opts.Reload == nil {
		opts.Reload = func(context.Context) {}
	}
	if opts.Status == nil {
		opts.Status = func(context.Context) Status { return Status{} }
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Started.IsZero() {
		opts.Started = opts.Now()
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	h := &Handler{opts: opts, mux: http.NewServeMux()}
	for _, rt := range routes {
		handle := rt.handle
		h.mux.HandleFunc(rt.method+" "+Prefix+rt.path, func(w http.ResponseWriter, r *http.Request) {
			if err := handle(h, w, r); err != nil {
				h.fail(w, r, err)
			}
		})
	}
	h.mux.HandleFunc(Prefix+"/", func(w http.ResponseWriter, r *http.Request) {
		h.fail(w, r, &apiError{http.StatusNotFound, "not_found", "no such endpoint: " + r.Method + " " + r.URL.Path, ""})
	})
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

type apiError struct {
	status  int
	code    string
	message string
	field   string
}

func (e *apiError) Error() string { return e.message }

func invalid(field, format string, args ...any) error {
	return &apiError{http.StatusBadRequest, "invalid", fmt.Sprintf(format, args...), field}
}

func notFound(what string) error {
	return &apiError{http.StatusNotFound, "not_found", what + " not found", ""}
}

func forbidden(msg string) error { return &apiError{http.StatusForbidden, "forbidden", msg, ""} }

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	var fe *config.FieldError
	switch {
	case errors.As(err, &ae):
	case errors.As(err, &fe):
		ae = &apiError{http.StatusBadRequest, "invalid", fe.Message, fe.Field}
	case errors.Is(err, store.ErrNotFound):
		ae = &apiError{http.StatusNotFound, "not_found", "not found", ""}
	case errors.Is(err, store.ErrVersion):
		ae = &apiError{http.StatusConflict, "version_conflict", "it was changed by someone else; read it again and retry", "If-Match"}
	case errors.Is(err, store.ErrConflict):
		ae = &apiError{http.StatusConflict, "conflict", "the name is taken", "name"}
	case errors.Is(err, store.ErrBadCursor):
		ae = &apiError{http.StatusBadRequest, "invalid", "cursor is not one this API gave out", "cursor"}
	default:
		h.opts.Logger.Error("admin API", "method", r.Method, "path", r.URL.Path, "err", err)
		ae = &apiError{http.StatusInternalServerError, "internal", "internal error, see the server log", ""}
	}
	body := map[string]string{"code": ae.code, "message": ae.message}
	if ae.field != "" {
		body["field"] = ae.field
	}
	writeJSON(w, ae.status, map[string]any{"error": body})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func etag(w http.ResponseWriter, version int64) {
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(version, 10)))
}

// ifMatch returns the version in If-Match, or 0 when there is none.
func ifMatch(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		return 0, nil
	}
	v := strings.Trim(strings.TrimPrefix(raw, "W/"), `"`)
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 {
		return 0, invalid("If-Match", "If-Match must be a version such as \"3\", not %q", raw)
	}
	return n, nil
}

const maxBody = 1 << 20

// decode reads a JSON object, rejecting unknown fields. The body must say
// it is JSON, so a page cannot send a write as a plain form post, which
// browsers allow across origins without asking.
func decode(r *http.Request, v any) error {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return &apiError{http.StatusUnsupportedMediaType, "invalid", "send the body as Content-Type: application/json", "Content-Type"}
	}
	return decodeFrom(http.MaxBytesReader(nil, r.Body, maxBody), v)
}

func decodeFrom(rd io.Reader, v any) error {
	dec := json.NewDecoder(rd)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		field := ""
		if _, name, ok := strings.Cut(err.Error(), "unknown field "); ok {
			field = strings.Trim(name, `"`)
		}
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			field = te.Field
		}
		return invalid(field, "the body is not valid: %v", err)
	}
	if dec.More() {
		return invalid("", "the body holds more than one JSON value")
	}
	return nil
}

func principal(r *http.Request) auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

// canWrite allows admin and member, the roles that may change agents and
// sessions.
func canWrite(r *http.Request) error {
	if !principal(r).CanRun() {
		return forbidden("this key's role can only read")
	}
	return nil
}

func isAdmin(r *http.Request) error {
	if principal(r).Role != "admin" {
		return forbidden("only an admin key can manage API keys")
	}
	return nil
}

func actor(r *http.Request) string {
	p := principal(r)
	if p.Name == "" {
		return p.ID
	}
	return p.Name
}

func page(r *http.Request) (store.Page, error) {
	q := r.URL.Query()
	p := store.Page{Cursor: q.Get("cursor")}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > store.MaxLimit {
			return p, invalid("limit", "limit must be between 1 and %d", store.MaxLimit)
		}
		p.Limit = n
	}
	return p, nil
}

func timeParam(r *http.Request, name string) (time.Time, error) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, invalid(name, "%s must be an RFC 3339 time such as 2026-11-21T09:00:00Z", name)
	}
	return t, nil
}

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

type list[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func newList[T any](items []T, next string) list[T] {
	if items == nil {
		items = []T{}
	}
	return list[T]{Items: items, NextCursor: next}
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) error {
	st := h.opts.Status(r.Context())
	if st.Protocols == nil {
		st.Protocols = map[string]bool{}
	}
	ups := map[string]string{}
	for k, v := range st.Upstreams {
		ups[k] = v
	}
	ups["storage"] = "ok"
	if err := h.opts.Store.Ping(r.Context()); err != nil {
		h.opts.Logger.Error("admin status: storage", "err", err)
		ups["storage"] = "unavailable"
	}
	agents, err := h.allAgents(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":        h.opts.Version,
		"started":        ts(h.opts.Started),
		"uptime_seconds": int64(h.opts.Now().Sub(h.opts.Started).Seconds()),
		"storage":        map[string]string{"dialect": h.opts.Store.Dialect()},
		"protocols":      st.Protocols,
		"upstreams":      ups,
		"agents":         len(agents),
	})
	return nil
}

func (h *Handler) openapi(w http.ResponseWriter, _ *http.Request) error {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(apispec.OpenAPI)
	return nil
}
