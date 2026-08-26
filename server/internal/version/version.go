// Package version exposes the build-time version of the Renfild server.
package version

// Version is injected at build time with
// -ldflags "-X github.com/t0mer/renfild/internal/version.Version=<v>".
var Version = "0.0.0-dev"
