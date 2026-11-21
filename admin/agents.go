package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/raven-clown/rosetta/config"
	"github.com/raven-clown/rosetta/registry"
	"github.com/raven-clown/rosetta/store"
)

type agentJSON struct {
	config.Agent
	Version       int64  `json:"version"`
	Source        string `json:"source"`
	ManagedByFile bool   `json:"managed_by_file,omitempty"`
	ManagedByCode bool   `json:"managed_by_code,omitempty"`
	Created       string `json:"created,omitempty"`
	Updated       string `json:"updated,omitempty"`
	UpdatedBy     string `json:"updated_by,omitempty"`
}

func fixedJSON(f FixedAgent) agentJSON {
	return agentJSON{Agent: f.Spec, Version: 1, Source: f.Source,
		ManagedByFile: f.Source == SourceConfig, ManagedByCode: f.Source == SourceCode}
}

func storedJSON(a store.Agent) (agentJSON, error) {
	var spec config.Agent
	if err := json.Unmarshal(a.Spec, &spec); err != nil {
		return agentJSON{}, err
	}
	spec.Name = a.Name
	return agentJSON{Agent: spec, Version: a.Version, Source: SourceAPI,
		Created: ts(a.Created), Updated: ts(a.Updated), UpdatedBy: a.UpdatedBy}, nil
}

func (h *Handler) fixed(name string) (FixedAgent, bool) {
	for _, f := range h.opts.Fixed() {
		if f.Spec.Name == name {
			return f, true
		}
	}
	return FixedAgent{}, false
}

func readOnly(f FixedAgent) error {
	where := "the config file"
	if f.Source == SourceCode {
		where = "code"
	}
	return &apiError{http.StatusConflict, "read_only", "agent " + f.Spec.Name + " comes from " + where + " and cannot be changed here", "name"}
}

