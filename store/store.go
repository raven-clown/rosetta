// Package store keeps rosetta's state in SQL: session history, run records,
// agents and API keys made through the admin API, and the audit log. It
// runs on SQLite (a file, or in memory) and on Postgres.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Errors callers check with errors.Is.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: name already taken")
	ErrVersion  = errors.New("store: version does not match")
	ErrTooNew   = errors.New("store: database is newer than this binary")
)

// org is the only organisation until organisations ship. Every query
// filters by it, so adding them later needs no data migration.
const org = "default"

// Dialects.
const (
	SQLite   = "sqlite"
	Postgres = "postgres"
)

// Options tune a Store. Zero values take the defaults.
type Options struct {
	SessionTTL  time.Duration
	MaxMessages int
	Now         func() time.Time
	NoMigrate   bool
}

// Defaults used when Options leaves a field empty.
const (
	DefaultSessionTTL  = 24 * time.Hour
	DefaultMaxMessages = 200
)

// Store is rosetta's state in one SQL database.
type Store struct {
	db      *sql.DB
	dialect string
	opts    Options
}

// Session is a conversation as the admin API shows it. SessionID is the ID
// the client sent; ID is the store's own.
type Session struct {
	ID        string
	Agent     string
	SessionID string
	Caller    string
	Messages  int
	Created   time.Time
	Updated   time.Time
	Expires   time.Time
	Version   int64
}

// SessionFilter narrows ListSessions.
type SessionFilter struct {
	Agent string
}

// Run is the record of one agent run. ID is made by rosetta; RunID is the
// protocol's run ID, which a client may choose, so it is not unique.
type Run struct {
	ID              string
	Agent           string
	RunID           string
	Protocol        string
	SessionID       string
	Caller          string
	Status          string
	Error           string
	Started         time.Time
	Ended           time.Time
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	RequestID       string
}

// Run statuses.
const (
	RunCompleted = "completed"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
)

// RunFilter narrows ListRuns. Empty fields match everything.
type RunFilter struct {
	Agent    string
	Protocol string
	Status   string
	From, To time.Time
}

// Agent is an agent made through the admin API. Spec is its JSON body in
// the config agent shape.
type Agent struct {
	Name      string
	Spec      json.RawMessage
	Version   int64
	Created   time.Time
	Updated   time.Time
	UpdatedBy string
}

// Key is a stored API key. Hash is the hex SHA-256 of the key; the key
// itself is never stored.
type Key struct {
	ID        string
	Name      string
	Role      string
	Prefix    string
	Hash      string
	Created   time.Time
	CreatedBy string
	Revoked   time.Time
	Version   int64
}

// Audit is one write made through the admin API.
type Audit struct {
	ID      string
	Time    time.Time
	Actor   string
	Action  string
	Target  string
	Details json.RawMessage
}

// AuditFilter narrows ListAudit.
type AuditFilter struct {
	Action   string
	Target   string
	From, To time.Time
}

// Page asks for one page of a list. Limit 0 means 50.
type Page struct {
	Cursor string
	Limit  int
}

// Dialect returns SQLite or Postgres.
func (s *Store) Dialect() string { return s.dialect }

// Ping checks that the database answers.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) now() time.Time { return s.opts.Now().UTC() }

// q rewrites ? placeholders to $n for Postgres. Queries here never hold a
// literal question mark.
func (s *Store) q(query string) string {
	if s.dialect != Postgres {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// forUpdate locks the selected row until the transaction ends. Postgres
// needs it, since READ COMMITTED lets two transactions read the same row
// and both write. SQLite takes its write lock at BEGIN, so it needs none.
func (s *Store) forUpdate() string {
	if s.dialect == Postgres {
		return " FOR UPDATE"
	}
	return ""
}

func (s *Store) exec(ctx context.Context, tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	return tx.ExecContext(ctx, s.q(query), args...)
}

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func nanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func fromNanos(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

// audit writes the record in tx, filling in the ID and time.
func (s *Store) audit(ctx context.Context, tx *sql.Tx, a Audit) error {
	if a.ID == "" {
		a.ID = newID()
	}
	if a.Time.IsZero() {
		a.Time = s.now()
	}
	details := a.Details
	if len(details) == 0 {
		details = json.RawMessage("{}")
	}
	_, err := s.exec(ctx, tx, `INSERT INTO audit (id, org_id, at, actor, action, target, details) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.ID, org, nanos(a.Time), a.Actor, a.Action, a.Target, string(details))
	if err != nil {
		return fmt.Errorf("store: writing audit: %w", err)
	}
	return nil
}

func (s *Store) bump(ctx context.Context, tx *sql.Tx) error {
	_, err := s.exec(ctx, tx, `UPDATE revision SET value = value + 1 WHERE id = 1`)
	return err
}

// Revision returns the counter that moves on every change to agents or
// keys, so other processes know when to reload.
func (s *Store) Revision(ctx context.Context) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT value FROM revision WHERE id = 1`).Scan(&v)
	return v, err
}

func isUnique(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "SQLSTATE 23505")
}

// Prune deletes expired sessions and runs that started before now minus
// retention. A zero retention keeps runs.
func (s *Store) Prune(ctx context.Context, now time.Time, retention time.Duration) (int64, error) {
	var total int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := s.exec(ctx, tx, `DELETE FROM sessions WHERE org_id = ? AND expires_at < ?`, org, nanos(now))
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		total += n
		if retention <= 0 {
			return nil
		}
		res, err = s.exec(ctx, tx, `DELETE FROM runs WHERE org_id = ? AND started_at < ?`, org, nanos(now.Add(-retention)))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		total += n
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("store: pruning: %w", err)
	}
	return total, nil
}
