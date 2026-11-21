package admin

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/raven-clown/rosetta/internal/auth"
	"github.com/raven-clown/rosetta/store"
)

type keyJSON struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Role          string `json:"role"`
	Prefix        string `json:"prefix,omitempty"`
	Version       int64  `json:"version"`
	ManagedByFile bool   `json:"managed_by_file,omitempty"`
	Created       string `json:"created,omitempty"`
	CreatedBy     string `json:"created_by,omitempty"`
	Revoked       string `json:"revoked,omitempty"`
}

const fileKeyPrefix = "file:"

func storedKeyJSON(k store.Key) keyJSON {
	return keyJSON{ID: k.ID, Name: k.Name, Role: k.Role, Prefix: k.Prefix, Version: k.Version,
		Created: ts(k.Created), CreatedBy: k.CreatedBy, Revoked: ts(k.Revoked)}
}

func sortKey(k keyJSON) string { return k.Name + "\x00" + k.ID }

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) error {
	p, err := page(r)
	if err != nil {
		return err
	}
	stored, err := h.opts.Store.ListKeys(r.Context())
	if err != nil {
		return err
	}
	var all []keyJSON
	for _, k := range h.opts.Keys {
		all = append(all, keyJSON{ID: fileKeyPrefix + k.Name, Name: k.Name, Role: k.Role, Version: 1, ManagedByFile: true})
	}
	for _, k := range stored {
		all = append(all, storedKeyJSON(k))
	}
	slices.SortFunc(all, func(a, b keyJSON) int { return strings.Compare(sortKey(a), sortKey(b)) })
	items, next, err := pageByName(all, p, sortKey)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, newList(items, next))
	return nil
}

var keyRoles = map[string]bool{"admin": true, "member": true, "viewer": true}

// NewKey makes a key: rsk_ and 32 random bytes in base64url.
func NewKey() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "rsk_" + base64.RawURLEncoding.EncodeToString(b)
}

// KeyPrefix is the part of a key shown in lists.
func KeyPrefix(key string) string { return key[:min(len(key), 8)] }

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) error {
	if err := isAdmin(r); err != nil {
		return err
	}
	var spec struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if err := decode(r, &spec); err != nil {
		return err
	}
	switch {
	case spec.Name == "" || len(spec.Name) > 64:
		return invalid("name", "name must be 1 to 64 characters")
	case !keyRoles[spec.Role]:
		return invalid("role", "role must be admin, member or viewer")
	}
	key := NewKey()
	details, _ := json.Marshal(map[string]string{"name": spec.Name, "role": spec.Role})
	k := store.Key{Name: spec.Name, Role: spec.Role, Prefix: KeyPrefix(key), Hash: auth.Hash(key), CreatedBy: actor(r)}
	k.ID = newKeyID()
	k, err := h.opts.Store.CreateKey(r.Context(), k, store.Audit{Actor: actor(r), Action: "api_key.create", Target: k.ID, Details: details})
	if err != nil {
		return err
	}
	h.opts.Reload(r.Context())
	etag(w, k.Version)
	writeJSON(w, http.StatusCreated, struct {
		keyJSON
		Key string `json:"key"`
	}{storedKeyJSON(k), key})
	return nil
}

func newKeyID() string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	return "k_" + base64.RawURLEncoding.EncodeToString(b)
}

func (h *Handler) revokeKey(w http.ResponseWriter, r *http.Request) error {
	if err := isAdmin(r); err != nil {
		return err
	}
	id := r.PathValue("id")
	if strings.HasPrefix(id, fileKeyPrefix) {
		return &apiError{http.StatusConflict, "read_only", "key " + strings.TrimPrefix(id, fileKeyPrefix) + " comes from the config file and cannot be revoked here", "id"}
	}
	version, err := ifMatch(r)
	if err != nil {
		return err
	}
	err = h.opts.Store.RevokeKey(r.Context(), id, version, store.Audit{Actor: actor(r), Action: "api_key.revoke", Target: id})
	if errors.Is(err, store.ErrNotFound) {
		return notFound("key " + id)
	}
	if err != nil {
		return err
	}
	h.opts.Reload(r.Context())
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func encodeNameCursor(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func decodeNameCursor(s string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) == 0 {
		return "", store.ErrBadCursor
	}
	return string(b), nil
}
