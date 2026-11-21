package admin

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/raven-clown/rosetta"
	"github.com/raven-clown/rosetta/store"
)

type runJSON struct {
	ID              string `json:"id"`
	RunID           string `json:"run_id,omitempty"`
	Agent           string `json:"agent"`
	Protocol        string `json:"protocol,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	Caller          string `json:"caller,omitempty"`
	Status          string `json:"status"`
	Error           string `json:"error,omitempty"`
	Started         string `json:"started"`
	Ended           string `json:"ended"`
	DurationMS      int64  `json:"duration_ms"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	ReasoningTokens int64  `json:"reasoning_tokens"`
	RequestID       string `json:"request_id,omitempty"`
}

func runOut(r store.Run) runJSON {
	return runJSON{ID: r.ID, RunID: r.RunID, Agent: r.Agent, Protocol: r.Protocol, SessionID: r.SessionID, Caller: r.Caller,
		Status: r.Status, Error: r.Error, Started: ts(r.Started), Ended: ts(r.Ended),
		DurationMS: max(r.Ended.Sub(r.Started).Milliseconds(), 0), InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
		ReasoningTokens: r.ReasoningTokens, RequestID: r.RequestID}
}

func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) error {
	p, err := page(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	f := store.RunFilter{Agent: q.Get("agent"), Protocol: q.Get("protocol"), Status: q.Get("status")}
	if f.From, err = timeParam(r, "from"); err != nil {
		return err
	}
	if f.To, err = timeParam(r, "to"); err != nil {
		return err
	}
	runs, next, err := h.opts.Store.ListRuns(r.Context(), f, p)
	if err != nil {
		return err
	}
	out := make([]runJSON, len(runs))
	for i, run := range runs {
		out[i] = runOut(run)
	}
	writeJSON(w, http.StatusOK, newList(out, next))
	return nil
}

func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) error {
	run, err := h.opts.Store.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		return notFound("run " + r.PathValue("id"))
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, runOut(run))
	return nil
}

type sessionJSON struct {
	ID        string `json:"id"`
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	Caller    string `json:"caller,omitempty"`
	Messages  int    `json:"messages"`
	Created   string `json:"created"`
	Updated   string `json:"updated"`
	Expires   string `json:"expires"`
	Version   int64  `json:"version"`
}

func sessionOut(s store.Session) sessionJSON {
	return sessionJSON{ID: s.ID, Agent: s.Agent, SessionID: s.SessionID, Caller: s.Caller, Messages: s.Messages,
		Created: ts(s.Created), Updated: ts(s.Updated), Expires: ts(s.Expires), Version: s.Version}
}

type partJSON struct {
	Text string          `json:"text,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

type messageJSON struct {
	Role  rosetta.Role `json:"role"`
	Parts []partJSON   `json:"parts"`
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) error {
	p, err := page(r)
	if err != nil {
		return err
	}
	list, next, err := h.opts.Store.ListSessions(r.Context(), store.SessionFilter{Agent: r.URL.Query().Get("agent")}, p)
	if err != nil {
		return err
	}
	out := make([]sessionJSON, len(list))
	for i, s := range list {
		out[i] = sessionOut(s)
	}
	writeJSON(w, http.StatusOK, newList(out, next))
	return nil
}

func (h *Handler) getSession(w http.ResponseWriter, r *http.Request) error {
	s, msgs, err := h.opts.Store.GetSession(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		return notFound("session " + r.PathValue("id"))
	}
	if err != nil {
		return err
	}
	history := make([]messageJSON, len(msgs))
	for i, m := range msgs {
		parts := make([]partJSON, len(m.Parts))
		for j, p := range m.Parts {
			parts[j] = partJSON(p)
		}
		history[i] = messageJSON{Role: m.Role, Parts: parts}
	}
	etag(w, s.Version)
	writeJSON(w, http.StatusOK, struct {
		sessionJSON
		History []messageJSON `json:"history"`
	}{sessionOut(s), history})
	return nil
}

func (h *Handler) deleteSession(w http.ResponseWriter, r *http.Request) error {
	if err := canWrite(r); err != nil {
		return err
	}
	version, err := ifMatch(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	err = h.opts.Store.DeleteSession(r.Context(), id, version, store.Audit{Actor: actor(r), Action: "session.delete", Target: id})
	if errors.Is(err, store.ErrNotFound) {
		return notFound("session " + id)
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type auditJSON struct {
	ID      string          `json:"id"`
	Time    string          `json:"time"`
	Actor   string          `json:"actor"`
	Action  string          `json:"action"`
	Target  string          `json:"target"`
	Details json.RawMessage `json:"details,omitempty"`
}

func (h *Handler) listAudit(w http.ResponseWriter, r *http.Request) error {
	p, err := page(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	f := store.AuditFilter{Action: q.Get("action"), Target: q.Get("target")}
	if f.From, err = timeParam(r, "from"); err != nil {
		return err
	}
	if f.To, err = timeParam(r, "to"); err != nil {
		return err
	}
	list, next, err := h.opts.Store.ListAudit(r.Context(), f, p)
	if err != nil {
		return err
	}
	out := make([]auditJSON, len(list))
	for i, a := range list {
		out[i] = auditJSON{ID: a.ID, Time: ts(a.Time), Actor: a.Actor, Action: a.Action, Target: a.Target, Details: a.Details}
	}
	writeJSON(w, http.StatusOK, newList(out, next))
	return nil
}
