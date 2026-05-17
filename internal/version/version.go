// Package version holds the build-stamp variables goreleaser sets via
// -ldflags. Defaults reflect a local (non-released) build.
package version

// These variables are set at link time by goreleaser. Keep the
// declarations exported so the linker can target them, and the
// fallback values informative so a `go install`-built binary still
// reports something meaningful.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)
