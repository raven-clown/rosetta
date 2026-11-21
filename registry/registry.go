// Package registry holds the live list of agents rosetta serves.
package registry

import (
	"fmt"
	"regexp"
	"sync"

	"github.com/raven-clown/rosetta"
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidName reports whether name can be used as an agent name. Names become
// MCP tool names and URL path segments.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Registry is a set of agents looked up by name. Set replaces the set at
// runtime and tells every subscriber of Changes.
type Registry struct {
	mu     sync.RWMutex
	order  []rosetta.Agent
	byName map[string]rosetta.Agent
	subs   []chan struct{}
}

// New checks the agents' names and returns a registry holding them.
func New(agents ...rosetta.Agent) (*Registry, error) {
	order, byName, err := build(agents)
	if err != nil {
		return nil, err
	}
	return &Registry{order: order, byName: byName}, nil
}

func build(agents []rosetta.Agent) ([]rosetta.Agent, map[string]rosetta.Agent, error) {
	byName := map[string]rosetta.Agent{}
	for _, a := range agents {
		name := a.Card().Name
		if !ValidName(name) {
			return nil, nil, fmt.Errorf("registry: agent name %q must match %s", name, namePattern)
		}
		if _, dup := byName[name]; dup {
			return nil, nil, fmt.Errorf("registry: two agents named %q", name)
		}
		byName[name] = a
	}
	return append([]rosetta.Agent{}, agents...), byName, nil
}

// Set replaces the agents after the same checks as New. On an error the
// old list stays.
func (r *Registry) Set(agents ...rosetta.Agent) error {
	order, byName, err := build(agents)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.order, r.byName = order, byName
	subs := r.subs
	r.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return nil
}

// Get returns the agent with the given name.
func (r *Registry) Get(name string) (rosetta.Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.byName[name]
	return a, ok
}

// List returns the agents in the order they were registered.
func (r *Registry) List() []rosetta.Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]rosetta.Agent{}, r.order...)
}

// Changes returns a channel of its own that is signalled after each Set.
// Signals do not queue: a subscriber that is behind sees one, then reads
// the current list.
func (r *Registry) Changes() <-chan struct{} {
	ch := make(chan struct{}, 1)
	r.mu.Lock()
	r.subs = append(r.subs, ch)
	r.mu.Unlock()
	return ch
}
