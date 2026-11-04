// Package registry holds the live list of agents rosetta serves.
package registry

import (
	"fmt"
	"regexp"

	"github.com/raven-clown/rosetta"
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidName reports whether name can be used as an agent name. Names become
// MCP tool names and URL path segments.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Registry is a set of agents looked up by name.
type Registry struct {
	order   []rosetta.Agent
	byName  map[string]rosetta.Agent
	changes chan struct{}
}

// New checks the agents' names and returns a registry holding them.
func New(agents ...rosetta.Agent) (*Registry, error) {
	r := &Registry{byName: map[string]rosetta.Agent{}, changes: make(chan struct{})}
	for _, a := range agents {
		name := a.Card().Name
		if !ValidName(name) {
			return nil, fmt.Errorf("registry: agent name %q must match %s", name, namePattern)
		}
		if _, dup := r.byName[name]; dup {
			return nil, fmt.Errorf("registry: two agents named %q", name)
		}
		r.byName[name] = a
		r.order = append(r.order, a)
	}
	return r, nil
}

// Get returns the agent with the given name.
func (r *Registry) Get(name string) (rosetta.Agent, bool) {
	a, ok := r.byName[name]
	return a, ok
}

// List returns the agents in the order they were registered.
func (r *Registry) List() []rosetta.Agent { return append([]rosetta.Agent{}, r.order...) }

// Changes is signalled when the agent list changes.
func (r *Registry) Changes() <-chan struct{} { return r.changes }
