// Package run runs agents for the protocol adapters: it frames events,
// keeps session history, recovers panics and handles cancellation.
package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/session"
)

// Runner implements protocol.Runner.
type Runner struct {
	store  session.Store
	logger *slog.Logger
	ids    func() string
}

// Option configures a Runner.
type Option func(*Runner)

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(r *Runner) { r.logger = l } }

// WithIDs sets how run and message IDs are made.
func WithIDs(ids func() string) Option { return func(r *Runner) { r.ids = ids } }

// New returns a Runner that keeps history in store.
func New(store session.Store, opts ...Option) *Runner {
	r := &Runner{store: store, logger: slog.Default(), ids: randomID}
	for _, o := range opts {
		o(r)
	}
	return r
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type sender struct {
	mu      sync.Mutex
	done    bool
	sinkErr error
	framer  framer
	sink    func(rosetta.Event) error
	text    strings.Builder
	cancel  context.CancelFunc
}

func (s *sender) send(e rosetta.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return rosetta.ErrRunEnded
	}
	if s.sinkErr != nil {
		return s.sinkErr
	}
	e = s.framer.frame(e)
	if t, ok := e.(rosetta.TextDelta); ok {
		s.text.WriteString(t.Text)
	}
	if err := s.sink(e); err != nil {
		s.sinkErr = err
		s.cancel()
		return err
	}
	return nil
}

func (s *sender) finish() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	return s.text.String(), s.sinkErr
}

// Run runs agent with req and passes its framed events to sink.
func (r *Runner) Run(ctx context.Context, agent rosetta.Agent, req *rosetta.Request, sink func(rosetta.Event) error) error {
	name := agent.Card().Name
	if req.RunID == "" {
		req.RunID = r.ids()
	}
	if req.SessionID != "" && len(req.History) == 0 {
		h, err := r.store.History(ctx, name, req.SessionID)
		if err != nil {
			r.logger.Error("loading session history", "agent", name, "session", req.SessionID, "err", err)
		}
		req.History = h
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &sender{framer: framer{ids: r.ids}, sink: sink, cancel: cancel}

	done := make(chan error, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				r.logger.Error("agent panicked", "agent", name, "panic", p, "stack", string(debug.Stack()))
				done <- fmt.Errorf("agent %s: panic: %v", name, p)
			}
		}()
		done <- agent.Run(runCtx, req, rosetta.NewStream(s.send))
	}()

	var err error
	select {
	case err = <-done:
	case <-runCtx.Done():
	}
	text, sinkErr := s.finish()
	switch {
	case sinkErr != nil:
		return sinkErr
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		return err
	}
	r.remember(ctx, name, req, text)
	return nil
}

func (r *Runner) remember(ctx context.Context, agent string, req *rosetta.Request, text string) {
	if req.SessionID == "" {
		return
	}
	msgs := []rosetta.Message{{Role: rosetta.RoleUser, Parts: req.Input}}
	if text != "" {
		msgs = append(msgs, rosetta.Message{Role: rosetta.RoleAgent, Parts: []rosetta.Part{{Text: text}}})
	}
	if err := r.store.Append(ctx, agent, req.SessionID, msgs...); err != nil {
		r.logger.Error("saving session history", "agent", agent, "session", req.SessionID, "err", err)
	}
}
