package rosetta

var version = "dev"

// Version reports the rosetta version, set at build time with -ldflags.
func Version() string {
	return version
}
