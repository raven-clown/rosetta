// Package a2a serves agents over A2A, with the JSON-RPC and REST bindings
// and an agent card per agent.
package a2a

import (
	"net/http"
	"strings"
	"sync"

	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/protocol"
)

// Options configures the adapter. BaseURL is the public URL rosetta is
// reached at. When empty, it comes from each request's Host header.
type Options struct {
	Path         string
	BaseURL      string
	DefaultAgent string
}

// Adapter serves A2A.
type Adapter struct {
	opts Options
}

// New returns an A2A adapter. Path defaults to /a2a.
func New(opts Options) *Adapter {
	if opts.Path == "" {
		opts.Path = "/a2a"
	}
	opts.BaseURL = strings.TrimSuffix(opts.BaseURL, "/")
	return &Adapter{opts: opts}
}

// Name returns "a2a".
func (a *Adapter) Name() string { return "a2a" }

type endpoint struct {
	jsonrpc http.Handler
	rest    http.Handler
}

// Mount registers the A2A routes for every agent on mux.
func (a *Adapter) Mount(mux *http.ServeMux, agents protocol.Registry, run protocol.Runner) error {
	var mu sync.Mutex
	endpoints := map[string]*endpoint{}
	go func() {
		for range agents.Changes() {
			mu.Lock()
			clear(endpoints)
			mu.Unlock()
		}
	}()
	lookup := func(w http.ResponseWriter, r *http.Request) (rosetta.Agent, *endpoint, bool) {
		agent, ok := agents.Get(r.PathValue("agent"))
		if !ok {
			http.Error(w, "unknown agent", http.StatusNotFound)
			return nil, nil, false
		}
		mu.Lock()
		defer mu.Unlock()
		ep, ok := endpoints[agent.Card().Name]
		if !ok {
			handler := a2asrv.NewHandler(newExecutor(agent, run))
			ep = &endpoint{jsonrpc: a2asrv.NewJSONRPCHandler(handler), rest: a2asrv.NewRESTHandler(handler)}
			endpoints[agent.Card().Name] = ep
		}
		return agent, ep, true
	}

	base := a.opts.Path + "/{agent}"
	mux.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		if _, ep, ok := lookup(w, r); ok {
			ep.jsonrpc.ServeHTTP(w, r)
		}
	})
	mux.HandleFunc(base+"/v1/", func(w http.ResponseWriter, r *http.Request) {
		if agent, ep, ok := lookup(w, r); ok {
			http.StripPrefix(a.opts.Path+"/"+agent.Card().Name+"/v1", ep.rest).ServeHTTP(w, r)
		}
	})
	mux.HandleFunc("GET "+base+a2asrv.WellKnownAgentCardPath, func(w http.ResponseWriter, r *http.Request) {
		if agent, _, ok := lookup(w, r); ok {
			a2asrv.NewStaticAgentCardHandler(a.card(agent, r)).ServeHTTP(w, r)
		}
	})
	mux.HandleFunc("GET "+a2asrv.WellKnownAgentCardPath, func(w http.ResponseWriter, r *http.Request) {
		agent, ok := a.defaultAgent(agents)
		if !ok {
			http.Error(w, "no agents", http.StatusNotFound)
			return
		}
		a2asrv.NewStaticAgentCardHandler(a.card(agent, r)).ServeHTTP(w, r)
	})
	return nil
}

func (a *Adapter) defaultAgent(agents protocol.Registry) (rosetta.Agent, bool) {
	if a.opts.DefaultAgent != "" {
		return agents.Get(a.opts.DefaultAgent)
	}
	list := agents.List()
	if len(list) == 0 {
		return nil, false
	}
	return list[0], true
}
