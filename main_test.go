package main

import (
	"bytes"
	"flag"
	"net"
	"strings"
	"testing"
	"time"

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

func TestBuildDaemonCommand_HasConfigRefreshHook(t *testing.T) {
	cmd := buildDaemonCommand()

	foundConfigHook := false
	foundRetryDelay := false
	foundWaitForGateway := false
	for _, flag := range cmd.Flags {
		if names := flag.Names(); len(names) > 0 && names[0] == "on-config-change" {
			foundConfigHook = true
		}
		if names := flag.Names(); len(names) > 0 && names[0] == "retry-delay" {
			foundRetryDelay = true
		}
		if names := flag.Names(); len(names) > 0 && names[0] == "wait-for-gateway" {
			foundWaitForGateway = true
		}
	}

	if !foundConfigHook {
		t.Fatalf("expected daemon command to expose --on-config-change")
	}
	if !foundRetryDelay {
		t.Fatalf("expected daemon command to expose --retry-delay")
	}
	if !foundWaitForGateway {
		t.Fatalf("expected daemon command to expose --wait-for-gateway")
	}
}

func TestExpandConfigHookCommand_ReplacesAllPlaceholders(t *testing.T) {
	got := expandConfigHookCommand(
		"reload {config} {state_dir} {forwarded_port} {port}",
		"/state",
		"/state/wg0.conf",
		"/state/forwarded_port",
		"43210",
	)
	want := "reload /state/wg0.conf /state /state/forwarded_port 43210"

	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestDaemonRetryDelayDefault(t *testing.T) {
	cmd := buildDaemonCommand()
	set := flag.NewFlagSet("daemon", flag.ContinueOnError)
	for _, f := range cmd.Flags {
		if err := f.Apply(set); err != nil {
			t.Fatalf("applying flag: %v", err)
		}
	}

	c := cli.NewContext(nil, set, nil)
	if got := c.Duration("retry-delay"); got != defaultDaemonRetryDelay {
		t.Fatalf("expected retry-delay %s, got %s", defaultDaemonRetryDelay, got)
	}
	if defaultDaemonRetryDelay >= defaultDaemonInterval {
		t.Fatalf("retry delay must be shorter than refresh interval")
	}
}

func TestSleepAfterFailure_ZeroReturnsImmediately(t *testing.T) {
	start := time.Now()
	sleepAfterFailure("test failure", nil, 0, false)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("expected zero retry delay to return immediately, took %s", elapsed)
	}
}

func TestWaitForGateway_ReturnsWhenReachable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()

	if err := waitForGateway(listener.Addr().String(), time.Second, 10*time.Millisecond, false); err != nil {
		t.Fatalf("expected reachable gateway, got: %v", err)
	}
	<-done
}

func TestWaitForGateway_ReturnsClearErrorWhenUnreachable(t *testing.T) {
	err := waitForGateway("127.0.0.1:1", 20*time.Millisecond, 10*time.Millisecond, false)
	if err == nil {
		t.Fatalf("expected unreachable gateway error")
	}
	if !strings.Contains(err.Error(), "not reachable through the active tunnel") {
		t.Fatalf("expected tunnel reachability error, got: %v", err)
	}
}
