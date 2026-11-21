package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SaveRun writes a run record. An empty ID is generated. Saving an ID
// twice fails, since only rosetta makes IDs.
func (s *Store) SaveRun(ctx context.Context, r Run) error {
	if r.ID == "" {
		r.ID = newID()
	}
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO runs (id, org_id, agent, run_id, protocol, session_id, caller, status, error,
		started_at, ended_at, input_tokens, output_tokens, reasoning_tokens, request_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		r.ID, org, r.Agent, r.RunID, r.Protocol, r.SessionID, r.Caller, r.Status, r.Error,
		nanos(r.Started), nanos(r.Ended), r.InputTokens, r.OutputTokens, r.ReasoningTokens, r.RequestID)
	if isUnique(err) {
		return fmt.Errorf("store: saving run: %w", ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("store: saving run: %w", err)
	}
	return nil
}

const runCols = `id, agent, run_id, protocol, session_id, caller, status, error, started_at, ended_at, input_tokens, output_tokens, reasoning_tokens, request_id`

func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var started, ended int64
	err := sc.Scan(&r.ID, &r.Agent, &r.RunID, &r.Protocol, &r.SessionID, &r.Caller, &r.Status, &r.Error,
		&started, &ended, &r.InputTokens, &r.OutputTokens, &r.ReasoningTokens, &r.RequestID)
	r.Started, r.Ended = fromNanos(started), fromNanos(ended)
	return r, err
}

// ListRuns returns runs, newest first, and the cursor of the next page.
func (s *Store) ListRuns(ctx context.Context, f RunFilter, p Page) ([]Run, string, error) {
	where := []string{"org_id = ?"}
	args := []any{org}
	for _, c := range []struct{ col, v string }{{"agent", f.Agent}, {"protocol", f.Protocol}, {"status", f.Status}} {
		if c.v != "" {
			where = append(where, c.col+" = ?")
			args = append(args, c.v)
		}
	}
	if !f.From.IsZero() {
		where = append(where, "started_at >= ?")
		args = append(args, nanos(f.From))
	}
	if !f.To.IsZero() {
		where = append(where, "started_at < ?")
		args = append(args, nanos(f.To))
	}
	tail, args, err := pageQuery(where, args, "started_at", p)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT `+runCols+` FROM runs`+tail), args...)
	if err != nil {
		return nil, "", fmt.Errorf("store: listing runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, "", fmt.Errorf("store: listing runs: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("store: listing runs: %w", err)
	}
	out, cur := next(out, p, func(r Run) cursor { return cursor{nanos(r.Started), r.ID} })
	return out, cur, nil
}

// GetRun returns one run by rosetta's ID.
func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, s.q(`SELECT `+runCols+` FROM runs WHERE org_id = ? AND id = ?`), org, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("store: loading run: %w", err)
	}
	return r, nil
}
