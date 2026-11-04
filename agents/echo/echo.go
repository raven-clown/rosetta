// Package echo is an agent that repeats its input, for demos and tests.
package echo

import (
	"context"
	"errors"

	"github.com/raven-clown/rosetta"
)

var errNoText = errors.New("echo: no text in input")

// New returns an echo agent with the given name.
func New(name string) rosetta.Agent {
	card := rosetta.Card{Name: name, Description: "Repeats what you send."}
	return rosetta.Func(card, func(_ context.Context, req *rosetta.Request, out rosetta.Stream) error {
		text := req.Text()
		if text == "" {
			return errNoText
		}
		if err := out.Send(rosetta.Step{Name: "echo"}); err != nil {
			return err
		}
		if err := out.Text(text); err != nil {
			return err
		}
		return out.Send(rosetta.Step{Name: "echo", Finished: true})
	})
}
