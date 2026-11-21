package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/raven-clown/rosetta/admin"
	"github.com/raven-clown/rosetta/config"
	"github.com/raven-clown/rosetta/internal/auth"
	"github.com/raven-clown/rosetta/store"
)

const bootstrapEnv = "ROSETTA_BOOTSTRAP_ADMIN_KEY"

// bootstrap creates the first admin key when auth is on, the config has
// no keys and the store has never held one. A generated key is printed
// to stderr once; ROSETTA_BOOTSTRAP_ADMIN_KEY sets it instead, for Docker
// and CI.
func bootstrap(ctx context.Context, st *store.Store, cfg *config.Config, stderr io.Writer) error {
	if cfg.Auth.Disabled || len(cfg.Auth.APIKeys) > 0 {
		return nil
	}
	keys, err := st.ListKeys(ctx)
	if err != nil || len(keys) > 0 {
		return err
	}
	key, given := os.Getenv(bootstrapEnv), true
	if key == "" {
		key, given = admin.NewKey(), false
	}
	details, _ := json.Marshal(map[string]string{"name": "bootstrap", "role": "admin"})
	k := store.Key{ID: "k_bootstrap", Name: "bootstrap", Role: "admin", Prefix: admin.KeyPrefix(key), Hash: auth.Hash(key), CreatedBy: "bootstrap"}
	_, err = st.CreateKey(ctx, k, store.Audit{Actor: "bootstrap", Action: "api_key.create", Target: k.ID, Details: details})
	if errors.Is(err, store.ErrConflict) {
		// Another replica starting on the same database made it first.
		return nil
	}
	if err != nil {
		return fmt.Errorf("creating the first admin key: %w", err)
	}
	if given {
		_, _ = fmt.Fprintf(stderr, "rosetta: created the first admin key from %s\n", bootstrapEnv)
		return nil
	}
	_, _ = fmt.Fprintf(stderr, "rosetta: created the first admin key. It is shown only this once:\n\n    %s\n\n", key)
	return nil
}
