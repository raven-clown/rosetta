package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/raven-clown/rosetta/agents/echo"
)

func TestReadyz(t *testing.T) {
	ok := Check{Name: "transponder", Check: func(context.Context) error { return nil }}
	bad := Check{Name: "airlock", Check: func(context.Context) error { return errors.New("connection refused") }}
	tests := map[string]struct {
		checks []Check
		code   int
		body   map[string]any
	}{
		"no checks": {nil, http.StatusOK, map[string]any{"status": "ok", "checks": map[string]any{}}},
		"all ok":    {[]Check{ok}, http.StatusOK, map[string]any{"status": "ok", "checks": map[string]any{"transponder": "ok"}}},
		"one fails": {[]Check{ok, bad}, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable",
			"checks": map[string]any{"transponder": "ok", "airlock": "connection refused"},
		}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			h := New(Options{Checks: tt.checks}, echo.New("echo")).Handler()
			rec := do(t, h, http.MethodGet, "/readyz", "")
			if rec.Code != tt.code {
				t.Errorf("status %d, want %d", rec.Code, tt.code)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(body, tt.body) {
				t.Errorf("body = %v, want %v", body, tt.body)
			}
		})
	}
}

func TestMetricsEndpoint(t *testing.T) {
	h := New(Options{}, echo.New("echo")).Handler()
	do(t, h, http.MethodPost, "/agui/echo", aguiBody)
	rec := do(t, h, http.MethodGet, "/metrics", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `rosetta_runs_total{agent="echo",status="completed"} 1`) {
		t.Errorf("status %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestHTTPCheck(t *testing.T) {
	status := func(code int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
	}
	closed := status(200)
	closed.Close()
	tests := map[string]struct {
		srv  *httptest.Server
		fail bool
	}{
		"200":    {status(200), false},
		"404":    {status(404), false},
		"500":    {status(500), true},
		"closed": {closed, true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			defer tt.srv.Close()
			c := HTTPCheck("up", tt.srv.URL, nil)
			if c.Name != "up" {
				t.Errorf("name = %q", c.Name)
			}
			if err := c.Check(t.Context()); (err != nil) != tt.fail {
				t.Errorf("err = %v, want failure %v", err, tt.fail)
			}
		})
	}
}
