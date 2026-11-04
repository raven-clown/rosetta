package session

import (
	"context"
	"sync"
	"time"

	"github.com/raven-clown/rosetta"
)

// Memory is a Store kept in process memory. Sessions expire ttl after
// their last append.
type Memory struct {
	ttl  time.Duration
	now  func() time.Time
	mu   sync.Mutex
	data map[key]*entry
}

type key struct{ agent, session string }

type entry struct {
	msgs []rosetta.Message
	last time.Time
}

// NewMemory returns an empty Memory. A nil now uses time.Now.
func NewMemory(ttl time.Duration, now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{ttl: ttl, now: now, data: map[key]*entry{}}
}

func (m *Memory) live(k key) *entry {
	e, ok := m.data[k]
	if !ok {
		return nil
	}
	if m.now().Sub(e.last) > m.ttl {
		delete(m.data, k)
		return nil
	}
	return e
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
		e = &entry{}
		m.data[k] = e
	}
	e.msgs = append(e.msgs, msgs...)
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
