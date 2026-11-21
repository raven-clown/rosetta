package server

import (
	"net/http"
	"testing"

	"github.com/raven-clown/rosetta/agents/echo"
	"github.com/raven-clown/rosetta/store"
)

func TestRunsRecorded(t *testing.T) {
	st, err := store.Open(t.Context(), "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h := New(Options{Store: st, Runs: st}, echo.New("echo")).Handler()
	if rec := do(t, h, http.MethodPost, "/agui/echo", aguiBody); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	runs, _, err := st.ListRuns(t.Context(), store.RunFilter{}, store.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Agent != "echo" || runs[0].Protocol != "agui" || runs[0].Status != store.RunCompleted {
		t.Errorf("runs = %+v", runs)
	}
}
