package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/server"
	"github.com/raven-clown/rosetta/store"
)

func stdio(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("stdio", flag.ContinueOnError)
	fs.SetOutput(stderr)
	protocol := fs.String("protocol", "", "acp or mcp")
	configPath := fs.String("config", "", "path to rosetta.yaml")
	var agentNames names
	fs.Var(&agentNames, "agent", "agent to serve: picked from the config and the database, or an echo agent without a config (repeatable)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	p := server.StdioProtocol(*protocol)
	if p != server.ACP && p != server.MCP {
		return fmt.Errorf("%w: --protocol must be acp or mcp", errUsage)
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	if *configPath == "" {
		agents, err := echoAgents(agentNames)
		if err != nil {
			return err
		}
		logger.Info("serving over stdio", "protocol", *protocol)
		return server.ServeStdio(ctx, p, stdin, stdout, server.Options{Logger: logger}, agents...)
	}
	agents, opts, st, err := stdioConfig(ctx, *configPath, agentNames, logger)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	logger.Info("serving over stdio", "protocol", *protocol, "storage", st.Dialect())
	return server.ServeStdio(ctx, p, stdin, stdout, opts, agents...)
}

// stdioConfig serves the config's agents and the stored ones, with history
// and run records in the same database as rosetta serve.
func stdioConfig(ctx context.Context, path string, pick names, logger *slog.Logger) ([]rosetta.Agent, server.Options, *store.Store, error) {
	cfg, err := loadConfig(path)
	if err != nil {
		return nil, server.Options{}, nil, err
	}
	fixed, err := buildAgents(ctx, cfg)
	if err != nil {
		return nil, server.Options{}, nil, err
	}
	st, err := store.OpenWith(ctx, cfg.Storage.DSN, store.Options{SessionTTL: cfg.Sessions.TTL})
	if err != nil {
		return nil, server.Options{}, nil, err
	}
	l, err := newLive(st, cfg, fixed, nil, nil, logger)
	if err == nil {
		err = l.reload(ctx)
	}
	if err != nil {
		_ = st.Close()
		return nil, server.Options{}, nil, err
	}
	opts := serverOptions(cfg, logger)
	opts.APIKeys, opts.Checks = nil, nil
	opts.Store, opts.Runs = st, st
	all := l.reg.List()
	if len(pick) == 0 {
		return all, opts, st, nil
	}
	agents := make([]rosetta.Agent, 0, len(pick))
	for _, name := range pick {
		a, ok := l.reg.Get(name)
		if !ok {
			_ = st.Close()
			return nil, server.Options{}, nil, fmt.Errorf("%w: no agent named %q in the config or the database", errUsage, name)
		}
		agents = append(agents, a)
	}
	return agents, opts, st, nil
}
