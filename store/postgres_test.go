//go:build integration

package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/raven-clown/rosetta/store"
	"github.com/raven-clown/rosetta/store/storetest"
)

var schemaSeq atomic.Int64

// postgresDSN returns a DSN for an empty schema of its own, dropped when
// the test ends.
func postgresDSN(t *testing.T) string {
	t.Helper()
	base := os.Getenv("ROSETTA_TEST_POSTGRES_DSN")
	if base == "" {
		t.Skip("ROSETTA_TEST_POSTGRES_DSN is not set")
	}
	db, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema := fmt.Sprintf("rosetta_test_%d_%d", os.Getpid(), schemaSeq.Add(1))
	if _, err := db.ExecContext(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func TestPostgres(t *testing.T) {
	storetest.Run(t, func(t *testing.T, opts store.Options) *store.Store {
		return openWith(t, postgresDSN(t), opts)
	})
}

func TestPostgresMigrateTogether(t *testing.T) {
	dsn := postgresDSN(t)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() {
			s, err := store.Open(t.Context(), dsn)
			if err == nil {
				_ = s.Close()
			}
			errs[i] = err
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresTooNew(t *testing.T) {
	dsn := postgresDSN(t)
	s := openWith(t, dsn, store.Options{})
	if err := store.ForceVersion(t.Context(), s, s.Latest()+1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(t.Context(), dsn); !errors.Is(err, store.ErrTooNew) {
		t.Errorf("err = %v", err)
	}
	if s.Dialect() != store.Postgres {
		t.Errorf("dialect = %s", s.Dialect())
	}
}
