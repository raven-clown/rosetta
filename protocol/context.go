package protocol

import "context"

type nameKey struct{}

// WithName tags ctx with the protocol a run came in on, for run records.
func WithName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, nameKey{}, name)
}

// NameFrom returns the protocol set by WithName, or "".
func NameFrom(ctx context.Context) string {
	s, _ := ctx.Value(nameKey{}).(string)
	return s
}
