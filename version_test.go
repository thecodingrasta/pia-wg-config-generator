package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestBuildVersionStringUsesLinkTimeValues(t *testing.T) {
	origVersion, origCommit, origDate := version, commit, date
	t.Cleanup(func() { version, commit, date = origVersion, origCommit, origDate })

	version, commit, date = "v9.9.9", "abc1234", "2026-01-02T03:04:05Z"

	got := buildVersionString()
	want := "v9.9.9 (commit abc1234, built 2026-01-02T03:04:05Z)"
	if got != want {
		t.Fatalf("buildVersionString() = %q, want %q", got, want)
	}
}

func TestVersionFlagPrintsVersion(t *testing.T) {
	app := buildApp()
	var out bytes.Buffer
	app.Writer = &out

	if err := app.Run([]string{"pia-wg-config", "--version"}); err != nil {
		t.Fatalf("--version returned error: %v", err)
	}
	if !strings.Contains(out.String(), buildVersionString()) {
		t.Fatalf("--version output %q does not contain %q", out.String(), buildVersionString())
	}
}

// The global --verbose flag owns the -v alias; the version flag must not
// steal it or the CLI panics on a duplicate flag definition.
func TestVerboseKeepsShortAlias(t *testing.T) {
	app := buildApp()
	for _, f := range app.Flags {
		names := f.Names()
		if names[0] != "verbose" {
			continue
		}
		for _, n := range names {
			if n == "v" {
				return
			}
		}
		t.Fatalf("--verbose lost its -v alias: %v", names)
	}
	t.Fatal("global --verbose flag not found")
}
