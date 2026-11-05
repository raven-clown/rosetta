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

func TestMetrics(t *testing.T) {
	m := New()
	m.RunDone("helper", nil, 2*time.Second, rosetta.Usage{InputTokens: 3, OutputTokens: 4})
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
		`rosetta_store_errors_total{op="append"} 1`,
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
}
