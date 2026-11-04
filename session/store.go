// Package session keeps short conversation history per agent and session.
package session

import (
	"context"

	"github.com/raven-clown/rosetta"
)

// Store keeps conversation history keyed by agent name and session ID.
type Store interface {
	History(ctx context.Context, agent, sessionID string) ([]rosetta.Message, error)
	Append(ctx context.Context, agent, sessionID string, msgs ...rosetta.Message) error
	Delete(ctx context.Context, agent, sessionID string) error
}
