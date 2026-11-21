package config

import "testing"

// FuzzParse feeds Parse any document and one ROSETTA_ override. It must return a config with its defaults filled in, or an error.
func FuzzParse(f *testing.F) {
	f.Add("auth: {api_keys: [{name: k, key: v, role: admin}]}\ntransponder: {url: http://t}\nagents:\n  - {name: a, kind: llm, model: m1}\n", "ROSETTA_AGENTS__0__MODEL=m2")
	f.Add("auth: {api_keys: [{name: k, key: v, role: admin}]}\nagents: [{name: a, kind: echo}]\nprotocols: {mcp: {path: /tools}}\n", "ROSETTA_SESSIONS__TTL=90m")
	f.Add("listen_addr: ${ADDR}\nagents: [{name: a, kind: a2a, url: http://x}]\n", "ADDR=:7000")
	f.Add("agents:\n  - &a {name: a, kind: echo}\n  - *a\n", "ROSETTA_AGENTS__9__NAME=x")
	f.Add("- 1\n- 2\n", "ROSETTA_AUTH__DISABLED=true")
	f.Fuzz(func(t *testing.T, doc, env string) {
		cfg, err := Parse([]byte(doc), []string{env})
		if err != nil {
			return
		}
		if cfg == nil || cfg.ListenAddr == "" || cfg.Sessions.TTL <= 0 {
			t.Fatalf("Parse(%q, %q) = %+v without defaults", doc, env, cfg)
		}
	})
}
