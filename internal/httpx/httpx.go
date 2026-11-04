// Package httpx holds HTTP helpers shared by the protocol adapters.
package httpx

import "strings"

// BearerToken returns the token from an Authorization header value, or ""
// when the scheme is not Bearer.
func BearerToken(header string) string {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}
