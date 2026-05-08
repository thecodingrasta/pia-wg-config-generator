package main

import (
	"bytes"
	"strings"
	"testing"

	cli "github.com/urfave/cli/v2"
)

func TestBuildApp_RootGenerateFlagsAreAccepted(t *testing.T) {
	app := buildApp()
	var gotUsername, gotPassword, gotRegion string
	var gotPortForwarding bool

	app.Action = func(c *cli.Context) error {
		gotUsername = c.String("username")
		gotPassword = c.String("password")
		gotRegion = c.String("region")
		gotPortForwarding = c.Bool("port-forwarding")
		return nil
	}

	err := app.Run([]string{
		"pia-wg-config",
		"--username", "username",
		"--password", "pass",
		"--region", "amsterdam404",
		"--port-forwarding",
	})
	if err != nil {
		t.Fatalf("expected root generate-style flags to parse, got: %v", err)
	}

	if gotUsername != "username" || gotPassword != "pass" || gotRegion != "amsterdam404" || !gotPortForwarding {
		t.Fatalf("unexpected parsed flags: username=%q password=%q region=%q portForwarding=%t",
			gotUsername, gotPassword, gotRegion, gotPortForwarding)
	}
}

func TestBuildApp_HelpIncludesExamples(t *testing.T) {
	app := buildApp()
	var out bytes.Buffer
	app.Writer = &out

	if err := app.Run([]string{"pia-wg-config", "--help"}); err != nil {
		t.Fatalf("help returned error: %v", err)
	}

	help := out.String()
	want := []string{
		"Examples:",
		"pia-wg-config generate --username USER --password PASS",
		"pia-wg-config --username USER --password PASS",
	}
	for _, s := range want {
		if !strings.Contains(help, s) {
			t.Fatalf("expected help to contain %q, got:\n%s", s, help)
		}
	}
}
