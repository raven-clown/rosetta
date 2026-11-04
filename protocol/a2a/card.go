package a2a

import (
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/raven-clown/rosetta"
)

var modes = []string{"text/plain", "application/json"}

func (a *Adapter) card(agent rosetta.Agent, r *http.Request) *a2a.AgentCard {
	c := agent.Card()
	base := a.opts.BaseURL
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	url := base + a.opts.Path + "/" + c.Name
	version := c.Version
	if version == "" {
		version = rosetta.Version()
	}
	card := &a2a.AgentCard{
		Name:        c.Name,
		Description: c.Description,
		Version:     version,
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(url, a2a.TransportProtocolJSONRPC),
			a2a.NewAgentInterface(url+"/v1", a2a.TransportProtocolHTTPJSON),
		},
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
		DefaultInputModes:  modes,
		DefaultOutputModes: modes,
		Skills:             []a2a.AgentSkill{},
	}
	for _, s := range c.Skills {
		tags := s.Tags
		if tags == nil {
			tags = []string{}
		}
		card.Skills = append(card.Skills, a2a.AgentSkill{
			ID: s.ID, Name: s.Name, Description: s.Description, Tags: tags, Examples: s.Examples,
		})
	}
	return card
}
