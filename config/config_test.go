package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestLoadFull(t *testing.T) {
	cfg, err := Load("testdata/full.yaml", []string{"SECRET=s3cret", "CI_KEY=ci-key"})
	if err != nil {
		t.Fatal(err)
	}
	want := &Config{
		ListenAddr:   ":9090",
		DefaultAgent: "helper",
		Storage:      Storage{DSN: "sqlite:///var/lib/rosetta/rosetta.db", SecretKey: "s3cret"},
		Auth: Auth{APIKeys: []APIKey{
			{Name: "ci", Key: "ci-key", Role: "member"},
			{Name: "ops", Key: "ops-key", Role: "admin"},
		}},
		Transponder: Upstream{URL: "http://transponder:8080/v1", APIKey: "t-key"},
		Airlock:     Upstream{URL: "http://airlock:8080/mcp", APIKey: "a-key"},
		Protocols: Protocols{
			MCP:  Protocol{Enabled: ptr(true), Path: "/tools"},
			A2A:  Protocol{Enabled: ptr(false)},
			AGUI: Protocol{Enabled: ptr(true), Path: "/ui"},
		},
		Sessions: Sessions{TTL: 2 * time.Hour},
		Agents: []Agent{
			{Name: "helper", Description: "Answers questions.", Kind: "llm", Model: "claude-sonnet-5-5",
				SystemPrompt: "Costs are $5 a month.", Tools: []string{"github__*", "logbook__recall"}, MaxSteps: 4},
			{Name: "travel", Kind: "a2a", URL: "https://agents.example.com/travel", ForwardToken: true},
			{Name: "echo", Kind: "echo"},
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("config =\n%+v\nwant\n%+v", cfg, want)
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := Parse([]byte("auth: {disabled: true}\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":8080" || cfg.Sessions.TTL != 24*time.Hour {
		t.Errorf("config = %+v", cfg)
	}
	for _, p := range []Protocol{cfg.Protocols.MCP, cfg.Protocols.A2A, cfg.Protocols.AGUI} {
		if p.Enabled == nil || !*p.Enabled || p.Path != "" {
			t.Errorf("protocol = %+v", p)
		}
	}
}

func TestEnvOverride(t *testing.T) {
	base := "auth: {api_keys: [{name: k, key: v, role: admin}]}\ntransponder: {url: http://t}\nagents:\n  - {name: a, kind: llm, model: m1}\n"
	tests := map[string]struct {
		env   string
		check func(*Config) bool
	}{
		"top level":     {"ROSETTA_LISTEN_ADDR=:7000", func(c *Config) bool { return c.ListenAddr == ":7000" }},
		"nested":        {"ROSETTA_TRANSPONDER__URL=http://other/v1", func(c *Config) bool { return c.Transponder.URL == "http://other/v1" }},
		"bool":          {"ROSETTA_AUTH__DISABLED=true", func(c *Config) bool { return c.Auth.Disabled }},
		"list index":    {"ROSETTA_AGENTS__0__MODEL=m2", func(c *Config) bool { return c.Agents[0].Model == "m2" }},
		"int":           {"ROSETTA_AGENTS__0__MAX_STEPS=3", func(c *Config) bool { return c.Agents[0].MaxSteps == 3 }},
		"duration":      {"ROSETTA_SESSIONS__TTL=90m", func(c *Config) bool { return c.Sessions.TTL == 90*time.Minute }},
		"string as is":  {"ROSETTA_AGENTS__0__SYSTEM_PROMPT=a: b", func(c *Config) bool { return c.Agents[0].SystemPrompt == "a: b" }},
		"list value":    {"ROSETTA_AGENTS__0__TOOLS=[x, y]", func(c *Config) bool { return reflect.DeepEqual(c.Agents[0].Tools, []string{"x", "y"}) }},
		"new map":       {"ROSETTA_PROTOCOLS__MCP__ENABLED=false", func(c *Config) bool { return !*c.Protocols.MCP.Enabled }},
		"unknown key":   {"ROSETTA_CI_KEY=secret", func(c *Config) bool { return c.ListenAddr == ":8080" }},
		"missing index": {"ROSETTA_AGENTS__5__MODEL=m", func(c *Config) bool { return len(c.Agents) == 1 }},
		"other prefix":  {"PATH=/bin", func(c *Config) bool { return c.ListenAddr == ":8080" }},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, err := Parse([]byte(base), []string{tt.env})
			if err != nil {
				t.Fatal(err)
			}
			if !tt.check(cfg) {
				t.Errorf("override %s not applied: %+v", tt.env, cfg)
			}
		})
	}
}

func TestEnvWithoutFile(t *testing.T) {
	cfg, err := Parse(nil, []string{"ROSETTA_AUTH__DISABLED=true", "ROSETTA_LISTEN_ADDR=:1"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Auth.Disabled || cfg.ListenAddr != ":1" {
		t.Errorf("config = %+v", cfg)
	}
}

func TestExpand(t *testing.T) {
	tests := map[string]struct {
		in, want string
	}{
		"set":      {"key: ${K}", "key: v"},
		"unset":    {"key: ${NOPE}", "key: "},
		"dollar":   {"p: costs $5 and $HOME", "p: costs $5 and $HOME"},
		"unclosed": {"p: ${K", "p: ${K"},
		"two":      {"${K}-${K}", "v-v"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := expand(tt.in, []string{"K=v"}); got != tt.want {
				t.Errorf("expand(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	keys := "auth: {api_keys: [{name: k, key: v, role: admin}]}\n"
	tests := map[string]struct {
		yaml  string
		field string
	}{
		"no keys":            {"listen_addr: ':1'\n", "auth.api_keys"},
		"empty key":          {"auth: {api_keys: [{name: k, key: '', role: admin}]}\n", "auth.api_keys[0].key"},
		"no key name":        {"auth: {api_keys: [{key: v, role: admin}]}\n", "auth.api_keys[0].name"},
		"bad role":           {"auth: {api_keys: [{name: k, key: v, role: root}]}\n", "auth.api_keys[0].role"},
		"bad name":           {keys + "agents: [{name: Bad Name, kind: echo}]\n", "agents[0].name"},
		"duplicate name":     {keys + "agents: [{name: a, kind: echo}, {name: a, kind: echo}]\n", "agents[1].name"},
		"bad kind":           {keys + "agents: [{name: a, kind: robot}]\n", "agents[0].kind"},
		"llm no model":       {keys + "transponder: {url: http://t}\nagents: [{name: a, kind: llm}]\n", "agents[0].model"},
		"llm no upstream":    {keys + "agents: [{name: a, kind: llm, model: m}]\n", "transponder.url"},
		"a2a no url":         {keys + "agents: [{name: a, kind: a2a}]\n", "agents[0].url"},
		"unknown default":    {keys + "default_agent: nope\nagents: [{name: a, kind: echo}]\n", "default_agent"},
		"negative steps":     {keys + "transponder: {url: http://t}\nagents: [{name: a, kind: llm, model: m, max_steps: -1}]\n", "agents[0].max_steps"},
		"bad ttl":            {keys + "sessions: {ttl: -1h}\n", "sessions.ttl"},
		"duplicate key name": {"auth: {api_keys: [{name: k, key: v1, role: admin}, {name: k, key: v2, role: viewer}]}\n", "auth.api_keys[1].name"},
		"duplicate key":      {"auth: {api_keys: [{name: k, key: v, role: admin}, {name: k2, key: v, role: viewer}]}\n", "auth.api_keys[1].key"},
		"bad protocol path":  {keys + "protocols: {mcp: {path: tools}}\n", "protocols.mcp.path"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml), nil)
			if err == nil || !strings.Contains(err.Error(), tt.field+":") {
				t.Errorf("err = %v, want one naming %s", err, tt.field)
			}
		})
	}
}

func TestUnknownKey(t *testing.T) {
	_, err := Parse([]byte("auth: {disabled: true}\nlisten: ':1'\n"), nil)
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), nil); !os.IsNotExist(err) && (err == nil || !strings.Contains(err.Error(), "nope.yaml")) {
		t.Errorf("err = %v", err)
	}
}

func TestExampleConfig(t *testing.T) {
	cfg, err := Load("../examples/rosetta.yaml", []string{"ROSETTA_LOCAL_KEY=dev-key"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Agents) != 3 || cfg.Auth.APIKeys[0].Key != "dev-key" {
		t.Errorf("config = %+v", cfg)
	}
}
