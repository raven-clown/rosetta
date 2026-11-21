// Package config loads rosetta's YAML config, expands ${VAR} references and
// applies ROSETTA_* environment overrides.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/raven-clown/rosetta/registry"
)

// Config is the whole file.
type Config struct {
	ListenAddr   string    `yaml:"listen_addr"`
	DefaultAgent string    `yaml:"default_agent"`
	Storage      Storage   `yaml:"storage"`
	Auth         Auth      `yaml:"auth"`
	Transponder  Upstream  `yaml:"transponder"`
	Airlock      Upstream  `yaml:"airlock"`
	Protocols    Protocols `yaml:"protocols"`
	Sessions     Sessions  `yaml:"sessions"`
	Runs         Runs      `yaml:"runs"`
	Agents       []Agent   `yaml:"agents"`
}

// Runs configures run records.
type Runs struct {
	Retention time.Duration `yaml:"retention"`
}

// DefaultDSN is where state goes with no storage.dsn: a file in the
// working directory, as the contract says.
const DefaultDSN = "sqlite://rosetta.db"

// Storage is where state is kept.
type Storage struct {
	DSN       string `yaml:"dsn"`
	SecretKey string `yaml:"secret_key"`
}

// Auth is inbound HTTP auth.
type Auth struct {
	Disabled bool     `yaml:"disabled"`
	APIKeys  []APIKey `yaml:"api_keys"`
}

// APIKey is one static key and its role.
type APIKey struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key"`
	Role string `yaml:"role"`
}

// Upstream is transponder or airlock.
type Upstream struct {
	URL    string `yaml:"url"`
	APIKey string `yaml:"api_key"`
}

// Protocols switches HTTP protocols on and off.
type Protocols struct {
	MCP  Protocol `yaml:"mcp"`
	A2A  Protocol `yaml:"a2a"`
	AGUI Protocol `yaml:"agui"`
}

// Protocol is one HTTP protocol. Enabled is never nil after loading.
type Protocol struct {
	Enabled *bool  `yaml:"enabled"`
	Path    string `yaml:"path"`
}

// Sessions configures conversation history.
type Sessions struct {
	TTL time.Duration `yaml:"ttl"`
}

// Agent is one agent declared in config or through the admin API, which
// takes the same fields as JSON.
type Agent struct {
	Name         string   `yaml:"name" json:"name"`
	Description  string   `yaml:"description" json:"description,omitempty"`
	Kind         string   `yaml:"kind" json:"kind"`
	Model        string   `yaml:"model" json:"model,omitempty"`
	SystemPrompt string   `yaml:"system_prompt" json:"system_prompt,omitempty"`
	Tools        []string `yaml:"tools" json:"tools,omitempty"`
	MaxSteps     int      `yaml:"max_steps" json:"max_steps,omitempty"`
	URL          string   `yaml:"url" json:"url,omitempty"`
	ForwardToken bool     `yaml:"forward_token" json:"forward_token,omitempty"`
}

// FieldError is a validation error that names the field it is about.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

