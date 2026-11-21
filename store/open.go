package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the pgx driver
	_ "modernc.org/sqlite"             // registers the sqlite driver
)

// Open opens the database named by dsn and applies migrations.
func Open(ctx context.Context, dsn string) (*Store, error) {
	return OpenWith(ctx, dsn, Options{})
}

// OpenWith opens the database named by dsn with opts. A dsn is
// sqlite://<path>, sqlite://:memory:, or a postgres:// or postgresql:// URL.
func OpenWith(ctx context.Context, dsn string, opts Options) (*Store, error) {
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = DefaultSessionTTL
	}
	if opts.MaxMessages <= 0 {
		opts.MaxMessages = DefaultMaxMessages
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s, err := open(dsn)
	if err != nil {
		return nil, err
	}
	s.opts = opts
	if err := s.db.PingContext(ctx); err != nil {
		_ = s.db.Close()
		return nil, fmt.Errorf("store: connecting: %w", err)
	}
	if !opts.NoMigrate {
		if _, err := s.Migrate(ctx); err != nil {
			_ = s.db.Close()
			return nil, err
		}
	}
	return s, nil
}

func open(dsn string) (*Store, error) {
	switch {
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		return &Store{db: db, dialect: Postgres}, nil
	case strings.HasPrefix(dsn, "sqlite://"):
		path := strings.TrimPrefix(dsn, "sqlite://")
		if path == "" {
			return nil, errors.New("store: sqlite:// names no file")
		}
		pragmas := "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"
		if path == ":memory:" {
			db, err := sql.Open("sqlite", "file::memory:?"+pragmas)
			if err != nil {
				return nil, fmt.Errorf("store: %w", err)
			}
			db.SetMaxOpenConns(1)
			return &Store{db: db, dialect: SQLite}, nil
		}
		if dir := filepath.Dir(path); dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("store: %w", err)
			}
		}
		// The file holds key hashes and conversations, so only its owner may
		// read it, even when it was made by something else. SQLite gives the
		// -wal and -shm files the same mode.
		path = filepath.Clean(path)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		_ = f.Close()
		if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
			if err := os.Chmod(path, 0o600); err != nil {
				return nil, fmt.Errorf("store: %s can be read by other users and could not be made private: %w", path, err)
			}
		}
		db, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?"+pragmas+"&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
		if err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		return &Store{db: db, dialect: SQLite}, nil
	}
	// The DSN is not echoed: it may hold a password, in the user part or in
	// the query.
	return nil, errors.New("store: the DSN must start with sqlite://, postgres:// or postgresql://")
}
