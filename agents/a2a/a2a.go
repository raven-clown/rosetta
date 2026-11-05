// Package a2a is the built-in agent kind that wraps a remote A2A agent, so
// it can be reached over every protocol rosetta serves.
package a2a

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"sync"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/internal/httpx"
)

// Config describes one remote agent. URL is the agent's base URL, where
// its card lives under /.well-known/agent-card.json. The caller's token is
// sent to the remote only when ForwardToken is set.
type Config struct {
	Name         string
	Description  string
	URL          string
	ForwardToken bool
	HTTP         *http.Client
}

// New returns the agent and tries to read the remote card once. When the
// remote is not reachable yet, the first run tries again.
func New(ctx context.Context, cfg Config) rosetta.Agent {
	a := &agent{cfg: cfg}
	_, _ = a.connect(ctx)
	return a
}

type agent struct {
	cfg    Config
	mu     sync.Mutex
	card   *a2a.AgentCard
	client *a2aclient.Client
}

func (a *agent) cardURL() string {
	u := strings.TrimSuffix(a.cfg.URL, "/")
	if strings.HasSuffix(u, ".json") {
		return u
	}
	return u + "/.well-known/agent-card.json"
}

func (a *agent) connect(ctx context.Context) (*a2aclient.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil {
		return a.client, nil
	}
	resolver := agentcard.DefaultResolver
	var opts []a2aclient.FactoryOption
	if a.cfg.HTTP != nil {
		resolver = agentcard.NewResolver(a.cfg.HTTP)
		opts = append(opts, a2aclient.WithJSONRPCTransport(a.cfg.HTTP), a2aclient.WithRESTTransport(a.cfg.HTTP))
	}
	card, err := resolver.Resolve(ctx, a.cardURL())
	if err != nil {
		return nil, fmt.Errorf("a2a: reading the card of %s: %w", a.cfg.Name, err)
	}
	client, err := a2aclient.NewFromCard(ctx, card, opts...)
	if err != nil {
		return nil, fmt.Errorf("a2a: connecting to %s: %w", a.cfg.Name, err)
	}
	a.card, a.client = card, client
	return client, nil
}

func (a *agent) Card() rosetta.Card {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := rosetta.Card{Name: a.cfg.Name, Description: a.cfg.Description}
	if a.card == nil {
		return c
	}
	if c.Description == "" {
		c.Description = a.card.Description
	}
	c.Version = a.card.Version
	for _, s := range a.card.Skills {
		c.Skills = append(c.Skills, rosetta.Skill{ID: s.ID, Name: s.Name, Description: s.Description, Tags: s.Tags, Examples: s.Examples})
	}
	return c
}

func (a *agent) Run(ctx context.Context, req *rosetta.Request, out rosetta.Stream) error {
	client, err := a.connect(ctx)
	if err != nil {
		return err
	}
	h := httpx.Outbound(req.Caller, "")
	if !a.cfg.ForwardToken {
		h.Del("Authorization")
	}
	ctx = a2aclient.AttachServiceParams(ctx, a2aclient.ServiceParams(h))

	msg := a2a.NewMessage(a2a.MessageRoleUser, parts(req.Input)...)
	msg.ContextID = contextID(req)
	send := &a2a.SendMessageRequest{Message: msg}

	m := &mapper{out: out}
	if client.Card().Capabilities.Streaming {
		return m.stream(client.SendStreamingMessage(ctx, send))
	}
	res, err := client.SendMessage(ctx, send)
	if err != nil {
		return fmt.Errorf("a2a: %w", err)
	}
	return m.event(res)
}

// contextID keeps sessions of different callers apart on the remote. Both
// forms are prefixed, so an anonymous session ID can never take the shape
// of a caller's.
func contextID(req *rosetta.Request) string {
	switch {
	case req.SessionID == "":
		return ""
	case req.Caller.Token == "":
		return "anon-" + req.SessionID
	}
	sum := sha256.Sum256([]byte(req.Caller.Token))
	return "caller-" + hex.EncodeToString(sum[:8]) + "-" + req.SessionID
}

func parts(in []rosetta.Part) []*a2a.Part {
	var ps []*a2a.Part
	for _, p := range in {
		switch {
		case p.Text != "":
			ps = append(ps, a2a.NewTextPart(p.Text))
		case len(p.Data) > 0:
			var v any
			if json.Unmarshal(p.Data, &v) == nil {
				ps = append(ps, a2a.NewDataPart(v))
			}
		}
	}
	return ps
}

const responseArtifact = "response"

