// Package protocol defines what a protocol adapter implements. Each
// protocol lives in its own subpackage.
package protocol

import (
	"context"
	"io"
	"net/http"

	"github.com/raven-clown/rosetta"
)

// Runner runs an agent and passes its framed events to sink.
type Runner interface {
	Run(ctx context.Context, agent rosetta.Agent, req *rosetta.Request, sink func(rosetta.Event) error) error
}

// Registry is the live list of agents.
type Registry interface {
	Get(name string) (rosetta.Agent, bool)
	List() []rosetta.Agent
	Changes() <-chan struct{}
}

// HTTPAdapter serves a protocol over HTTP.
type HTTPAdapter interface {
	Name() string
	Mount(mux *http.ServeMux, agents Registry, run Runner) error
}

// StdioAdapter serves a protocol over a reader and a writer.
type StdioAdapter interface {
	Name() string
	ServeStdio(ctx context.Context, agents Registry, run Runner, in io.Reader, out io.Writer) error
}
