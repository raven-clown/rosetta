// Package agui serves agents over AG-UI: a POST with a RunAgentInput body,
// answered with a stream of server-sent events.
package agui

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/protocol"
)

const maxBody = 4 << 20

// Options configures the adapter.
type Options struct {
	Path string
	Now  func() time.Time
}

// Adapter serves AG-UI.
type Adapter struct {
	path string
	now  func() time.Time
}

// New returns an AG-UI adapter. Path defaults to /agui.
func New(opts Options) *Adapter {
	a := &Adapter{path: opts.Path, now: opts.Now}
	if a.path == "" {
		a.path = "/agui"
	}
	if a.now == nil {
		a.now = time.Now
	}
	return a
}

// Name returns "agui".
func (a *Adapter) Name() string { return "agui" }

// Mount registers POST {path}/{agent} on mux.
func (a *Adapter) Mount(mux *http.ServeMux, agents protocol.Registry, run protocol.Runner) error {
	mux.HandleFunc("POST "+a.path+"/{agent}", func(w http.ResponseWriter, r *http.Request) {
		agent, ok := agents.Get(r.PathValue("agent"))
		if !ok {
			http.Error(w, "unknown agent", http.StatusNotFound)
			return
		}
		var in types.RunAgentInput
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&in); err != nil {
			http.Error(w, "invalid RunAgentInput: "+err.Error(), http.StatusBadRequest)
			return
		}
		req := request(in)
		req.Caller = rosetta.Caller{Token: bearer(r), RequestID: r.Header.Get("X-Request-ID")}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		s := newStream(r.Context(), w, a.now, in.ThreadID, req.RunID)
		if err := s.start(); err != nil {
			return
		}
		err := run.Run(r.Context(), agent, req, s.send)
		if r.Context().Err() != nil {
			return
		}
		_ = s.finish(err)
	})
	return nil
}

func bearer(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(prefix) && h[:len(prefix)] == prefix {
		return h[len(prefix):]
	}
	return ""
}

func request(in types.RunAgentInput) *rosetta.Request {
	req := &rosetta.Request{SessionID: in.ThreadID, RunID: in.RunID, History: []rosetta.Message{}}
	if in.State != nil {
		if b, err := json.Marshal(in.State); err == nil {
			req.State = b
		}
	}
	msgs := in.Messages
	if n := len(msgs); n > 0 && msgs[n-1].Role == types.RoleUser {
		req.Input = parts(msgs[n-1])
		msgs = msgs[:n-1]
	}
	for _, m := range msgs {
		role, ok := roles[m.Role]
		if !ok {
			continue
		}
		req.History = append(req.History, rosetta.Message{Role: role, Parts: parts(m)})
	}
	return req
}

var roles = map[types.Role]rosetta.Role{
	types.RoleUser:      rosetta.RoleUser,
	types.RoleAssistant: rosetta.RoleAgent,
	types.RoleSystem:    rosetta.RoleSystem,
	types.RoleDeveloper: rosetta.RoleSystem,
}

func parts(m types.Message) []rosetta.Part {
	if s, ok := m.ContentString(); ok {
		return []rosetta.Part{{Text: s}}
	}
	var out []rosetta.Part
	if contents, ok := m.ContentInputContents(); ok {
		for _, c := range contents {
			if c.Type == "text" && c.Text != "" {
				out = append(out, rosetta.Part{Text: c.Text})
			}
		}
	}
	return out
}
