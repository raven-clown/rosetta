package store

import "context"

// ForceVersion records a migration the binary does not have, as a newer
// binary would.
func ForceVersion(ctx context.Context, s *Store, v int) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`), v)
	return err
}
