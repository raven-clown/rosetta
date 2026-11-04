package rosetta

import "context"

// Func turns a function into an Agent.
func Func(card Card, run func(ctx context.Context, req *Request, out Stream) error) Agent {
	return funcAgent{card: card, run: run}
}

type funcAgent struct {
	card Card
	run  func(ctx context.Context, req *Request, out Stream) error
}

func (f funcAgent) Card() Card { return f.card }

func (f funcAgent) Run(ctx context.Context, req *Request, out Stream) error {
	return f.run(ctx, req, out)
}
