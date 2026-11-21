// Package run runs agents for the protocol adapters: it frames events,
// keeps session history, recovers panics and handles cancellation.
package run

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/protocol"
	"github.com/raven-clown/rosetta/session"
	"github.com/raven-clown/rosetta/store"
)

// Runner implements protocol.Runner.
type Runner struct {
	store    session.Store
	logger   *slog.Logger
	ids      func() string
	observer Observer
	recorder Recorder
}

// Recorder saves the record of every finished run. *store.Store
// implements it.
type Recorder interface {
	SaveRun(ctx context.Context, r store.Run) error
}

// WithRecorder sets where run records go. Without one, runs are not
// recorded.
func WithRecorder(rec Recorder) Option { return func(r *Runner) { r.recorder = rec } }

const recordTimeout = 5 * time.Second

// Observer is told about every finished run and every failed store call,
// for metrics.
type Observer interface {
	RunDone(agent string, err error, d time.Duration, u rosetta.Usage)
	StoreError(op string)
}

type noObserver struct{}

func (noObserver) RunDone(string, error, time.Duration, rosetta.Usage) {}
func (noObserver) StoreError(string)                                   {}

// WithObserver sets the Observer.
func WithObserver(o Observer) Option { return func(r *Runner) { r.observer = o } }

// Option configures a Runner.
type Option func(*Runner)

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(r *Runner) { r.logger = l } }

// WithIDs sets how run and message IDs are made.
func WithIDs(ids func() string) Option { return func(r *Runner) { r.ids = ids } }

// New returns a Runner that keeps history in store.
func New(store session.Store, opts ...Option) *Runner {
	r := &Runner{store: store, logger: slog.Default(), ids: randomID, observer: noObserver{}}
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
	usage   rosetta.Usage
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
	switch e := e.(type) {
	case rosetta.TextDelta:
		s.text.WriteString(e.Text)
	case rosetta.Usage:
		s.usage.InputTokens += e.InputTokens
		s.usage.OutputTokens += e.OutputTokens
		s.usage.ReasoningTokens += e.ReasoningTokens
	}
	if err := s.sink(e); err != nil {
		s.sinkErr = err
		s.cancel()
		return err
	}
	return nil
}

func (s *sender) finish() (string, rosetta.Usage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	return s.text.String(), s.usage, s.sinkErr
}

// Run runs agent with req and passes its framed events to sink. A nil
// History means the protocol did not send one: it is loaded from the store
// and the run is saved there afterwards. A non-nil History is used as given.
func (r *Runner) Run(ctx context.Context, agent rosetta.Agent, req *rosetta.Request, sink func(rosetta.Event) error) (err error) {
	name := agent.Card().Name
	start := time.Now()
	var usage rosetta.Usage
	if req.RunID == "" {
		req.RunID = r.ids()
	}
	defer func() {
		r.observer.RunDone(name, err, time.Since(start), usage)
		r.record(ctx, name, req, start, usage, err)
	}()
	keep := req.SessionID != "" && req.History == nil
	if keep {
		h, err := r.store.History(ctx, name, sessionKey(req))
		if err != nil {
			r.logger.Error("loading session history", "agent", name, "session", req.SessionID, "err", err)
			r.observer.StoreError("history")
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
				done <- fmt.Errorf("agent %s panicked, see the server log for details", name)
			}
		}()
		done <- agent.Run(runCtx, req, rosetta.NewStream(s.send))
	}()

	var runErr error
	select {
	case runErr = <-done:
	case <-runCtx.Done():
	}
	text, usage, sinkErr := s.finish()
	switch {
	case sinkErr != nil:
		return sinkErr
	case ctx.Err() != nil:
		return ctx.Err()
	case runErr != nil:
		return runErr
	}
	if keep {
		r.remember(ctx, name, req, text)
	}
	return nil
}

// record saves the run after its answer has gone out. The save has its own
// deadline and outlives a cancelled run, so cancelled runs are recorded too.
func (r *Runner) record(ctx context.Context, agent string, req *rosetta.Request, start time.Time, u rosetta.Usage, err error) {
	if r.recorder == nil {
		return
	}
	rec := store.Run{
		ID: req.RunID, Agent: agent, Protocol: protocol.NameFrom(ctx), SessionID: req.SessionID,
		Caller: req.Caller.ID, Status: store.RunCompleted, Started: start, Ended: time.Now(),
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, ReasoningTokens: u.ReasoningTokens,
		RequestID: req.Caller.RequestID,
	}
	switch {
	case errors.Is(err, context.Canceled):
		rec.Status = store.RunCancelled
	case err != nil:
		rec.Status, rec.Error = store.RunFailed, err.Error()
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	if err := r.recorder.SaveRun(saveCtx, rec); err != nil {
		r.logger.Error("saving run record", "agent", agent, "run", req.RunID, "err", err)
		r.observer.StoreError("run")
	}
}

func (r *Runner) remember(ctx context.Context, agent string, req *rosetta.Request, text string) {
	msgs := []rosetta.Message{{Role: rosetta.RoleUser, Parts: req.Input}}
	if text != "" {
		msgs = append(msgs, rosetta.Message{Role: rosetta.RoleAgent, Parts: []rosetta.Part{{Text: text}}})
	}
	if err := r.store.Append(ctx, agent, sessionKey(req), msgs...); err != nil {
		r.logger.Error("saving session history", "agent", agent, "session", req.SessionID, "err", err)
		r.observer.StoreError("append")
	}
}

// sessionKey scopes a session to its caller, so one caller cannot read
// another's history by guessing the session ID.
func sessionKey(req *rosetta.Request) string {
	var who string
	switch {
	case req.Caller.ID != "":
		who = "id:" + req.Caller.ID
	case req.Caller.Token != "":
		who = "token:" + req.Caller.Token
	default:
		return "anon:" + req.SessionID
	}
	sum := sha256.Sum256([]byte(who))
	return "caller:" + hex.EncodeToString(sum[:8]) + "/" + req.SessionID
}
