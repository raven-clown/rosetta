package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const agentCols = `name, spec, version, created_at, updated_at, updated_by`

func scanAgent(sc interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	var spec string
	var created, updated int64
	err := sc.Scan(&a.Name, &spec, &a.Version, &created, &updated, &a.UpdatedBy)
	a.Spec = []byte(spec)
	a.Created, a.Updated = fromNanos(created), fromNanos(updated)
	return a, err
}

// ListAgents returns every stored agent by name.
func (s *Store) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT `+agentCols+` FROM agents WHERE org_id = ? ORDER BY name`), org)
	if err != nil {
		return nil, fmt.Errorf("store: listing agents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing agents: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAgent returns one stored agent.
func (s *Store) GetAgent(ctx context.Context, name string) (Agent, error) {
	a, err := scanAgent(s.db.QueryRowContext(ctx, s.q(`SELECT `+agentCols+` FROM agents WHERE org_id = ? AND name = ?`), org, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("store: loading agent: %w", err)
	}
	return a, nil
}

// CreateAgent stores a new agent with its audit record. Its first version
// is the revision this write makes. Every agent write moves the revision,
// so an agent deleted and made again never repeats an old version, and a
// write or a reload based on the old one sees the difference.
func (s *Store) CreateAgent(ctx context.Context, a Agent, au Audit) (Agent, error) {
	now := s.now()
	a.Created, a.Updated = now, now
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := s.bump(ctx, tx); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT value FROM revision WHERE id = 1`).Scan(&a.Version); err != nil {
			return err
		}
		_, err := s.exec(ctx, tx, `INSERT INTO agents (org_id, name, spec, version, created_at, updated_at, updated_by) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			org, a.Name, string(a.Spec), a.Version, nanos(now), nanos(now), a.UpdatedBy)
		if isUnique(err) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, au)
	})
	return a, err
}

// UpdateAgent replaces a stored agent's spec. A version above zero must
// match the stored one.
func (s *Store) UpdateAgent(ctx context.Context, a Agent, version int64, au Audit) (Agent, error) {
	var out Agent
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := s.checkVersion(ctx, tx, `SELECT version FROM agents WHERE org_id = ? AND name = ?`, version, org, a.Name); err != nil {
			return err
		}
		if _, err := s.exec(ctx, tx, `UPDATE agents SET spec = ?, version = version + 1, updated_at = ?, updated_by = ? WHERE org_id = ? AND name = ?`,
			string(a.Spec), nanos(s.now()), a.UpdatedBy, org, a.Name); err != nil {
			return err
		}
		var err error
		if out, err = scanAgent(tx.QueryRowContext(ctx, s.q(`SELECT `+agentCols+` FROM agents WHERE org_id = ? AND name = ?`), org, a.Name)); err != nil {
			return err
		}
		if err := s.bump(ctx, tx); err != nil {
			return err
		}
		return s.audit(ctx, tx, au)
	})
	return out, err
}

// DeleteAgent deletes a stored agent. A version above zero must match.
func (s *Store) DeleteAgent(ctx context.Context, name string, version int64, au Audit) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := s.checkVersion(ctx, tx, `SELECT version FROM agents WHERE org_id = ? AND name = ?`, version, org, name); err != nil {
			return err
		}
		if _, err := s.exec(ctx, tx, `DELETE FROM agents WHERE org_id = ? AND name = ?`, org, name); err != nil {
			return err
		}
		if err := s.bump(ctx, tx); err != nil {
			return err
		}
		return s.audit(ctx, tx, au)
	})
}
