package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/server"
)

func serve(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "", "address to listen on (default listen_addr from the config, or :8080)")
	configPath := fs.String("config", "", "path to rosetta.yaml")
	var agentNames names
	fs.Var(&agentNames, "agent", "name of an echo agent to serve without a config (repeatable)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	logger := slog.New(slog.NewJSONHandler(stdout, nil))
	agents, opts, addr, err := serveSetup(ctx, *configPath, agentNames, logger)
	if err != nil {
		return err
	}
	if *listen != "" {
		addr = *listen
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           server.New(opts, agents...).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.Info("listening", "addr", ln.Addr().String())

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	logger.Info("stopped")
	return nil
}

func serveSetup(ctx context.Context, path string, agentNames names, logger *slog.Logger) ([]rosetta.Agent, server.Options, string, error) {
	if path == "" {
		agents, err := echoAgents(agentNames)
		if err != nil {
			return nil, server.Options{}, "", err
		}
		logger.Warn("auth is off because no --config was given; anyone who can reach this address can run its agents")
		return agents, server.Options{Logger: logger}, ":8080", nil
	}
	if len(agentNames) > 0 {
		return nil, server.Options{}, "", fmt.Errorf("%w: --agent only applies without --config", errUsage)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return nil, server.Options{}, "", err
	}
	agents, err := buildAgents(ctx, cfg)
	if err != nil {
		return nil, server.Options{}, "", err
	}
	if cfg.Auth.Disabled {
		logger.Warn("auth is off because auth.disabled is set")
	}
	return agents, serverOptions(cfg, logger), cfg.ListenAddr, nil
}
