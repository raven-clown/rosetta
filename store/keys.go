package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const keyCols = `id, name, role, prefix, hash, created_at, created_by, revoked_at, version`

func scanKey(sc interface{ Scan(...any) error }) (Key, error) {
	var k Key
	var created, revoked int64
	err := sc.Scan(&k.ID, &k.Name, &k.Role, &k.Prefix, &k.Hash, &created, &k.CreatedBy, &revoked, &k.Version)
	k.Created, k.Revoked = fromNanos(created), fromNanos(revoked)
	return k, err
}

// ListKeys returns every stored key, revoked ones included, newest first.
func (s *Store) ListKeys(ctx context.Context) ([]Key, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT `+keyCols+` FROM api_keys WHERE org_id = ? ORDER BY created_at DESC, id DESC`), org)
	if err != nil {
		return nil, fmt.Errorf("store: listing keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Key
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing keys: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// GetKey returns one stored key.
func (s *Store) GetKey(ctx context.Context, id string) (Key, error) {
	k, err := scanKey(s.db.QueryRowContext(ctx, s.q(`SELECT `+keyCols+` FROM api_keys WHERE org_id = ? AND id = ?`), org, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Key{}, ErrNotFound
	}
	if err != nil {
		return Key{}, fmt.Errorf("store: loading key: %w", err)
	}
	return k, nil
}

// CountKeys returns how many keys are stored and not revoked.
func (s *Store) CountKeys(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM api_keys WHERE org_id = ? AND revoked_at = 0`), org).Scan(&n)
	return n, err
}

// CreateKey stores a key at version 1. An empty ID is generated.
func (s *Store) CreateKey(ctx context.Context, k Key, au Audit) (Key, error) {
	if k.ID == "" {
		k.ID = newID()
	}
	k.Version, k.Created = 1, s.now()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		_, err := s.exec(ctx, tx, `INSERT INTO api_keys (id, org_id, name, role, prefix, hash, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			k.ID, org, k.Name, k.Role, k.Prefix, k.Hash, nanos(k.Created), k.CreatedBy)
		if isUnique(err) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if err := s.bump(ctx, tx); err != nil {
			return err
		}
		return s.audit(ctx, tx, au)
	})
	return k, err
}

// RevokeKey marks a key revoked. A version above zero must match.
// Revoking a revoked key changes nothing and still succeeds.
func (s *Store) RevokeKey(ctx context.Context, id string, version int64, au Audit) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := s.checkVersion(ctx, tx, `SELECT version FROM api_keys WHERE org_id = ? AND id = ?`, version, org, id); err != nil {
			return err
		}
		if _, err := s.exec(ctx, tx, `UPDATE api_keys SET revoked_at = ?, version = version + 1 WHERE org_id = ? AND id = ? AND revoked_at = 0`,
			nanos(s.now()), org, id); err != nil {
			return err
		}
		if err := s.bump(ctx, tx); err != nil {
			return err
		}
		return s.audit(ctx, tx, au)
	})
}
