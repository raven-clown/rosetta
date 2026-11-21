package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations
var migrations embed.FS

// lockID is the Postgres advisory lock held while migrating, so replicas
// that start together do not apply the same migration twice.
const lockID = 0x726f7365747461

type migration struct {
	version int
	name    string
	sql     string
}

func (s *Store) migrations() ([]migration, error) {
	dir := path.Join("migrations", s.dialect)
	entries, err := fs.ReadDir(migrations, dir)
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || !strings.HasSuffix(e.Name(), ".sql") {
			return nil, fmt.Errorf("store: bad migration name %s", e.Name())
		}
		b, err := migrations.ReadFile(path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// Latest returns the newest migration this binary knows.
func (s *Store) Latest() int {
	ms, _ := s.migrations()
	if len(ms) == 0 {
		return 0
	}
	return ms[len(ms)-1].version
}

// Migrate applies the migrations the database does not have yet and
// returns how many ran. It fails with ErrTooNew when the database has a
// migration this binary does not know.
func (s *Store) Migrate(ctx context.Context) (int, error) {
	ms, err := s.migrations()
	if err != nil {
		return 0, err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: migrating: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if s.dialect == Postgres {
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
			return 0, fmt.Errorf("store: taking the migration lock: %w", err)
		}
		defer func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockID) }()
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at BIGINT NOT NULL)`); err != nil {
		return 0, fmt.Errorf("store: migrating: %w", err)
	}
	var newest int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&newest); err != nil {
		return 0, fmt.Errorf("store: migrating: %w", err)
	}
	if latest := ms[len(ms)-1].version; newest > latest {
		return 0, fmt.Errorf("%w: it has migration %d, this binary knows up to %d; run a newer rosetta", ErrTooNew, newest, latest)
	}
	applied := 0
	for _, m := range ms {
		if m.version <= newest {
			continue
		}
		ran, err := s.apply(ctx, conn, m)
		if err != nil {
			return applied, err
		}
		if ran {
			applied++
		}
	}
	return applied, nil
}

// apply runs one migration in its own transaction. It checks again inside
// the transaction, since another process may have applied it meanwhile.
func (s *Store) apply(ctx context.Context, conn *sql.Conn, m migration) (bool, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: migration %s: %w", m.name, err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`), m.version).Scan(&n); err != nil {
		return false, fmt.Errorf("store: migration %s: %w", m.name, err)
	}
	if n > 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return false, fmt.Errorf("store: migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`), m.version, nanos(s.now())); err != nil {
		return false, fmt.Errorf("store: migration %s: %w", m.name, err)
	}
	return true, tx.Commit()
}