func fieldErr(field, format string, args ...any) error {
	return &FieldError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// Load reads the file at path. environ is in os.Environ form.
func Load(path string, environ []string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path is the operator's --config flag
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return Parse(data, environ)
}

// Parse loads config from YAML bytes, which may be empty.
func Parse(data []byte, environ []string) (*Config, error) {
	tree := map[string]any{}
	if err := yaml.Unmarshal([]byte(expand(string(data), environ)), &tree); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if tree == nil {
		tree = map[string]any{}
	}
	override(tree, environ)
	merged, err := yaml.Marshal(tree)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(merged))
	dec.KnownFields(true)
	cfg := &Config{}
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg.defaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

func (c *Config) defaults() {
	if c.ListenAddr == "" {
		c.ListenAddr = ":8080"
	}
	if c.Storage.DSN == "" {
		c.Storage.DSN = DefaultDSN
	}
	if c.Runs.Retention == 0 {
		c.Runs.Retention = 30 * 24 * time.Hour
	}
	if c.Sessions.TTL == 0 {
		c.Sessions.TTL = 24 * time.Hour
	}
	for _, p := range []*Protocol{&c.Protocols.MCP, &c.Protocols.A2A, &c.Protocols.AGUI} {
		if p.Enabled == nil {
			on := true
			p.Enabled = &on
		}
	}
}

var roles = map[string]bool{"admin": true, "member": true, "viewer": true}

var dsnSchemes = []string{"sqlite://", "postgres://", "postgresql://"}

func (c *Config) validate() error {
	if !slices.ContainsFunc(dsnSchemes, func(s string) bool { return strings.HasPrefix(c.Storage.DSN, s) }) {
		return errors.New("storage.dsn: must start with sqlite://, postgres:// or postgresql://")
	}
	if c.Runs.Retention < 0 {
		return errors.New("runs.retention: must be positive")
	}
	seenKeys, seenNames := map[string]bool{}, map[string]bool{}
	for i, k := range c.Auth.APIKeys {
		field := fmt.Sprintf("auth.api_keys[%d]", i)
		switch {
		case k.Name == "":
			return fmt.Errorf("%s.name: is required", field)
		case seenNames[k.Name]:
			return fmt.Errorf("%s.name: %q is used by an earlier key", field, k.Name)
		case k.Key == "":
			return fmt.Errorf("%s.key: is empty", field)
		case seenKeys[k.Key]:
			return fmt.Errorf("%s.key: is the same as an earlier key", field)
		case !roles[k.Role]:
			return fmt.Errorf("%s.role: must be admin, member or viewer, not %q", field, k.Role)
		}
		seenKeys[k.Key], seenNames[k.Name] = true, true
	}
	for name, p := range map[string]Protocol{"mcp": c.Protocols.MCP, "a2a": c.Protocols.A2A, "agui": c.Protocols.AGUI} {
		if p.Path != "" && !strings.HasPrefix(p.Path, "/") {
			return fmt.Errorf("protocols.%s.path: must start with /", name)
		}
		if p.Path == "/admin" || strings.HasPrefix(p.Path, "/admin/") {
			return fmt.Errorf("protocols.%s.path: must not be under /admin, which the admin API owns", name)
		}
	}
	if c.Sessions.TTL < 0 {
		return errors.New("sessions.ttl: must be positive")
	}
	names := map[string]bool{}
	for i, a := range c.Agents {
		if err := c.validateAgent(i, a, names); err != nil {
			return err
		}
		names[a.Name] = true
	}
	if c.DefaultAgent != "" && !names[c.DefaultAgent] {
		return fmt.Errorf("default_agent: no agent is named %q", c.DefaultAgent)
	}
	return nil
}

func (c *Config) validateAgent(i int, a Agent, names map[string]bool) error {
	prefix := fmt.Sprintf("agents[%d].", i)
	if registry.ValidName(a.Name) && names[a.Name] {
		return fieldErr(prefix+"name", "%q is used by an earlier agent", a.Name)
	}
	return c.checkAgent(prefix, a)
}

// ValidateAgent checks one agent against this config, as the admin API
// does before storing it. A *FieldError names the field without a prefix.
func (c *Config) ValidateAgent(a Agent) error { return c.checkAgent("", a) }

func (c *Config) checkAgent(prefix string, a Agent) error {
	if !registry.ValidName(a.Name) {
		return fieldErr(prefix+"name", "%q must be lowercase letters, digits, - or _, up to 64 characters", a.Name)
	}
	switch a.Kind {
	case "echo":
	case "llm":
		switch {
		case a.Model == "":
			return fieldErr(prefix+"model", "is required for kind llm")
		case a.MaxSteps < 0:
			return fieldErr(prefix+"max_steps", "must not be negative")
		case c.Transponder.URL == "":
			return fieldErr("transponder.url", "is required by agent %s of kind llm", a.Name)
		}
	case "a2a":
		if a.URL == "" {
			return fieldErr(prefix+"url", "is required for kind a2a")
		}
		if u, err := url.Parse(a.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fieldErr(prefix+"url", "must be an http:// or https:// URL")
		}
	default:
		return fieldErr(prefix+"kind", "must be llm, a2a or echo, not %q", a.Kind)
	}
	return nil
}
