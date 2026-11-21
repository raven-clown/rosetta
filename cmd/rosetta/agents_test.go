package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/raven-clown/rosetta/config"
	"github.com/raven-clown/rosetta/internal/auth"
	"github.com/raven-clown/rosetta/server"
	"github.com/raven-clown/rosetta/store"
)

// TestRevokeSeenAfterAgentReload checks that an agent reload which runs
// after a revoke cannot mark that revoke as seen for the keys.
func TestRevokeSeenAfterAgentReload(t *testing.T) {
	st, err := store.Open(t.Context(), "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	k, err := st.CreateKey(ctx, store.Key{Name: "ci", Role: "member", Hash: auth.Hash("revoked-soon-key-0123456789abcdef")},
		store.Audit{Actor: "test", Action: "api_key.create", Target: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Parse(nil, nil)
	keys := server.NewKeys(nil)
	l, err := newLive(st, cfg, nil, nil, keys, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.reload(ctx); err != nil {
		t.Fatal(err)
	}
	if keys.Len() != 1 {
		t.Fatalf("keys after the first reload = %d", keys.Len())
	}

	if err := st.RevokeKey(ctx, k.ID, 0, store.Audit{Actor: "test", Action: "api_key.revoke", Target: k.ID}); err != nil {
		t.Fatal(err)
	}
	l.reloading.Lock()
	err = l.reloadAgents(ctx)
	l.reloading.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	l.check(ctx)
	if keys.Len() != 0 {
		t.Error("the revoked key still works after the agent reload moved past the revoke")
	}
}
