package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/admin"
	"github.com/raven-clown/rosetta/agents/a2a"
	"github.com/raven-clown/rosetta/agents/echo"
	"github.com/raven-clown/rosetta/agents/llm"
	"github.com/raven-clown/rosetta/config"
	"github.com/raven-clown/rosetta/llm/openai"
	"github.com/raven-clown/rosetta/registry"
	"github.com/raven-clown/rosetta/server"
	"github.com/raven-clown/rosetta/store"
	"github.com/raven-clown/rosetta/tools/mcp"
)

const cardTimeout = 5 * time.Second

// loadConfig reads path, or takes the defaults and ROSETTA_* variables
// when there is no file.
func loadConfig(path string) (*config.Config, error) {
	if path == "" {
		return config.Parse(nil, os.Environ())
	}
	return config.Load(path, os.Environ())
}

type builder struct {
	cfg   *config.Config
	chat  *openai.Client
	tools llm.Tools
}

func newBuilder(cfg *config.Config) *builder {
	b := &builder{cfg: cfg, chat: &openai.Client{URL: cfg.Transponder.URL, APIKey: cfg.Transponder.APIKey}}
	if cfg.Airlock.URL != "" {
		b.tools = llm.MCPTools(&mcp.Source{URL: cfg.Airlock.URL, APIKey: cfg.Airlock.APIKey})
	}
	return b
}

func (b *builder) build(ctx context.Context, a config.Agent) (rosetta.Agent, error) {
	switch a.Kind {
	case "echo":
		return echo.New(a.Name), nil
	case "llm":
		return llm.New(llm.Config{
			Name: a.Name, Description: a.Description, Model: a.Model,
			SystemPrompt: a.SystemPrompt, Tools: a.Tools, MaxSteps: a.MaxSteps,
		}, b.chat, b.tools), nil
	case "a2a":
		cardCtx, cancel := context.WithTimeout(ctx, cardTimeout)
		defer cancel()
		return a2a.New(cardCtx, a2a.Config{Name: a.Name, Description: a.Description, URL: a.URL, ForwardToken: a.ForwardToken}), nil
	}
	return nil, fmt.Errorf("agent %s: unknown kind %q", a.Name, a.Kind)
}

func buildAgents(ctx context.Context, cfg *config.Config) ([]rosetta.Agent, error) {
	b := newBuilder(cfg)
	agents := make([]rosetta.Agent, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
		agent, err := b.build(ctx, a)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, nil
}

type builtAgent struct {
	version int64
	agent   rosetta.Agent
}

// live keeps the registry and key set in step with the store: fixed
// agents from code and config, plus agents and keys made through the
// admin API.
type live struct {
	store  *store.Store
	reg    *registry.Registry
	keys   *server.Keys
	fixed  []rosetta.Agent
	info   []admin.FixedAgent
	build  *builder
	logger *slog.Logger
	// keying serializes key reloads, and reloading agent reloads; agents
	// and skipped belong to reloading. Keys never wait behind an agent
	// build, which can wait seconds on an a2a card. mu guards only rev.
	keying    sync.Mutex
	reloading sync.Mutex
	agents    map[string]builtAgent
	skipped   map[string]int64
	mu        sync.Mutex
	rev       int64
}

func newLive(st *store.Store, cfg *config.Config, fixed []rosetta.Agent, info []admin.FixedAgent, keys *server.Keys, logger *slog.Logger) (*live, error) {
	reg, err := registry.New(fixed...)
	if err != nil {
		return nil, err
	}
	return &live{store: st, reg: reg, keys: keys, fixed: fixed, info: info, build: newBuilder(cfg), logger: logger,
		agents: map[string]builtAgent{}, skipped: map[string]int64{}}, nil
}

// reload reads stored keys and agents and swaps them in.
func (l *live) reload(ctx context.Context) error {
	if err := l.reloadKeys(ctx); err != nil {
		return err
	}
	l.reloading.Lock()
	defer l.reloading.Unlock()
	return l.reloadAgents(ctx)
}

func (l *live) reloadKeys(ctx context.Context) error {
	if l.keys == nil {
		return nil
	}
	l.keying.Lock()
	defer l.keying.Unlock()
	keys, err := l.store.ListKeys(ctx)
	if err != nil {
		return err
	}
	l.keys.SetStored(keys)
	return nil
}

// reloadAgents runs with reloading held. A stored agent that fails to
// build, or that has the name of a fixed one, is logged and left out, so
// one bad row cannot stop the server.
func (l *live) reloadAgents(ctx context.Context) error {
	rev, err := l.store.Revision(ctx)
	if err != nil {
		return err
	}
	stored, err := l.store.ListAgents(ctx)
	if err != nil {
		return err
	}
	fixed := map[string]bool{}
	for _, a := range l.fixed {
		fixed[a.Card().Name] = true
	}
	agents := append([]rosetta.Agent{}, l.fixed...)
	built := map[string]builtAgent{}
	for _, s := range stored {
		if fixed[s.Name] {
			if l.skipped[s.Name] != s.Version {
				l.logger.Warn("stored agent has the name of a config or code agent, so it is not served", "agent", s.Name)
				l.skipped[s.Name] = s.Version
			}
			continue
		}
		if b, ok := l.agents[s.Name]; ok && b.version == s.Version {
			built[s.Name] = b
			agents = append(agents, b.agent)
			continue
		}
		var spec config.Agent
		err := json.Unmarshal(s.Spec, &spec)
		var agent rosetta.Agent
		if err == nil {
			spec.Name = s.Name
			agent, err = l.build.build(ctx, spec)
		}
		if err != nil {
			if l.skipped[s.Name] != s.Version {
				l.logger.Error("stored agent cannot be built, so it is not served", "agent", s.Name, "err", err)
				l.skipped[s.Name] = s.Version
			}
			continue
		}
		built[s.Name] = builtAgent{s.Version, agent}
		agents = append(agents, agent)
	}
	if err := l.reg.Set(agents...); err != nil {
		return err
	}
	l.agents = built
	l.mu.Lock()
	l.rev = rev
	l.mu.Unlock()
	return nil
}

// reloadNow is the admin API's hook after a write.
func (l *live) reloadNow(ctx context.Context) {
	if err := l.reload(ctx); err != nil {
		l.logger.Error("reloading agents and keys", "err", err)
	}
}

// watch reloads when the revision moves, so a change made by another
// process shows up here within one interval. Keys reload on every move.
// Agents reload in the background, and a move seen while one is running
// is picked up on a later tick, since rev only catches up when it ends.
func (l *live) watch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rev, err := l.store.Revision(ctx)
		if err != nil {
			l.logger.Error("checking for changes", "err", err)
			continue
		}
		l.mu.Lock()
		changed := rev != l.rev
		l.mu.Unlock()
		if !changed {
			continue
		}
		if err := l.reloadKeys(ctx); err != nil {
			l.logger.Error("reloading keys", "err", err)
		}
		if l.reloading.TryLock() {
			go func() {
				defer l.reloading.Unlock()
				if err := l.reloadAgents(ctx); err != nil && ctx.Err() == nil {
					l.logger.Error("reloading agents", "err", err)
				}
			}()
		}
	}
}

func prune(ctx context.Context, st *store.Store, retention, every time.Duration, logger *slog.Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		n, err := st.Prune(ctx, time.Now(), retention)
		switch {
		case err != nil && ctx.Err() == nil:
			logger.Error("pruning old sessions and runs", "err", err)
		case n > 0:
			logger.Info("pruned old sessions and runs", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
