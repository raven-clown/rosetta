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
	"github.com/raven-clown/rosetta/admin"
	"github.com/raven-clown/rosetta/config"
	"github.com/raven-clown/rosetta/server"
	"github.com/raven-clown/rosetta/store"
)

// Intervals of the background loops. Tests shorten them.
var (
	reloadEvery = 2 * time.Second
	pruneEvery  = time.Hour
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	setup, err := serveSetup(ctx, *configPath, agentNames, logger, stderr)
	if err != nil {
		return err
	}
	defer func() { _ = setup.store.Close() }()
	addr := setup.addr
	if *listen != "" {
		addr = *listen
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           server.New(setup.opts).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.Info("listening", "addr", ln.Addr().String(), "storage", setup.store.Dialect())

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := srv.Shutdown(shutdown); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	logger.Info("stopped")
	return nil
}

type setup struct {
	opts  server.Options
	addr  string
	store *store.Store
}

// serveSetup opens the store, makes the first key, builds the agents and
// starts the reload and prune loops, which stop with ctx. Without a config
// file the defaults apply and the echo agents are served as code agents.
func serveSetup(ctx context.Context, path string, agentNames names, logger *slog.Logger, stderr io.Writer) (*setup, error) {
	if path != "" && len(agentNames) > 0 {
		return nil, fmt.Errorf("%w: --agent only applies without --config", errUsage)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return nil, err
	}
	var fixed []rosetta.Agent
	var info []admin.FixedAgent
	if path == "" {
		if fixed, err = echoAgents(agentNames); err != nil {
			return nil, err
		}
		for _, a := range fixed {
			info = append(info, admin.FixedAgent{Spec: config.Agent{Name: a.Card().Name, Description: a.Card().Description, Kind: "echo"}, Source: admin.SourceCode})
		}
	} else {
		if fixed, err = buildAgents(ctx, cfg); err != nil {
			return nil, err
		}
		for _, a := range cfg.Agents {
			info = append(info, admin.FixedAgent{Spec: a, Source: admin.SourceConfig})
		}
	}

	st, err := store.OpenWith(ctx, cfg.Storage.DSN, store.Options{SessionTTL: cfg.Sessions.TTL})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*setup, error) {
		_ = st.Close()
		return nil, err
	}
	if err := bootstrap(ctx, st, cfg, stderr); err != nil {
		return fail(err)
	}
	opts := serverOptions(cfg, logger)
	opts.Store, opts.Runs = st, st
	opts.Checks = append(opts.Checks, server.Check{Name: "storage", Check: st.Ping})
	if cfg.Auth.Disabled {
		logger.Warn("auth is off because auth.disabled is set; the admin API answers only on this machine")
	} else {
		opts.Keys = server.NewKeys(opts.APIKeys)
	}
	opts.APIKeys = nil

	l, err := newLive(st, cfg, fixed, info, opts.Keys, logger)
	if err != nil {
		return fail(err)
	}
	if err := l.reload(ctx); err != nil {
		return fail(err)
	}
	opts.Registry = l.reg
	var fixedKeys []admin.FixedKey
	for _, k := range cfg.Auth.APIKeys {
		fixedKeys = append(fixedKeys, admin.FixedKey{Name: k.Name, Role: k.Role})
	}
	checks := opts.Checks
	opts.Admin = admin.New(admin.Options{
		Store:    st,
		Fixed:    func() []admin.FixedAgent { return info },
		Keys:     fixedKeys,
		Validate: cfg.ValidateAgent,
		Reload:   l.reloadNow,
		Status:   func(ctx context.Context) admin.Status { return status(ctx, cfg, checks, logger) },
		Version:  rosetta.Version(),
		Logger:   logger,
	})
	go l.watch(ctx, reloadEvery)
	go prune(ctx, st, cfg.Runs.Retention, pruneEvery, logger)
	return &setup{opts: opts, addr: cfg.ListenAddr, store: st}, nil
}

func status(ctx context.Context, cfg *config.Config, checks []server.Check, logger *slog.Logger) admin.Status {
	on := func(p config.Protocol) bool { return p.Enabled == nil || *p.Enabled }
	st := admin.Status{
		Protocols: map[string]bool{"mcp": on(cfg.Protocols.MCP), "a2a": on(cfg.Protocols.A2A), "agui": on(cfg.Protocols.AGUI)},
		Upstreams: map[string]string{},
	}
	for _, c := range checks {
		if c.Name == "storage" {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := c.Check(cctx)
		cancel()
		st.Upstreams[c.Name] = "ok"
		if err != nil {
			logger.Warn("admin status: upstream", "upstream", c.Name, "err", err)
			st.Upstreams[c.Name] = "unavailable"
		}
	}
	return st
}

func serverOptions(cfg *config.Config, logger *slog.Logger) server.Options {
	toggle := func(p config.Protocol) server.Toggle {
		return server.Toggle{Disabled: p.Enabled != nil && !*p.Enabled, Path: p.Path}
	}
	opts := server.Options{
		Logger:       logger,
		DefaultAgent: cfg.DefaultAgent,
		Protocols: server.Protocols{
			MCP:  toggle(cfg.Protocols.MCP),
			A2A:  toggle(cfg.Protocols.A2A),
			AGUI: toggle(cfg.Protocols.AGUI),
		},
	}
	if !cfg.Auth.Disabled {
		for _, k := range cfg.Auth.APIKeys {
			opts.APIKeys = append(opts.APIKeys, server.APIKey{Name: k.Name, Key: k.Key, Role: k.Role})
		}
	}
	if cfg.Transponder.URL != "" {
		opts.Checks = append(opts.Checks, server.HTTPCheck("transponder", cfg.Transponder.URL, nil))
	}
	if cfg.Airlock.URL != "" {
		opts.Checks = append(opts.Checks, server.HTTPCheck("airlock", cfg.Airlock.URL, nil))
	}
	return opts
}
