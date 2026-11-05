package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/server"
)

func stdio(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("stdio", flag.ContinueOnError)
	fs.SetOutput(stderr)
	protocol := fs.String("protocol", "", "acp or mcp")
	configPath := fs.String("config", "", "path to rosetta.yaml")
	var agentNames names
	fs.Var(&agentNames, "agent", "agent to serve: picked from the config, or an echo agent without one (repeatable)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	p := server.StdioProtocol(*protocol)
	if p != server.ACP && p != server.MCP {
		return fmt.Errorf("%w: --protocol must be acp or mcp", errUsage)
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	opts := server.Options{Logger: logger}
	var agents []rosetta.Agent
	var err error
	if *configPath == "" {
		agents, err = echoAgents(agentNames)
	} else {
		agents, opts, err = stdioConfig(ctx, *configPath, agentNames, logger)
	}
	if err != nil {
		return err
	}
	logger.Info("serving over stdio", "protocol", *protocol)
	return server.ServeStdio(ctx, p, stdin, stdout, opts, agents...)
}

func stdioConfig(ctx context.Context, path string, pick names, logger *slog.Logger) ([]rosetta.Agent, server.Options, error) {
	cfg, err := loadConfig(path)
	if err != nil {
		return nil, server.Options{}, err
	}
	all, err := buildAgents(ctx, cfg)
	if err != nil {
		return nil, server.Options{}, err
	}
	opts := serverOptions(cfg, logger)
	if len(pick) == 0 {
		return all, opts, nil
	}
	byName := map[string]rosetta.Agent{}
	for _, a := range all {
		byName[a.Card().Name] = a
	}
	agents := make([]rosetta.Agent, 0, len(pick))
	for _, name := range pick {
		a, ok := byName[name]
		if !ok {
			return nil, server.Options{}, fmt.Errorf("%w: the config has no agent named %q", errUsage, name)
		}
		agents = append(agents, a)
	}
	return agents, opts, nil
}
