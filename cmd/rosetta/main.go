// Command rosetta serves agents over MCP, A2A, ACP and AG-UI.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/agents/echo"
	"github.com/raven-clown/rosetta/registry"
)

const usage = `usage:
  rosetta serve [--listen :8080] [--agent echo]...
  rosetta stdio --protocol acp|mcp [--agent echo]...
`

var errUsage = errors.New("usage error")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "serve":
		err = serve(ctx, args[1:], stdout, stderr)
	case "stdio":
		err = stdio(ctx, args[1:], stdin, stdout, stderr)
	default:
		err = fmt.Errorf("%w: unknown command %q", errUsage, args[0])
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintf(stderr, "%v\n%s", err, usage)
		return 2
	default:
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
}

type names []string

func (n *names) String() string { return strings.Join(*n, ",") }

func (n *names) Set(v string) error {
	*n = append(*n, v)
	return nil
}

func echoAgents(list names) ([]rosetta.Agent, error) {
	if len(list) == 0 {
		list = names{"echo"}
	}
	agents := make([]rosetta.Agent, 0, len(list))
	for _, name := range list {
		if !registry.ValidName(name) {
			return nil, fmt.Errorf("%w: agent name %q must be lowercase letters, digits, - or _", errUsage, name)
		}
		agents = append(agents, echo.New(name))
	}
	return agents, nil
}
