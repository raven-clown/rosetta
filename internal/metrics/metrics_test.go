package metrics

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raven-clown/rosetta"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func TestRequestMetrics(t *testing.T) {
	m := New()
	mux := http.NewServeMux()
	inflight := make(chan string, 1)
	mux.HandleFunc("POST /agui/{agent}", func(w http.ResponseWriter, _ *http.Request) {
		inflight <- scrape(t, m)
		w.WriteHeader(http.StatusAccepted)
		w.(http.Flusher).Flush()
	})
	h := m.Middleware(mux, mux)
	for _, path := range []string{"/agui/one", "/agui/two"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, nil))
		if during := <-inflight; !strings.Contains(during, "rosetta_inflight_requests 1") {
			t.Errorf("in flight during a request:\n%s", during)
		}
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nowhere/42", nil))
	body := scrape(t, m)
	for _, want := range []string{
		`rosetta_requests_total{code="202",route="POST /agui/{agent}"} 2`,
		`rosetta_requests_total{code="404",route="other"} 1`,
		`rosetta_request_duration_seconds_count{route="POST /agui/{agent}"} 2`,
		`rosetta_inflight_requests 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "rosetta_") && (strings.Contains(line, "/agui/one") || strings.Contains(line, "/nowhere")) {
			t.Errorf("a raw path became a label: %s", line)
		}
	}
}

func TestMetrics(t *testing.T) {
	m := New()
	m.RunDone("helper", nil, 2*time.Second, rosetta.Usage{InputTokens: 3, OutputTokens: 4, ReasoningTokens: 5})
	m.RunDone("helper", errors.New("nope"), time.Second, rosetta.Usage{})
	m.RunDone("helper", context.Canceled, time.Second, rosetta.Usage{})
	m.StoreError("append")

	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	for _, want := range []string{
		`rosetta_runs_total{agent="helper",status="completed"} 1`,
		`rosetta_runs_total{agent="helper",status="failed"} 1`,
		`rosetta_runs_total{agent="helper",status="cancelled"} 1`,
		`rosetta_run_duration_seconds_count{agent="helper"} 3`,
		`rosetta_tokens_total{agent="helper",direction="input"} 3`,
		`rosetta_tokens_total{agent="helper",direction="output"} 4`,
		`rosetta_tokens_total{agent="helper",direction="reasoning"} 5`,
		`rosetta_store_errors_total{op="append"} 1`,
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
}
