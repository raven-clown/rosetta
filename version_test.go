package rosetta

import "testing"

func TestVersion(t *testing.T) {
	tests := []struct {
		name string
		set  string
		want string
	}{
		{name: "default", set: "dev", want: "dev"},
		{name: "release build", set: "v0.1.0", want: "v0.1.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := version
			t.Cleanup(func() { version = old })
			version = tt.set
			if got := Version(); got != tt.want {
				t.Errorf("Version() = %q, want %q", got, tt.want)
			}
		})
	}
}
