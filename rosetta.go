package rosetta

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Agent is the one interface an agent implements. Run ends the run when it
// returns: nil means completed, an error means failed.
type Agent interface {
	Card() Card
	Run(ctx context.Context, req *Request, out Stream) error
}

// Card describes an agent to clients.
type Card struct {
	Name        string
	Description string
	Version     string
	Skills      []Skill
}

// Skill is one thing an agent can do, as advertised on its card.
type Skill struct {
	ID          string
	Name        string
	Description string
	Tags        []string
	Examples    []string
}

// Request is one run of an agent. History holds earlier turns of the
// session, filled by rosetta when the protocol does not send them.
type Request struct {
	SessionID string
	RunID     string
	Input     []Part
	History   []Message
	State     json.RawMessage
	Caller    Caller
}

// Text returns the text parts of the input joined with newlines.
func (r *Request) Text() string {
	var texts []string
	for _, p := range r.Input {
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// Caller carries who called and the headers to forward upstream. ID names
// a caller that rosetta authenticated itself, such as "key:" plus a hash of
// the key, whose
// credential is not forwarded. Token is the bearer token to forward.
type Caller struct {
	ID        string
	Token     string
	RequestID string
	Project   string
	Trace     http.Header
}

// Role is who wrote a message.
type Role string

// Roles a message can have.
const (
	RoleUser   Role = "user"
	RoleAgent  Role = "agent"
	RoleSystem Role = "system"
)

// Message is one turn in a session.
type Message struct {
	Role  Role
	Parts []Part
}

// Part is a piece of a message: text or JSON data.
type Part struct {
	Text string
	Data json.RawMessage
}

// ErrRunEnded is returned when an agent sends after its run has ended.
var ErrRunEnded = errors.New("rosetta: run has ended")
