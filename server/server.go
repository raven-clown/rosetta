// Package server serves agents over every protocol rosetta supports: one
// http.Handler for MCP, A2A and AG-UI, and ServeStdio for ACP or MCP.
package server

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/internal/auth"
	"github.com/raven-clown/rosetta/internal/metrics"
	"github.com/raven-clown/rosetta/internal/run"
	"github.com/raven-clown/rosetta/protocol"
	"github.com/raven-clown/rosetta/protocol/a2a"
	"github.com/raven-clown/rosetta/protocol/agui"
	"github.com/raven-clown/rosetta/protocol/mcp"
	"github.com/raven-clown/rosetta/registry"
	"github.com/raven-clown/rosetta/session"
)

// Toggle switches a protocol off or moves it to another path.
type Toggle struct {
	Disabled bool
	Path     string
}

// Protocols holds a Toggle per HTTP protocol. The zero value serves all
// of them on their default paths.
type Protocols struct {
	MCP  Toggle
	A2A  Toggle
	AGUI Toggle
}

// Options configures a Server. A nil Store keeps sessions in memory for
// a day, and a nil Logger discards logs.
type Options struct {
	Logger       *slog.Logger
	Store        session.Store
	DefaultAgent string
	BaseURL      string
	Protocols    Protocols
	Checks       []Check
	APIKeys      []APIKey
	Runs         RunRecorder

	// Keys replaces APIKeys with a set the caller can update while serving.
	// With Keys set, auth is on even while the set is empty. With neither
	// Keys nor APIKeys, auth is off.
	Keys *Keys
	// Admin is served under /admin/v1/ when set.
	Admin http.Handler
	// Registry replaces the agents passed to New with a list the caller
	// can change while serving.
	Registry *registry.Registry
}

// Keys is a set of API keys that can change while serving.
type Keys = auth.Keys

// NewKeys returns a key set holding static keys.
func NewKeys(static []APIKey) *Keys {
	keys := make([]auth.Key, len(static))
	for i, k := range static {
		keys[i] = auth.Key(k)
	}
	return auth.NewKeys(keys)
}

// RunRecorder saves a record of every run. *store.Store implements it.
// With none, runs are not recorded.
type RunRecorder = run.Recorder

// APIKey is a static key with a role: admin, member or viewer. When
// Options.APIKeys is empty the server checks no keys.
type APIKey struct {
	Name string
	Key  string
	Role string
}

func (o Options) withDefaults() Options {
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	if o.Store == nil {
		o.Store = session.NewMemory(24*time.Hour, nil)
	}
	return o
}

const adminPath = "/admin/v1/"

// Server serves agents over the HTTP protocols.
type Server struct {
	handler http.Handler
}

// New returns a Server for the agents, or for Options.Registry when set.
// It panics when two agents share a name, a name is not valid, or both a
// registry and agents are given, since those are programming errors.
func New(opts Options, agents ...rosetta.Agent) *Server {
	opts = opts.withDefaults()
	reg := opts.Registry
	if reg != nil && len(agents) > 0 {
		panic("server: pass agents or Options.Registry, not both")
	}
	if opts.Keys != nil && len(opts.APIKeys) > 0 {
		panic("server: pass Options.APIKeys or Options.Keys, not both; NewKeys takes the static keys")
	}
	if reg == nil {
		var err error
		if reg, err = registry.New(agents...); err != nil {
			panic(err)
		}
	}
	m := metrics.New()
	runOpts := []run.Option{run.WithLogger(opts.Logger), run.WithObserver(m)}
	if opts.Runs != nil {
		runOpts = append(runOpts, run.WithRecorder(opts.Runs))
	}
	runner := run.New(opts.Store, runOpts...)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	mux.Handle("GET /readyz", readyz(opts.Checks, opts.Logger))
	mux.Handle("GET /metrics", m.Handler())
	p := opts.Protocols
	adapters := []struct {
		toggle  Toggle
		adapter protocol.HTTPAdapter
	}{
		{p.MCP, mcp.New(mcp.Options{Path: p.MCP.Path})},
		{p.A2A, a2a.New(a2a.Options{Path: p.A2A.Path, BaseURL: opts.BaseURL, DefaultAgent: opts.DefaultAgent})},
		{p.AGUI, agui.New(agui.Options{Path: p.AGUI.Path})},
	}
	for _, a := range adapters {
		if a.toggle.Disabled {
			continue
		}
		// Requests under /admin take any role, so a protocol there would
		// let a viewer key run agents.
		if p := a.toggle.Path; p == "/admin" || strings.HasPrefix(p, "/admin/") {
			panic("server: protocol path " + p + " is under /admin, which the admin API owns")
		}
		if err := a.adapter.Mount(mux, reg, runner); err != nil {
			panic(err)
		}
	}
	if opts.Admin != nil {
		mux.Handle(adminPath, opts.Admin)
	}
	keys := opts.Keys
	open := keys == nil && len(opts.APIKeys) == 0
	if keys == nil {
		keys = NewKeys(opts.APIKeys)
	}
	a2aPath := p.A2A.Path
	if a2aPath == "" {
		a2aPath = "/a2a"
	}
	authed := auth.Middleware(keys, auth.Options{A2APath: a2aPath, AdminPath: adminPath, Open: open}, mux)
	return &Server{handler: m.Middleware(mux, authed)}
}

// Handler returns the HTTP handler for every enabled protocol.
func (s *Server) Handler() http.Handler { return s.handler }
