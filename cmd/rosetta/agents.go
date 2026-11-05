package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/agents/a2a"
	"github.com/raven-clown/rosetta/agents/echo"
	"github.com/raven-clown/rosetta/agents/llm"
	"github.com/raven-clown/rosetta/config"
	"github.com/raven-clown/rosetta/llm/openai"
	"github.com/raven-clown/rosetta/server"
	"github.com/raven-clown/rosetta/session"
	"github.com/raven-clown/rosetta/tools/mcp"
)

const cardTimeout = 5 * time.Second

func loadConfig(path string) (*config.Config, error) {
	return config.Load(path, os.Environ())
}

func buildAgents(ctx context.Context, cfg *config.Config) ([]rosetta.Agent, error) {
	chat := &openai.Client{URL: cfg.Transponder.URL, APIKey: cfg.Transponder.APIKey}
	var tools llm.Tools
	if cfg.Airlock.URL != "" {
		tools = llm.MCPTools(&mcp.Source{URL: cfg.Airlock.URL, APIKey: cfg.Airlock.APIKey})
	}
	agents := make([]rosetta.Agent, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
		switch a.Kind {
		case "echo":
			agents = append(agents, echo.New(a.Name))
		case "llm":
			agents = append(agents, llm.New(llm.Config{
				Name: a.Name, Description: a.Description, Model: a.Model,
				SystemPrompt: a.SystemPrompt, Tools: a.Tools, MaxSteps: a.MaxSteps,
			}, chat, tools))
		case "a2a":
			cardCtx, cancel := context.WithTimeout(ctx, cardTimeout)
			agents = append(agents, a2a.New(cardCtx, a2a.Config{
				Name: a.Name, Description: a.Description, URL: a.URL, ForwardToken: a.ForwardToken,
			}))
			cancel()
		default:
			return nil, fmt.Errorf("agent %s: unknown kind %q", a.Name, a.Kind)
		}
	}
	return agents, nil
}

func serverOptions(cfg *config.Config, logger *slog.Logger) server.Options {
	toggle := func(p config.Protocol) server.Toggle {
		return server.Toggle{Disabled: p.Enabled != nil && !*p.Enabled, Path: p.Path}
	}
	opts := server.Options{
		Logger:       logger,
		Store:        session.NewMemory(cfg.Sessions.TTL, nil),
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
