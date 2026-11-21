package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/raven-clown/rosetta/session"
	"github.com/raven-clown/rosetta/store"
	"github.com/raven-clown/rosetta/store/storetest"
)

var _ session.Store = (*store.Store)(nil)

func openWith(t *testing.T, dsn string, opts store.Options) *store.Store {
	t.Helper()
	s, err := store.OpenWith(t.Context(), dsn, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMemory(t *testing.T) {
	storetest.Run(t, func(t *testing.T, opts store.Options) *store.Store {
		return openWith(t, "sqlite://:memory:", opts)
	})
}

func TestSQLiteFile(t *testing.T) {
	storetest.Run(t, func(t *testing.T, opts store.Options) *store.Store {
		return openWith(t, "sqlite://"+filepath.Join(t.TempDir(), "sub", "rosetta.db"), opts)
	})
}

func TestOpenErrors(t *testing.T) {
	tests := map[string]struct {
		dsn  string
		want string
	}{
		"unknown scheme": {"mysql://x", "not a sqlite:// or postgres:// URL"},
		"no path":        {"sqlite://", "names no file"},
		"password":       {"redis://u:secret@h/db", "xxxxx"},
		"unparseable":    {"redis://u:secret@h:bad port/db", "<unparseable dsn>"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := store.Open(context.Background(), tt.dsn)
			if err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), "secret") {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestReopenKeepsData(t *testing.T) {
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "rosetta.db")
	s := openWith(t, dsn, store.Options{})
	if err := s.SaveRun(t.Context(), store.Run{ID: "r1", Agent: "a", Status: store.RunCompleted}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s2 := openWith(t, dsn, store.Options{})
	if _, err := s2.GetRun(t.Context(), "r1"); err != nil {
		t.Errorf("run lost on reopen: %v", err)
	}
	if s2.Dialect() != store.SQLite {
		t.Errorf("dialect = %s", s2.Dialect())
	}
}

func TestFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix file modes")
	}
	path := filepath.Join(t.TempDir(), "rosetta.db")
	openWith(t, "sqlite://"+path, store.Options{})
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 600", mode)
	}
}

func TestTooNew(t *testing.T) {
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "rosetta.db")
	s := openWith(t, dsn, store.Options{})
	if err := store.ForceVersion(t.Context(), s, s.Latest()+5); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	_, err := store.Open(t.Context(), dsn)
	if !errors.Is(err, store.ErrTooNew) || !strings.Contains(err.Error(), "migration 6") {
		t.Errorf("err = %v", err)
	}
}

func TestNoMigrate(t *testing.T) {
	s := openWith(t, "sqlite://:memory:", store.Options{NoMigrate: true})
	n, err := s.Migrate(t.Context())
	if err != nil || n != s.Latest() {
		t.Errorf("migrate = %d, %v, want %d", n, err, s.Latest())
	}
}
