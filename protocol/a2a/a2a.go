// Package a2a serves agents over A2A, with the JSON-RPC and REST bindings
// and an agent card per agent.
package a2a

import (
	"net/http"
	"reflect"
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

// endpoint is one agent's A2A handler. The handler holds that agent's
// tasks in memory, so it lives as long as the agent value does.
type endpoint struct {
	agent   rosetta.Agent
	jsonrpc http.Handler
	rest    http.Handler
}

// same reports whether a and b are the same agent value. Values of a type
// that cannot be compared count as different, so they get a new handler.
func same(a, b rosetta.Agent) bool {
	ta := reflect.TypeOf(a)
	if ta != reflect.TypeOf(b) || ta == nil || !ta.Comparable() {
		return false
	}
	return a == b
}

// Mount registers the A2A routes for every agent on mux.
func (a *Adapter) Mount(mux *http.ServeMux, agents protocol.Registry, run protocol.Runner) error {
	var mu sync.Mutex
	endpoints := map[string]*endpoint{}
	changes := agents.Changes()
	go func() {
		for range changes {
			mu.Lock()
			for name, ep := range endpoints {
				if cur, ok := agents.Get(name); !ok || !same(cur, ep.agent) {
					delete(endpoints, name)
				}
			}
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
		name := agent.Card().Name
		ep, ok := endpoints[name]
		if !ok || !same(ep.agent, agent) {
			handler := a2asrv.NewHandler(newExecutor(agent, run))
			ep = &endpoint{agent: agent, jsonrpc: a2asrv.NewJSONRPCHandler(handler), rest: a2asrv.NewRESTHandler(handler)}
			endpoints[name] = ep
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
