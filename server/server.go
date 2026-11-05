// Package server serves agents over every protocol rosetta supports: one
// http.Handler for MCP, A2A and AG-UI, and ServeStdio for ACP or MCP.
package server

import (
	"io"
	"log/slog"
	"net/http"
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
}

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

// Server serves a fixed set of agents.
type Server struct {
	handler http.Handler
}

// New returns a Server for the agents. It panics when two agents share a
// name or a name is not valid, since that is a programming error.
func New(opts Options, agents ...rosetta.Agent) *Server {
	opts = opts.withDefaults()
	reg, err := registry.New(agents...)
	if err != nil {
		panic(err)
	}
	m := metrics.New()
	runner := run.New(opts.Store, run.WithLogger(opts.Logger), run.WithObserver(m))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	mux.Handle("GET /readyz", readyz(opts.Checks))
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
		if err := a.adapter.Mount(mux, reg, runner); err != nil {
			panic(err)
		}
	}
	if len(opts.APIKeys) == 0 {
		return &Server{handler: mux}
	}
	keys := make([]auth.Key, len(opts.APIKeys))
	for i, k := range opts.APIKeys {
		keys[i] = auth.Key(k)
	}
	return &Server{handler: auth.Middleware(keys, mux)}
}

// Handler returns the HTTP handler for every enabled protocol.
func (s *Server) Handler() http.Handler { return s.handler }
