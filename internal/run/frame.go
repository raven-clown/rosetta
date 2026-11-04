package run

import "github.com/raven-clown/rosetta"

type framer struct {
	ids func() string
	cur string
}

func (f *framer) frame(e rosetta.Event) rosetta.Event {
	t, ok := e.(rosetta.TextDelta)
	if !ok {
		f.cur = ""
		return e
	}
	switch {
	case t.MessageID != "":
		f.cur = t.MessageID
	case f.cur == "":
		f.cur = f.ids()
		t.MessageID = f.cur
	default:
		t.MessageID = f.cur
	}
	return t
}
