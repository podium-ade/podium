// Package version carries the build identity of every Podium binary.
// Version and Commit are overwritten at link time by the Makefile / GoReleaser.
package version

var (
	// Version is the release version, or "dev" for an unstamped build.
	Version = "dev"
	// Commit is the short git commit, or "none" for an unstamped build.
	Commit = "none"
)

// String renders the build identity as "<version> (<commit>)".
func String() string {
	return Version + " (" + Commit + ")"
}
