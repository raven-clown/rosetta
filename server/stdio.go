package server

import (
	"context"
	"fmt"
	"io"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/internal/run"
	"github.com/raven-clown/rosetta/protocol"
	"github.com/raven-clown/rosetta/protocol/acp"
	"github.com/raven-clown/rosetta/protocol/mcp"
	"github.com/raven-clown/rosetta/registry"
)

// StdioProtocol names a protocol that can be served over stdin and stdout.
type StdioProtocol string

// Protocols that ServeStdio supports.
const (
	ACP StdioProtocol = "acp"
	MCP StdioProtocol = "mcp"
)

// ServeStdio serves agents over in and out until ctx ends or the client
// leaves. ACP serves the first agent, MCP serves all of them.
func ServeStdio(ctx context.Context, p StdioProtocol, in io.Reader, out io.Writer, opts Options, agents ...rosetta.Agent) error {
	opts = opts.withDefaults()
	var adapter protocol.StdioAdapter
	switch p {
	case ACP:
		adapter = acp.New(acp.Options{Logger: opts.Logger})
	case MCP:
		adapter = mcp.New(mcp.Options{})
	default:
		return fmt.Errorf("server: unknown stdio protocol %q", p)
	}
	reg, err := registry.New(agents...)
	if err != nil {
		return err
	}
	return adapter.ServeStdio(ctx, reg, run.New(opts.Store, run.WithLogger(opts.Logger)), in, out)
}
