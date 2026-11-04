// Package acp serves one agent over ACP on a reader and a writer, the way
// editors start agents as child processes.
package acp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"sync"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/protocol"
)

// Options configures the adapter.
type Options struct {
	Logger *slog.Logger
}

// Adapter serves ACP.
type Adapter struct {
	logger *slog.Logger
}

// New returns an ACP adapter.
func New(opts Options) *Adapter {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	return &Adapter{logger: opts.Logger}
}

// Name returns "acp".
func (a *Adapter) Name() string { return "acp" }

// ServeStdio serves the first agent in the registry until ctx ends or the
// client closes the connection.
func (a *Adapter) ServeStdio(ctx context.Context, agents protocol.Registry, run protocol.Runner, in io.Reader, out io.Writer) error {
	list := agents.List()
	if len(list) == 0 {
		return errors.New("acp: no agent to serve")
	}
	impl := newAgent(list[0], run)
	conn := acpsdk.NewAgentSideConnection(impl, out, in)
	conn.SetLogger(a.logger)
	impl.connect(conn)
	select {
	case <-ctx.Done():
	case <-conn.Done():
	}
	return nil
}

type session struct {
	cancel context.CancelFunc
}

type agent struct {
	agent    rosetta.Agent
	run      protocol.Runner
	conn     updater
	ready    chan struct{}
	mu       sync.Mutex
	sessions map[acpsdk.SessionId]*session
}

func (x *agent) Initialize(context.Context, acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	card := x.agent.Card()
	version := card.Version
	if version == "" {
		version = rosetta.Version()
	}
	return acpsdk.InitializeResponse{
		ProtocolVersion:   acpsdk.ProtocolVersionNumber,
		AgentCapabilities: acpsdk.AgentCapabilities{},
		AgentInfo:         &acpsdk.Implementation{Name: card.Name, Version: version},
		AuthMethods:       []acpsdk.AuthMethod{},
	}, nil
}

func (x *agent) NewSession(context.Context, acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := acpsdk.SessionId(hex.EncodeToString(b))
	x.mu.Lock()
	x.sessions[id] = &session{}
	x.mu.Unlock()
	return acpsdk.NewSessionResponse{SessionId: id}, nil
}

func (x *agent) CloseSession(_ context.Context, p acpsdk.CloseSessionRequest) (acpsdk.CloseSessionResponse, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if s, ok := x.sessions[p.SessionId]; ok && s.cancel != nil {
		s.cancel()
	}
	delete(x.sessions, p.SessionId)
	return acpsdk.CloseSessionResponse{}, nil
}

func (x *agent) Cancel(_ context.Context, p acpsdk.CancelNotification) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if s, ok := x.sessions[p.SessionId]; ok && s.cancel != nil {
		s.cancel()
	}
	return nil
}

func (x *agent) Prompt(ctx context.Context, p acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	x.mu.Lock()
	s, ok := x.sessions[p.SessionId]
	if ok {
		s.cancel = cancel
	}
	x.mu.Unlock()
	if !ok {
		return acpsdk.PromptResponse{}, acpsdk.NewInvalidParams(map[string]any{"sessionId": p.SessionId, "error": "unknown session"})
	}

	req := &rosetta.Request{SessionID: string(p.SessionId)}
	for _, block := range p.Prompt {
		if block.Text != nil && block.Text.Text != "" {
			req.Input = append(req.Input, rosetta.Part{Text: block.Text.Text})
		}
	}
	err := x.run.Run(runCtx, x.agent, req, func(e rosetta.Event) error {
		for _, u := range updates(e) {
			if err := x.update(runCtx, acpsdk.SessionNotification{SessionId: p.SessionId, Update: u}); err != nil {
				return err
			}
		}
		return nil
	})
	switch {
	case err == nil:
		return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn}, nil
	case errors.Is(err, context.Canceled):
		return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonCancelled}, nil
	default:
		return acpsdk.PromptResponse{}, &acpsdk.RequestError{Code: -32603, Message: err.Error()}
	}
}

func (x *agent) Authenticate(context.Context, acpsdk.AuthenticateRequest) (acpsdk.AuthenticateResponse, error) {
	return acpsdk.AuthenticateResponse{}, acpsdk.NewMethodNotFound("authenticate")
}

func (x *agent) Logout(context.Context, acpsdk.LogoutRequest) (acpsdk.LogoutResponse, error) {
	return acpsdk.LogoutResponse{}, acpsdk.NewMethodNotFound("logout")
}

func (x *agent) ListSessions(context.Context, acpsdk.ListSessionsRequest) (acpsdk.ListSessionsResponse, error) {
	return acpsdk.ListSessionsResponse{}, acpsdk.NewMethodNotFound("session/list")
}

func (x *agent) ResumeSession(context.Context, acpsdk.ResumeSessionRequest) (acpsdk.ResumeSessionResponse, error) {
	return acpsdk.ResumeSessionResponse{}, acpsdk.NewMethodNotFound("session/resume")
}

func (x *agent) SetSessionConfigOption(context.Context, acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error) {
	return acpsdk.SetSessionConfigOptionResponse{}, acpsdk.NewMethodNotFound("session/set_config_option")
}

func (x *agent) SetSessionMode(context.Context, acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	return acpsdk.SetSessionModeResponse{}, acpsdk.NewMethodNotFound("session/set_mode")
}

type updater interface {
	SessionUpdate(ctx context.Context, n acpsdk.SessionNotification) error
}

func newAgent(a rosetta.Agent, run protocol.Runner) *agent {
	return &agent{agent: a, run: run, sessions: map[acpsdk.SessionId]*session{}, ready: make(chan struct{})}
}

// connect publishes the connection. The SDK starts reading before its
// constructor returns, so handlers wait on ready instead of reading conn
// directly.
func (x *agent) connect(u updater) {
	x.conn = u
	close(x.ready)
}

func (x *agent) update(ctx context.Context, n acpsdk.SessionNotification) error {
	select {
	case <-x.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	return x.conn.SessionUpdate(ctx, n)
}
