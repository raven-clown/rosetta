// Package golden compares test output with files in testdata.
package golden

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files with the current output")

// Assert compares got with testdata/<name>.golden. With -update it writes
// the file instead.
func Assert(t testing.TB, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("golden: %v", err)
			return
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // the path is built from a name the test chooses
	if err != nil {
		t.Fatalf("golden: %v (run go test with -update to create it)", err)
		return
	}
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	got = bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n"))
	if bytes.Equal(want, got) {
		return
	}
	t.Errorf("golden %s differs (run go test with -update to accept):\n%s", path, diff(string(want), string(got)))
}

func diff(want, got string) string {
	w := strings.Split(want, "\n")
	g := strings.Split(got, "\n")
	var b strings.Builder
	for i := range max(len(w), len(g)) {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			fmt.Fprintf(&b, "line %d\n-want %s\n+got %s\n", i+1, wl, gl)
		}
	}
	return b.String()
}
