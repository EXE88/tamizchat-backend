// Package version carries build metadata, overridable with -ldflags at build time.
package version

// Version is the released backend version.
var Version = "0.1.0-dev"

// Commit is the git revision the binary was built from.
var Commit = "unknown"
