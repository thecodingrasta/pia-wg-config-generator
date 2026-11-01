package main

import (
	"fmt"
	"runtime/debug"
)

// Populated at link time by the release workflow, for example:
//
//	go build -ldflags "-X main.version=v1.2.3 -X main.commit=<sha> -X main.date=<RFC3339>"
//
// A plain `go build` leaves the defaults, in which case buildVersionString
// falls back to the module version Go embeds for `go install ...@vX.Y.Z`.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// buildVersionString returns the human-readable version reported by --version.
func buildVersionString() string {
	resolved := version
	if resolved == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			resolved = info.Main.Version
		}
	}
	return fmt.Sprintf("%s (commit %s, built %s)", resolved, commit, date)
}
