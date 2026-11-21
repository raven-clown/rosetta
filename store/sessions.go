package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/raven-clown/rosetta"
)

type wirePart struct {
	Text string          `json:"text,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

func encodeParts(parts []rosetta.Part) (string, error) {
	w := make([]wirePart, len(parts))
	for i, p := range parts {
		w[i] = wirePart(p)
	}
	b, err := json.Marshal(w)
	return string(b), err
}

func decodeParts(s string) ([]rosetta.Part, error) {
	var w []wirePart
	if err := json.Unmarshal([]byte(s), &w); err != nil {
		return nil, err
	}
	parts := make([]rosetta.Part, len(w))
	for i, p := range w {
		parts[i] = rosetta.Part(p)
	}
	return parts, nil
}

// splitKey takes the caller and the client's session ID out of the key the
// runner uses, "caller:<hash>/<id>" or "anon:<id>".
func splitKey(key string) (caller, id string) {
	if rest, ok := strings.CutPrefix(key, "anon:"); ok {
		return "", rest
	}
	if strings.HasPrefix(key, "caller:") {
		if c, id, ok := strings.Cut(key, "/"); ok {
			return c, id
		}
	}
	return "", key
}

// History returns the session's messages, oldest first, or none when the
// session does not exist or has expired.
func (s *Store) History(ctx context.Context, agent, key string) ([]rosetta.Message, error) {
	var id string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT id FROM sessions WHERE org_id = ? AND agent = ? AND session_key = ? AND expires_at >= ?`),
		org, agent, key, nanos(s.now())).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return []rosetta.Message{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: loading history: %w", err)
	}
	return s.messages(ctx, s.db, id)
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) messages(ctx context.Context, db querier, sessionID string) ([]rosetta.Message, error) {
	rows, err := db.QueryContext(ctx, s.q(`SELECT role, parts FROM messages WHERE org_id = ? AND session_id = ? ORDER BY seq`), org, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: loading history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	msgs := []rosetta.Message{}
	for rows.Next() {
		var role, raw string
		if err := rows.Scan(&role, &raw); err != nil {
			return nil, fmt.Errorf("store: loading history: %w", err)
		}
		parts, err := decodeParts(raw)
		if err != nil {
			return nil, fmt.Errorf("store: loading history: %w", err)
		}
		msgs = append(msgs, rosetta.Message{Role: rosetta.Role(role), Parts: parts})
	}
	return msgs, rows.Err()
}

// Append adds messages to the session, starting it when it does not exist
// or has expired, and keeps at most Options.MaxMessages of them.
func (s *Store) Append(ctx context.Context, agent, key string, msgs ...rosetta.Message) error {
	now := s.now()
	caller, clientID := splitKey(key)
	encoded := make([]string, len(msgs))
	for i, m := range msgs {
		var err error
		if encoded[i], err = encodeParts(m.Parts); err != nil {
			return fmt.Errorf("store: saving history: message %d: %w", i, err)
		}
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := s.exec(ctx, tx, `DELETE FROM sessions WHERE org_id = ? AND agent = ? AND session_key = ? AND expires_at < ?`,
			org, agent, key, nanos(now)); err != nil {
			return err
		}
		if _, err := s.exec(ctx, tx, `INSERT INTO sessions (id, org_id, agent, session_key, session_id, caller, created_at, updated_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (org_id, agent, session_key) DO NOTHING`,
			newID(), org, agent, key, clientID, caller, nanos(now), nanos(now), nanos(now.Add(s.opts.SessionTTL))); err != nil {
			return err
		}
		var id string
		var last int64
		if err := tx.QueryRowContext(ctx, s.q(`SELECT id FROM sessions WHERE org_id = ? AND agent = ? AND session_key = ?`)+s.forUpdate(),
			org, agent, key).Scan(&id); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, s.q(`SELECT COALESCE(MAX(seq), 0) FROM messages WHERE org_id = ? AND session_id = ?`), org, id).Scan(&last); err != nil {
			return err
		}
		for i, m := range msgs {
			last++
			if _, err := s.exec(ctx, tx, `INSERT INTO messages (session_id, org_id, seq, role, parts, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
				id, org, last, string(m.Role), encoded[i], nanos(now)); err != nil {
				return err
			}
		}
		if _, err := s.exec(ctx, tx, `DELETE FROM messages WHERE org_id = ? AND session_id = ? AND seq <= ?`, org, id, last-int64(s.opts.MaxMessages)); err != nil {
			return err
		}
		_, err := s.exec(ctx, tx, `UPDATE sessions SET messages = (SELECT COUNT(*) FROM messages WHERE org_id = ? AND session_id = ?),
			updated_at = ?, expires_at = ?, version = version + 1 WHERE org_id = ? AND id = ?`,
			org, id, nanos(now), nanos(now.Add(s.opts.SessionTTL)), org, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: saving history: %w", err)
	}
	return nil
}

// Delete removes the session with the runner's key, if there is one.
func (s *Store) Delete(ctx context.Context, agent, key string) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM sessions WHERE org_id = ? AND agent = ? AND session_key = ?`), org, agent, key)
	if err != nil {
		return fmt.Errorf("store: deleting session: %w", err)
	}
	return nil
}

const sessionCols = `id, agent, session_id, caller, messages, created_at, updated_at, expires_at, version`

func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var se Session
	var created, updated, expires int64
	err := sc.Scan(&se.ID, &se.Agent, &se.SessionID, &se.Caller, &se.Messages, &created, &updated, &expires, &se.Version)
	se.Created, se.Updated, se.Expires = fromNanos(created), fromNanos(updated), fromNanos(expires)
	return se, err
}

