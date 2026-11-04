package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/raven-clown/rosetta/server"
)

func stdio(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("stdio", flag.ContinueOnError)
	fs.SetOutput(stderr)
	protocol := fs.String("protocol", "", "acp or mcp")
	var agentNames names
	fs.Var(&agentNames, "agent", "name of an echo agent to serve (repeatable)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	p := server.StdioProtocol(*protocol)
	if p != server.ACP && p != server.MCP {
		return fmt.Errorf("%w: --protocol must be acp or mcp", errUsage)
	}
	agents, err := echoAgents(agentNames)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	logger.Info("serving over stdio", "protocol", *protocol)
	return server.ServeStdio(ctx, p, stdin, stdout, server.Options{Logger: logger}, agents...)
}