type mapper struct {
	out rosetta.Stream
}

func (m *mapper) stream(events iter.Seq2[a2a.Event, error]) error {
	for ev, err := range events {
		if err != nil {
			return fmt.Errorf("a2a: %w", err)
		}
		if err := m.event(ev); err != nil {
			return err
		}
	}
	return nil
}

func (m *mapper) event(ev a2a.Event) error {
	switch ev := ev.(type) {
	case *a2a.Message:
		return m.artifact(&a2a.Artifact{Name: responseArtifact, Parts: ev.Parts})
	case *a2a.Task:
		for _, art := range ev.Artifacts {
			if err := m.artifact(art); err != nil {
				return err
			}
		}
		return m.status(ev.Status)
	case *a2a.TaskArtifactUpdateEvent:
		return m.artifact(ev.Artifact)
	case *a2a.TaskStatusUpdateEvent:
		return m.status(ev.Status)
	}
	return nil
}

func (m *mapper) artifact(art *a2a.Artifact) error {
	if art == nil {
		return nil
	}
	if art.Name != "" && art.Name != responseArtifact {
		return m.out.Send(rosetta.Artifact{Name: art.Name, Parts: rosettaParts(art.Parts)})
	}
	for _, p := range art.Parts {
		if t := p.Text(); t != "" {
			if err := m.out.Text(t); err != nil {
				return err
			}
			continue
		}
		if ps := rosettaParts([]*a2a.Part{p}); len(ps) > 0 {
			if err := m.out.Send(rosetta.Artifact{Name: responseArtifact, Parts: ps}); err != nil {
				return err
			}
		}
	}
	return nil
}

func rosettaParts(in []*a2a.Part) []rosetta.Part {
	var ps []rosetta.Part
	for _, p := range in {
		if t := p.Text(); t != "" {
			ps = append(ps, rosetta.Part{Text: t})
			continue
		}
		if d := p.Data(); d != nil {
			if b, err := json.Marshal(d); err == nil {
				ps = append(ps, rosetta.Part{Data: b})
			}
		}
	}
	return ps
}

func (m *mapper) status(s a2a.TaskStatus) error {
	switch s.State {
	case a2a.TaskStateWorking:
		return m.working(s.Message)
	case a2a.TaskStateFailed, a2a.TaskStateRejected:
		if t := messageText(s.Message); t != "" {
			return errors.New(t)
		}
		return errors.New("a2a: the remote agent failed")
	case a2a.TaskStateCanceled:
		return errors.New("a2a: the remote task was cancelled")
	case a2a.TaskStateInputRequired, a2a.TaskStateAuthRequired:
		return errors.New("a2a: the remote agent asked for input, which rosetta does not support yet")
	}
	return nil
}

func messageText(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}
	var texts []string
	for _, p := range msg.Parts {
		if t := p.Text(); t != "" {
			texts = append(texts, t)
		}
	}
	return strings.Join(texts, "\n")
}

type workingData struct {
	ToolCall *struct {
		ID   string          `json:"id"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"toolCall"`
	ToolResult *struct {
		ID      string          `json:"id"`
		Result  json.RawMessage `json:"result"`
		IsError bool            `json:"isError"`
	} `json:"toolResult"`
	Step *struct {
		Name     string `json:"name"`
		Finished bool   `json:"finished"`
	} `json:"step"`
}

// working maps status messages. Text becomes a step, and the data shapes a
// rosetta remote sends become tool calls, tool results and steps again.
func (m *mapper) working(msg *a2a.Message) error {
	if msg == nil {
		return nil
	}
	for _, p := range msg.Parts {
		var err error
		if t := p.Text(); t != "" {
			if err = m.out.Send(rosetta.Step{Name: t}); err == nil {
				err = m.out.Send(rosetta.Step{Name: t, Finished: true})
			}
		} else {
			err = m.workingData(p.Data())
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *mapper) workingData(v any) error {
	var d workingData
	if b, err := json.Marshal(v); err == nil {
		_ = json.Unmarshal(b, &d)
	}
	switch {
	case d.ToolCall != nil:
		return m.out.ToolCall(d.ToolCall.ID, d.ToolCall.Name, d.ToolCall.Args)
	case d.ToolResult != nil:
		return m.out.ToolResult(d.ToolResult.ID, d.ToolResult.Result, d.ToolResult.IsError)
	case d.Step != nil:
		return m.out.Send(rosetta.Step{Name: d.Step.Name, Finished: d.Step.Finished})
	}
	return nil
}