// ListSessions returns live sessions, most recently used first, and the
// cursor of the next page.
func (s *Store) ListSessions(ctx context.Context, f SessionFilter, p Page) ([]Session, string, error) {
	where := []string{"org_id = ?", "expires_at >= ?"}
	args := []any{org, nanos(s.now())}
	if f.Agent != "" {
		where = append(where, "agent = ?")
		args = append(args, f.Agent)
	}
	tail, args, err := pageQuery(where, args, "updated_at", p)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT `+sessionCols+` FROM sessions`+tail), args...)
	if err != nil {
		return nil, "", fmt.Errorf("store: listing sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Session
	for rows.Next() {
		se, err := scanSession(rows)
		if err != nil {
			return nil, "", fmt.Errorf("store: listing sessions: %w", err)
		}
		out = append(out, se)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("store: listing sessions: %w", err)
	}
	out, cur := next(out, p, func(se Session) cursor { return cursor{nanos(se.Updated), se.ID} })
	return out, cur, nil
}

// GetSession returns one live session and its messages, read from one
// snapshot so the count and version match the messages.
func (s *Store) GetSession(ctx context.Context, id string) (Session, []rosetta.Message, error) {
	var opts *sql.TxOptions
	if s.dialect == Postgres {
		opts = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return Session{}, nil, fmt.Errorf("store: loading session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	se, err := scanSession(tx.QueryRowContext(ctx, s.q(`SELECT `+sessionCols+` FROM sessions WHERE org_id = ? AND id = ? AND expires_at >= ?`),
		org, id, nanos(s.now())))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, nil, ErrNotFound
	}
	if err != nil {
		return Session{}, nil, fmt.Errorf("store: loading session: %w", err)
	}
	msgs, err := s.messages(ctx, tx, id)
	return se, msgs, err
}

// DeleteSession deletes a session by its store ID. A version above zero
// must match the stored one.
func (s *Store) DeleteSession(ctx context.Context, id string, version int64, a Audit) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := s.checkVersion(ctx, tx, `SELECT version FROM sessions WHERE org_id = ? AND id = ?`, version, org, id); err != nil {
			return err
		}
		if _, err := s.exec(ctx, tx, `DELETE FROM sessions WHERE org_id = ? AND id = ?`, org, id); err != nil {
			return err
		}
		return s.audit(ctx, tx, a)
	})
}

// checkVersion fails with ErrNotFound when the row is missing, and with
// ErrVersion when want is above zero and differs from the stored version.
func (s *Store) checkVersion(ctx context.Context, tx *sql.Tx, query string, want int64, args ...any) error {
	var got int64
	err := tx.QueryRowContext(ctx, s.q(query)+s.forUpdate(), args...).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if want > 0 && want != got {
		return ErrVersion
	}
	return nil
}
