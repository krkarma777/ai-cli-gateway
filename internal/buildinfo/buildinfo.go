// Package buildinfo exposes build metadata for the executable.
package buildinfo

var (
	// Version identifies the build version.
	Version = "dev"
	// Commit identifies the source revision used for the build.
	Commit = "none"
	// Date identifies when the build was created.
	Date = "unknown"
)
