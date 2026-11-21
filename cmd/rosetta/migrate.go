package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/raven-clown/rosetta/store"
)

// migrate applies database migrations without serving, for teams that
// migrate as a separate deploy step.
func migrate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to rosetta.yaml")
	dsn := fs.String("dsn", "", "database to migrate (default storage.dsn from the config)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if *dsn != "" {
		cfg.Storage.DSN = *dsn
	}
	st, err := store.OpenWith(ctx, cfg.Storage.DSN, store.Options{NoMigrate: true})
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	n, err := st.Migrate(ctx)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "rosetta: applied %d migrations, the %s database is at version %d\n", n, st.Dialect(), st.Latest())
	return nil
}
