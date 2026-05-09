package main

import (
	"bytes"
	"flag"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thecodingrasta/pia-wg-config-generator/pia"
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
	foundRestartContainer := false
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
		if names := flag.Names(); len(names) > 0 && names[0] == "restart-container" {
			foundRestartContainer = true
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
	if !foundRestartContainer {
		t.Fatalf("expected daemon command to expose --restart-container")
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

func TestRestartDockerContainerWithClient_PostsRestartRequest(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotQuery string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := restartDockerContainerWithClient(server.Client(), server.URL, "media-gluetun", false)
	if err != nil {
		t.Fatalf("expected docker restart request to succeed, got: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/containers/media-gluetun/restart" {
		t.Fatalf("unexpected restart path: %s", gotPath)
	}
	if gotQuery != "t=10" {
		t.Fatalf("unexpected restart query: %s", gotQuery)
	}
}

func TestRestartDockerContainerWithClient_ReturnsDockerErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No such container"}`))
	}))
	defer server.Close()

	err := restartDockerContainerWithClient(server.Client(), server.URL, "missing", false)
	if err == nil {
		t.Fatalf("expected docker restart error")
	}
	if !strings.Contains(err.Error(), "No such container") {
		t.Fatalf("expected docker error body, got: %v", err)
	}
}

func TestPendingPortForwardRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), defaultPendingPFName)
	key := pia.AddKeyResult{
		Gateway:   "10.175.0.1",
		ServerVip: "10.175.0.1",
		PeerIP:    "10.0.0.2",
	}

	if err := writePendingPortForward(path, key); err != nil {
		t.Fatalf("write pending: %v", err)
	}

	pending, ok, err := readPendingPortForward(path)
	if err != nil {
		t.Fatalf("read pending: %v", err)
	}
	if !ok {
		t.Fatalf("expected pending state")
	}
	if pending.Key.Gateway != key.Gateway || pending.Key.PeerIP != key.PeerIP {
		t.Fatalf("unexpected pending key: %+v", pending.Key)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat pending: %v", err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("expected pending file mode 0600, got %v", info.Mode().Perm())
		}
	}

	if err := removePendingPortForward(path); err != nil {
		t.Fatalf("remove pending: %v", err)
	}
	if _, ok, err := readPendingPortForward(path); err != nil || ok {
		t.Fatalf("expected pending state to be absent, ok=%t err=%v", ok, err)
	}
}