// allAgents lists fixed agents and stored ones by name. A stored agent
// shadowed by a fixed one is left out, as the server does.
func (h *Handler) allAgents(ctx context.Context) ([]agentJSON, error) {
	var out []agentJSON
	seen := map[string]bool{}
	for _, f := range h.opts.Fixed() {
		out = append(out, fixedJSON(f))
		seen[f.Spec.Name] = true
	}
	stored, err := h.opts.Store.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range stored {
		if seen[a.Name] {
			continue
		}
		j, err := storedJSON(a)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	slices.SortFunc(out, func(a, b agentJSON) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (h *Handler) listAgents(w http.ResponseWriter, r *http.Request) error {
	p, err := page(r)
	if err != nil {
		return err
	}
	all, err := h.allAgents(r.Context())
	if err != nil {
		return err
	}
	items, next, err := pageByName(all, p, func(a agentJSON) string { return a.Name })
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, newList(items, next))
	return nil
}

func (h *Handler) getAgent(w http.ResponseWriter, r *http.Request) error {
	name := r.PathValue("name")
	if f, ok := h.fixed(name); ok {
		etag(w, 1)
		writeJSON(w, http.StatusOK, fixedJSON(f))
		return nil
	}
	a, err := h.opts.Store.GetAgent(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		return notFound("agent " + name)
	}
	if err != nil {
		return err
	}
	j, err := storedJSON(a)
	if err != nil {
		return err
	}
	etag(w, j.Version)
	writeJSON(w, http.StatusOK, j)
	return nil
}

// a2aNeedsAdmin keeps remote agents to admin keys: they make the server
// call out to a URL, and with forward_token they pass every caller's
// token there.
func a2aNeedsAdmin(r *http.Request, kinds ...string) error {
	if slices.Contains(kinds, "a2a") && principal(r).Role != "admin" {
		return forbidden("only an admin key can create or change a2a agents, since they call another server")
	}
	return nil
}

func (h *Handler) checkSpec(spec config.Agent) error {
	if !registry.ValidName(spec.Name) {
		return invalid("name", "name must be lowercase letters, digits, - or _, up to 64 characters")
	}
	return h.opts.Validate(spec)
}

func (h *Handler) createAgent(w http.ResponseWriter, r *http.Request) error {
	if err := canWrite(r); err != nil {
		return err
	}
	var spec config.Agent
	if err := decode(r, &spec); err != nil {
		return err
	}
	if err := h.checkSpec(spec); err != nil {
		return err
	}
	if err := a2aNeedsAdmin(r, spec.Kind); err != nil {
		return err
	}
	if f, ok := h.fixed(spec.Name); ok {
		return &apiError{http.StatusConflict, "conflict", "agent " + spec.Name + " already comes from " + f.Source, "name"}
	}
	raw, _ := json.Marshal(spec)
	details, _ := json.Marshal(map[string]string{"kind": spec.Kind})
	a, err := h.opts.Store.CreateAgent(r.Context(), store.Agent{Name: spec.Name, Spec: raw, UpdatedBy: actor(r)},
		store.Audit{Actor: actor(r), Action: "agent.create", Target: spec.Name, Details: details})
	if err != nil {
		return err
	}
	h.opts.Reload(r.Context())
	j, err := storedJSON(a)
	if err != nil {
		return err
	}
	etag(w, j.Version)
	writeJSON(w, http.StatusCreated, j)
	return nil
}

func (h *Handler) updateAgent(w http.ResponseWriter, r *http.Request) error {
	if err := canWrite(r); err != nil {
		return err
	}
	name := r.PathValue("name")
	if f, ok := h.fixed(name); ok {
		return readOnly(f)
	}
	version, err := ifMatch(r)
	if err != nil {
		return err
	}
	var patch map[string]json.RawMessage
	if err := decode(r, &patch); err != nil {
		return err
	}
	cur, err := h.opts.Store.GetAgent(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		return notFound("agent " + name)
	}
	if err != nil {
		return err
	}
	merged := map[string]json.RawMessage{}
	if err := json.Unmarshal(cur.Spec, &merged); err != nil {
		return err
	}
	var fields []string
	for k, v := range patch {
		merged[k] = v
		fields = append(fields, k)
	}
	slices.Sort(fields)
	raw, _ := json.Marshal(merged)
	var spec config.Agent
	if err := decodeFrom(bytes.NewReader(raw), &spec); err != nil {
		return err
	}
	if spec.Name != name {
		return invalid("name", "an agent cannot be renamed; create a new one instead")
	}
	if err := h.checkSpec(spec); err != nil {
		return err
	}
	var before config.Agent
	_ = json.Unmarshal(cur.Spec, &before)
	if err := a2aNeedsAdmin(r, before.Kind, spec.Kind); err != nil {
		return err
	}
	raw, _ = json.Marshal(spec)
	details, _ := json.Marshal(map[string][]string{"fields": fields})
	a, err := h.opts.Store.UpdateAgent(r.Context(), store.Agent{Name: name, Spec: raw, UpdatedBy: actor(r)}, version,
		store.Audit{Actor: actor(r), Action: "agent.update", Target: name, Details: details})
	if errors.Is(err, store.ErrNotFound) {
		return notFound("agent " + name)
	}
	if err != nil {
		return err
	}
	h.opts.Reload(r.Context())
	j, err := storedJSON(a)
	if err != nil {
		return err
	}
	etag(w, j.Version)
	writeJSON(w, http.StatusOK, j)
	return nil
}

func (h *Handler) deleteAgent(w http.ResponseWriter, r *http.Request) error {
	if err := canWrite(r); err != nil {
		return err
	}
	name := r.PathValue("name")
	if f, ok := h.fixed(name); ok {
		return readOnly(f)
	}
	version, err := ifMatch(r)
	if err != nil {
		return err
	}
	if cur, err := h.opts.Store.GetAgent(r.Context(), name); err == nil {
		var spec config.Agent
		_ = json.Unmarshal(cur.Spec, &spec)
		if err := a2aNeedsAdmin(r, spec.Kind); err != nil {
			return err
		}
	}
	err = h.opts.Store.DeleteAgent(r.Context(), name, version, store.Audit{Actor: actor(r), Action: "agent.delete", Target: name})
	if errors.Is(err, store.ErrNotFound) {
		return notFound("agent " + name)
	}
	if err != nil {
		return err
	}
	h.opts.Reload(r.Context())
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// pageByName pages a list already sorted by key. The cursor is the last
// key of the previous page.
func pageByName[T any](all []T, p store.Page, key func(T) string) ([]T, string, error) {
	start := 0
	if p.Cursor != "" {
		after, err := decodeNameCursor(p.Cursor)
		if err != nil {
			return nil, "", err
		}
		start = len(all)
		for i, v := range all {
			if key(v) > after {
				start = i
				break
			}
		}
	}
	limit := p.Limit
	if limit == 0 {
		limit = store.DefaultLimit
	}
	end := min(start+limit, len(all))
	next := ""
	if end < len(all) {
		next = encodeNameCursor(key(all[end-1]))
	}
	return all[start:end], next, nil
}
