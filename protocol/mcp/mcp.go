// Package mcp serves agents over MCP. Each agent is one tool named after it,
// over Streamable HTTP or stdio.
package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/server"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/protocol"
)

// Options configures the adapter.
type Options struct {
	Path    string
	Name    string
	Version string
}

// Adapter serves MCP.
type Adapter struct {
	opts Options
}

// New returns an MCP adapter. Path defaults to /mcp and Name to rosetta.
func New(opts Options) *Adapter {
	if opts.Path == "" {
		opts.Path = "/mcp"
	}
	if opts.Name == "" {
		opts.Name = "rosetta"
	}
	if opts.Version == "" {
		opts.Version = rosetta.Version()
	}
	return &Adapter{opts: opts}
}

// Name returns "mcp".
func (a *Adapter) Name() string { return "mcp" }

// Mount serves Streamable HTTP on the adapter's path.
func (a *Adapter) Mount(mux *http.ServeMux, agents protocol.Registry, run protocol.Runner) error {
	srv := a.newServer(context.Background(), agents, run)
	mux.Handle(a.opts.Path, server.NewStreamableHTTPServer(srv,
		server.WithEndpointPath(a.opts.Path),
		server.WithHTTPContextFunc(withCaller),
	))
	return nil
}

// ServeStdio serves every agent in the registry over in and out.
func (a *Adapter) ServeStdio(ctx context.Context, agents protocol.Registry, run protocol.Runner, in io.Reader, out io.Writer) error {
	srv := a.newServer(ctx, agents, run)
	if err := server.NewStdioServer(srv).Listen(ctx, in, out); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func (a *Adapter) newServer(ctx context.Context, agents protocol.Registry, run protocol.Runner) *server.MCPServer {
	srv := server.NewMCPServer(a.opts.Name, a.opts.Version, server.WithToolCapabilities(true))
	srv.SetTools(tools(agents, run)...)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-agents.Changes():
				if !ok {
					return
				}
				srv.SetTools(tools(agents, run)...)
			}
		}
	}()
	return srv
}

type callerKey struct{}

func withCaller(ctx context.Context, r *http.Request) context.Context {
	c := rosetta.Caller{
		Token:     strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
		RequestID: r.Header.Get("X-Request-ID"),
	}
	if c.Token == r.Header.Get("Authorization") {
		c.Token = ""
	}
	return context.WithValue(ctx, callerKey{}, c)
}
