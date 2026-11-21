package store

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// Limits on a page.
const (
	DefaultLimit = 50
	MaxLimit     = 500
)

// ErrBadCursor is returned for a cursor this store did not make.
var ErrBadCursor = fmt.Errorf("store: bad cursor")

type cursor struct {
	at int64
	id string
}

func (c cursor) String() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.at, 10) + ":" + c.id))
}

func parseCursor(s string) (cursor, bool, error) {
	if s == "" {
		return cursor{}, false, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, false, ErrBadCursor
	}
	at, id, ok := strings.Cut(string(b), ":")
	n, err := strconv.ParseInt(at, 10, 64)
	if !ok || err != nil || id == "" {
		return cursor{}, false, ErrBadCursor
	}
	return cursor{at: n, id: id}, true, nil
}

func (p Page) limit() int {
	switch {
	case p.Limit <= 0:
		return DefaultLimit
	case p.Limit > MaxLimit:
		return MaxLimit
	}
	return p.Limit
}

// pageQuery adds the cursor condition and order for a list sorted newest
// first by column col, then id.
func pageQuery(where []string, args []any, col string, p Page) (string, []any, error) {
	c, ok, err := parseCursor(p.Cursor)
	if err != nil {
		return "", nil, err
	}
	if ok {
		where = append(where, "("+col+" < ? OR ("+col+" = ? AND id < ?))")
		args = append(args, c.at, c.at, c.id)
	}
	q := " WHERE " + strings.Join(where, " AND ") + " ORDER BY " + col + " DESC, id DESC LIMIT ?"
	return q, append(args, p.limit()+1), nil
}

// next trims a result of limit+1 rows and returns the cursor of the last
// row kept, or "" on the last page.
func next[T any](rows []T, p Page, key func(T) cursor) ([]T, string) {
	if len(rows) <= p.limit() {
		return rows, ""
	}
	rows = rows[:p.limit()]
	return rows, key(rows[len(rows)-1]).String()
}
