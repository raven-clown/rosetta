package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Check reports whether one dependency is reachable, for /readyz.
type Check struct {
	Name  string
	Check func(context.Context) error
}

// HTTPCheck passes when url answers with any status below 500. A nil
// client means http.DefaultClient.
func HTTPCheck(name, url string, client *http.Client) Check {
	if client == nil {
		client = http.DefaultClient
	}
	return Check{Name: name, Check: func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 500 {
			return fmt.Errorf("answered %s", resp.Status)
		}
		return nil
	}}
}

const checkTimeout = 2 * time.Second

func readyz(checks []Check) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		results := make(map[string]string, len(checks))
		var mu sync.Mutex
		var wg sync.WaitGroup
		healthy := true
		for _, c := range checks {
			wg.Go(func() {
				ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
				defer cancel()
				res := "ok"
				if err := c.Check(ctx); err != nil {
					res = err.Error()
				}
				mu.Lock()
				defer mu.Unlock()
				results[c.Name] = res
				healthy = healthy && res == "ok"
			})
		}
		wg.Wait()
		status, code := "ok", http.StatusOK
		if !healthy {
			status, code = "unavailable", http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "checks": results})
	}
}
