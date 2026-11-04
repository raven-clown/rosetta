package session

import (
	"context"
	"sync"
	"time"

	"github.com/raven-clown/rosetta"
)

// Limits used when no option sets them.
const (
	DefaultMaxSessions = 10000
	DefaultMaxMessages = 200
)

// Memory is a Store kept in process memory. Sessions expire ttl after
// their last append. When the store is full, expired sessions go first,
// then the least recently used one.
type Memory struct {
	ttl         time.Duration
	now         func() time.Time
	maxSessions int
	maxMessages int
	mu          sync.Mutex
	data        map[key]*entry
}

type key struct{ agent, session string }

type entry struct {
	msgs []rosetta.Message
	last time.Time
}

// Option changes a Memory limit.
type Option func(*Memory)

// MaxSessions caps how many sessions are kept.
func MaxSessions(n int) Option { return func(m *Memory) { m.maxSessions = n } }

// MaxMessages caps how many messages a session keeps. Older ones are dropped.
func MaxMessages(n int) Option { return func(m *Memory) { m.maxMessages = n } }

// NewMemory returns an empty Memory. A nil now uses time.Now.
func NewMemory(ttl time.Duration, now func() time.Time, opts ...Option) *Memory {
	if now == nil {
		now = time.Now
	}
	m := &Memory{
		ttl:         ttl,
		now:         now,
		maxSessions: DefaultMaxSessions,
		maxMessages: DefaultMaxMessages,
		data:        map[key]*entry{},
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Len reports how many sessions are stored, expired ones included.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.data)
}

func (m *Memory) expired(e *entry) bool { return m.now().Sub(e.last) > m.ttl }

func (m *Memory) live(k key) *entry {
	e, ok := m.data[k]
	if !ok {
		return nil
	}
	if m.expired(e) {
		delete(m.data, k)
		return nil
	}
	return e
}

func (m *Memory) makeRoom() {
	for k, e := range m.data {
		if m.expired(e) {
			delete(m.data, k)
		}
	}
	for len(m.data) >= m.maxSessions {
		var oldest key
		var at time.Time
		first := true
		for k, e := range m.data {
			if first || e.last.Before(at) {
				oldest, at, first = k, e.last, false
			}
		}
		delete(m.data, oldest)
	}
}

// History returns a copy of the session's messages, or an empty slice.
func (m *Memory) History(_ context.Context, agent, sessionID string) ([]rosetta.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.live(key{agent, sessionID})
	if e == nil {
		return []rosetta.Message{}, nil
	}
	return append([]rosetta.Message{}, e.msgs...), nil
}

// Append adds messages to the end of the session.
func (m *Memory) Append(_ context.Context, agent, sessionID string, msgs ...rosetta.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key{agent, sessionID}
	e := m.live(k)
	if e == nil {
		m.makeRoom()
		e = &entry{}
		m.data[k] = e
	}
	e.msgs = append(e.msgs, msgs...)
	if over := len(e.msgs) - m.maxMessages; over > 0 {
		e.msgs = append([]rosetta.Message{}, e.msgs[over:]...)
	}
	e.last = m.now()
	return nil
}

// Delete removes the session.
func (m *Memory) Delete(_ context.Context, agent, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key{agent, sessionID})
	return nil
}
