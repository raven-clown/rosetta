package session

import (
	"container/list"
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
	data        map[key]*list.Element
	lru         *list.List
}

type key struct{ agent, session string }

type entry struct {
	key  key
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
		data:        map[key]*list.Element{},
		lru:         list.New(),
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

func (m *Memory) remove(el *list.Element) {
	delete(m.data, el.Value.(*entry).key)
	m.lru.Remove(el)
}

func (m *Memory) live(k key) *list.Element {
	el, ok := m.data[k]
	if !ok {
		return nil
	}
	if m.expired(el.Value.(*entry)) {
		m.remove(el)
		return nil
	}
	return el
}

// makeRoom relies on the list being ordered by last append, oldest first.
func (m *Memory) makeRoom() {
	for el := m.lru.Front(); el != nil && m.expired(el.Value.(*entry)); el = m.lru.Front() {
		m.remove(el)
	}
	for m.lru.Len() >= m.maxSessions {
		m.remove(m.lru.Front())
	}
}

// History returns a copy of the session's messages, or an empty slice.
func (m *Memory) History(_ context.Context, agent, sessionID string) ([]rosetta.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	el := m.live(key{agent, sessionID})
	if el == nil {
		return []rosetta.Message{}, nil
	}
	return append([]rosetta.Message{}, el.Value.(*entry).msgs...), nil
}

// Append adds messages to the end of the session.
func (m *Memory) Append(_ context.Context, agent, sessionID string, msgs ...rosetta.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key{agent, sessionID}
	el := m.live(k)
	if el == nil {
		m.makeRoom()
		el = m.lru.PushBack(&entry{key: k})
		m.data[k] = el
	} else {
		m.lru.MoveToBack(el)
	}
	e := el.Value.(*entry)
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
	if el, ok := m.data[key{agent, sessionID}]; ok {
		m.remove(el)
	}
	return nil
}
