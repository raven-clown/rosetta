package httpx

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/raven-clown/rosetta"
)

func TestBearerToken(t *testing.T) {
	tests := map[string]string{
		"Bearer abc":   "abc",
		"bearer abc":   "abc",
		"BEARER  abc ": "abc",
		"Basic abc":    "",
		"abc":          "",
		"":             "",
		"Bearer ":      "",
		"Bearerabc":    "",
	}
	for header, want := range tests {
		if got := BearerToken(header); got != want {
			t.Errorf("BearerToken(%q) = %q, want %q", header, got, want)
		}
	}
}

func header(kv ...string) func(string) string {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h.Get
}

func TestCaller(t *testing.T) {
	c := Caller(header(
		"Authorization", "bearer tok",
		"X-Request-ID", "req-1",
		"traceparent", "00-abc-def-01",
		"tracestate", "k=v",
		"X-Project", "proj",
		"X-Rosetta-Caller", "key:ci",
	))
	want := rosetta.Caller{
		ID:        "key:ci",
		Token:     "tok",
		RequestID: "req-1",
		Project:   "proj",
		Trace:     http.Header{"Traceparent": {"00-abc-def-01"}, "Tracestate": {"k=v"}},
	}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("Caller = %+v, want %+v", c, want)
	}

	c = Caller(header("Authorization", "Basic abc"))
	if c.Token != "" || c.Trace != nil {
		t.Errorf("Caller = %+v", c)
	}
	if len(c.RequestID) != 32 {
		t.Errorf("generated request ID %q, want 32 hex chars", c.RequestID)
	}
	if other := Caller(header()); other.RequestID == c.RequestID {
		t.Error("request IDs repeat")
	}
}

func TestOutbound(t *testing.T) {
	full := rosetta.Caller{
		Token:     "tok",
		RequestID: "req-1",
		Project:   "proj",
		Trace:     http.Header{"Traceparent": {"tp"}, "Tracestate": {"ts"}},
	}
	tests := map[string]struct {
		caller   rosetta.Caller
		fallback string
		want     http.Header
	}{
		"caller wins": {full, "key", http.Header{
			"Authorization": {"Bearer tok"},
			"X-Request-Id":  {"req-1"},
			"X-Project":     {"proj"},
			"Traceparent":   {"tp"},
			"Tracestate":    {"ts"},
		}},
		"fallback": {rosetta.Caller{RequestID: "r"}, "key", http.Header{
			"Authorization": {"Bearer key"},
			"X-Request-Id":  {"r"},
		}},
		"nothing": {rosetta.Caller{}, "", http.Header{}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := Outbound(tt.caller, tt.fallback); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Outbound = %v, want %v", got, tt.want)
			}
		})
	}
}
