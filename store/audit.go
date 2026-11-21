package store

import (
	"context"
	"fmt"
)

// ListAudit returns audit records, newest first, and the cursor of the
// next page.
func (s *Store) ListAudit(ctx context.Context, f AuditFilter, p Page) ([]Audit, string, error) {
	where := []string{"org_id = ?"}
	args := []any{org}
	if f.Action != "" {
		where = append(where, "action = ?")
		args = append(args, f.Action)
	}
	if f.Target != "" {
		where = append(where, "target = ?")
		args = append(args, f.Target)
	}
	if !f.From.IsZero() {
		where = append(where, "at >= ?")
		args = append(args, nanos(f.From))
	}
	if !f.To.IsZero() {
		where = append(where, "at < ?")
		args = append(args, nanos(f.To))
	}
	tail, args, err := pageQuery(where, args, "at", p)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id, at, actor, action, target, details FROM audit`+tail), args...)
	if err != nil {
		return nil, "", fmt.Errorf("store: listing audit: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Audit
	for rows.Next() {
		var a Audit
		var at int64
		var details string
		if err := rows.Scan(&a.ID, &at, &a.Actor, &a.Action, &a.Target, &details); err != nil {
			return nil, "", fmt.Errorf("store: listing audit: %w", err)
		}
		a.Time, a.Details = fromNanos(at), []byte(details)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("store: listing audit: %w", err)
	}
	out, cur := next(out, p, func(a Audit) cursor { return cursor{nanos(a.Time), a.ID} })
	return out, cur, nil
}
