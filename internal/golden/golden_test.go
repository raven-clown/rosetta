package golden

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeTB struct {
	testing.TB
	failures []string
}

func (f *fakeTB) Helper() {}

func (f *fakeTB) Errorf(format string, args ...any) {
	f.failures = append(f.failures, fmt.Sprintf(format, args...))
}

func (f *fakeTB) Fatalf(format string, args ...any) {
	f.failures = append(f.failures, fmt.Sprintf(format, args...))
}

func inTempDir(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll("testdata", 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join("testdata", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAssert(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		got      string
		wantFail []string
	}{
		{"equal", "a\nb\n", "a\nb\n", nil},
		{"different", "a\nb\n", "a\nc\n", []string{"-want b", "+got c"}},
		{"crlf file matches lf output", "a\r\nb\r\n", "a\nb\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inTempDir(t, map[string]string{"x.golden": tt.file})
			f := &fakeTB{TB: t}
			Assert(f, "x", []byte(tt.got))
			msg := strings.Join(f.failures, "\n")
			if len(tt.wantFail) == 0 && msg != "" {
				t.Fatalf("unexpected failure: %s", msg)
			}
			for _, w := range tt.wantFail {
				if !strings.Contains(msg, w) {
					t.Errorf("failure %q does not contain %q", msg, w)
				}
			}
		})
	}
}

func TestAssertMissingFile(t *testing.T) {
	inTempDir(t, nil)
	f := &fakeTB{TB: t}
	Assert(f, "missing", []byte("x"))
	if len(f.failures) != 1 || !strings.Contains(f.failures[0], "-update") {
		t.Errorf("failures = %v", f.failures)
	}
}

func TestAssertUpdate(t *testing.T) {
	inTempDir(t, nil)
	*update = true
	t.Cleanup(func() { *update = false })
	f := &fakeTB{TB: t}
	Assert(f, "new", []byte("hello\n"))
	if len(f.failures) != 0 {
		t.Fatalf("failures = %v", f.failures)
	}
	b, err := os.ReadFile(filepath.Join("testdata", "new.golden"))
	if err != nil || string(b) != "hello\n" {
		t.Errorf("file = %q, %v", b, err)
	}
}
