package main

import (
	"testing"

	cli "github.com/urfave/cli/v2"
)

func commandByName(app *cli.App, name string) *cli.Command {
	for _, cmd := range app.Commands {
		if cmd.Name == name {
			return cmd
		}
	}
	return nil
}

func hasFlag(cmd *cli.Command, name string) bool {
	for _, f := range cmd.Flags {
		if names := f.Names(); len(names) > 0 && names[0] == name {
			return true
		}
	}
	return false
}

func TestBuildApp_RegistersRefreshAndPFCommands(t *testing.T) {
	app := buildApp()

	refresh := commandByName(app, "refresh")
	if refresh == nil {
		t.Fatalf("expected a 'refresh' command to be registered")
	}
	pf := commandByName(app, "pf")
	if pf == nil {
		t.Fatalf("expected a 'pf' command to be registered")
	}
}

func TestRefreshCommand_HasSelfRecoveryFlags(t *testing.T) {
	cmd := buildRefreshCommand()
	for _, want := range []string{
		"max-registration-age",
		"health-check-interval",
		"control-server-url",
		"recovery-backoff-min",
		"recovery-backoff-max",
		"restart-container",
		"on-failure",
	} {
		if !hasFlag(cmd, want) {
			t.Fatalf("expected refresh command to expose --%s", want)
		}
	}
}

func TestPFCommand_HasGatewayFlags(t *testing.T) {
	cmd := buildPFCommand()
	for _, want := range []string{"wait-for-gateway", "gateway-timeout", "poll-interval", "region"} {
		if !hasFlag(cmd, want) {
			t.Fatalf("expected pf command to expose --%s", want)
		}
	}
}

func TestDaemonCommand_StillExistsForBackCompat(t *testing.T) {
	// The original combined daemon must remain for gluetun-native / existing
	// deployments and the back-compat path.
	app := buildApp()
	if commandByName(app, "daemon") == nil {
		t.Fatalf("expected 'daemon' command to remain for back-compat")
	}
}
