// Package buildinfo holds build-time metadata for the pp binary.
//
// The three vars below are populated by `-ldflags "-X ..."` at link
// time. See Makefile and .goreleaser.yml for the injection points.
// When built with plain `go build`, the defaults ("dev" / "unknown")
// keep the binary functional — `pp version` will report "dev", which
// is a useful signal that the binary was built from a source checkout
// rather than a tagged release.
//
// The agent forwards Version (with any leading "v" stripped) to the
// master in the RegisterRequest so the master's node page can show
// the real version of the running agent.
package buildinfo

var (
	// Version is the release tag (e.g. "v0.1.0" or "dev"). GoReleaser
	// injects {{ .Version }} here, which retains the leading "v".
	Version = "dev"

	// Commit is the git SHA the binary was built from.
	Commit = "unknown"

	// BuildTime is the ISO-8601 UTC timestamp of the build.
	BuildTime = "unknown"
)
