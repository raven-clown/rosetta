package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/raven-clown/rosetta/config"
)

// minKindWidth is 4 so a 3-letter kind has three spaces before the detail.
const minKindWidth = 4

// check loads a config file and prints one line per agent.
// config.Load only reads and validates the file, so this does not open
// the database or contact transponder, airlock or a remote A2A agent.
func check(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to rosetta.yaml")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if *configPath == "" {
		return fmt.Errorf("%w: --config is required", errUsage)
	}
	cfg, err := config.Load(*configPath, os.Environ())
	if err != nil {
		return err
	}
	writeCheck(stdout, *configPath, cfg.Agents)
	return nil
}

func writeCheck(w io.Writer, path string, agents []config.Agent) {
	nameW, kindW := 0, minKindWidth
	for _, a := range agents {
		nameW = max(nameW, len(a.Name))
		kindW = max(kindW, len(a.Kind))
	}
	_, _ = fmt.Fprintf(w, "ok  %s\n", path)
	for _, a := range agents {
		_, _ = fmt.Fprintln(w, agentLine(nameW, kindW, a))
	}
}

func agentLine(nameW, kindW int, a config.Agent) string {
	detail := agentDetail(a)
	if detail == "" {
		return fmt.Sprintf("agent  %-*s  %s", nameW, a.Name, a.Kind)
	}
	return fmt.Sprintf("agent  %-*s  %-*s  %s", nameW, a.Name, kindW, a.Kind, detail)
}

func agentDetail(a config.Agent) string {
	switch a.Kind {
	case "llm":
		fields := "model=" + a.Model
		if len(a.Tools) > 0 {
			fields += " tools=" + strings.Join(a.Tools, ",")
		}
		return fields
	case "a2a":
		return "url=" + a.URL
	default:
		return ""
	}
}
