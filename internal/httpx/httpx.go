// Package httpx holds HTTP helpers shared by the protocol adapters and the
// upstream clients.
package httpx

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/raven-clown/rosetta"
)

// BearerToken returns the token from an Authorization header value, or ""
// when the scheme is not Bearer.
func BearerToken(header string) string {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

var traceHeaders = []string{"Traceparent", "Tracestate"}

// Caller reads the caller's identity and the headers to forward upstream.
// A missing X-Request-ID is generated.
func Caller(get func(string) string) rosetta.Caller {
	c := rosetta.Caller{
		Token:     BearerToken(get("Authorization")),
		RequestID: get("X-Request-ID"),
		Project:   get("X-Project"),
	}
	if c.RequestID == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		c.RequestID = hex.EncodeToString(b)
	}
	for _, k := range traceHeaders {
		if v := get(k); v != "" {
			if c.Trace == nil {
				c.Trace = http.Header{}
			}
			c.Trace.Set(k, v)
		}
	}
	return c
}

// Outbound returns the headers for a call to transponder or airlock. The
// caller's token wins, fallbackKey is used when there is none.
func Outbound(c rosetta.Caller, fallbackKey string) http.Header {
	h := http.Header{}
	switch {
	case c.Token != "":
		h.Set("Authorization", "Bearer "+c.Token)
	case fallbackKey != "":
		h.Set("Authorization", "Bearer "+fallbackKey)
	}
	if c.RequestID != "" {
		h.Set("X-Request-ID", c.RequestID)
	}
	if c.Project != "" {
		h.Set("X-Project", c.Project)
	}
	for _, k := range traceHeaders {
		if v := c.Trace.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	return h
}
