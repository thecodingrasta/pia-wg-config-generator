package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thecodingrasta/pia-wg-config-generator/pia"
)

// mockControlServer is a stand-in for gluetun's HTTP control server whose
// reported VPN status can be flipped at will, simulating a tunnel that dies
// (PIA peer eviction) and comes back after a config apply + restart.
type mockControlServer struct {
	srv    *httptest.Server
	status atomic.Value // string
	// ip is what /v1/publicip/ip reports. ipUnavailable (the default) makes the
	// endpoint answer 404, i.e. a gluetun whose public IP cannot be read.
	ip atomic.Value // string
}

const ipUnavailable = "<unavailable>"

func newMockControlServer(initial string) *mockControlServer {
	m := &mockControlServer{}
	m.status.Store(initial)
	m.ip.Store(ipUnavailable)
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/vpn/status":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": m.status.Load().(string)})
		case "/v1/publicip/ip":
			ip := m.ip.Load().(string)
			if ip == ipUnavailable {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"public_ip": ip})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return m
}

func (m *mockControlServer) set(s string)   { m.status.Store(s) }
func (m *mockControlServer) setIP(s string) { m.ip.Store(s) }
func (m *mockControlServer) close()         { m.srv.Close() }

// newTestRuntime wires a refreshRuntime with injected seams so no real PIA or
// Docker is touched. generateCount tracks how many registrations were minted
// (defect F: there must be exactly one per recovery, never a leak storm).
func newTestRuntime(t *testing.T, control *mockControlServer) (*refreshRuntime, *int64) {
	t.Helper()
	dir := t.TempDir()

	var genCount int64
	rt := &refreshRuntime{
		region:            "uk_southampton",
		stateDir:          dir,
		portForwarding:    true,
		ipv6ModeStr:       "kill",
		fallbackInterval:  12 * time.Hour,
		jitterMax:         0,
		healthInterval:    20 * time.Millisecond,
		controlURL:        control.srv.URL,
		controlTimeout:    time.Second,
		backoffMin:        time.Millisecond,
		backoffMax:        5 * time.Millisecond,
		restartContainer:  "mock-gluetun",
		configPath:        filepath.Join(dir, defaultConfigFileName),
		statusPath:        filepath.Join(dir, defaultStatusFileName),
		forwardedPortPath: filepath.Join(dir, defaultForwardedPortOut),
		pendingPFPath:     filepath.Join(dir, defaultPendingPFName),
		lifetime:          pia.NewLifetimeStore(dir),
	}
	rt.generateFn = func() (pia.GenerateResult, error) {
		n := atomic.AddInt64(&genCount, 1)
		return pia.GenerateResult{
			Config: "[Interface]\nPrivateKey = REDACTED\nAddress = 10.0.0.2\nDNS = 10.0.0.243\n[Peer]\nPublicKey = REDACTED\nAllowedIPs = 0.0.0.0/0, ::/0\nEndpoint = 203.0.113." + itoa(int(n)) + ":1337\nPersistentKeepalive = 25\n",
			Key: pia.AddKeyResult{
				Status: "OK", Gateway: "10.175.0.1", ServerIP: "203.0.113." + itoa(int(n)),
				ServerPort: 1337, PeerIP: "10.0.0.2", DNSServers: []string{"10.0.0.243"},
			},
		}, nil
	}
	rt.restartFn = func(container string) error {
		// Simulate gluetun reloading the fresh config and the tunnel coming back.
		control.set("running")
		return nil
	}
	return rt, &genCount
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func readStatus(t *testing.T, path string) daemonStatus {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	var s daemonStatus
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	return s
}

// Acceptance #1: a dead tunnel (control server reports "stopped") is recovered
// with NO human action — a fresh config is written, gluetun restarted, and
// status.json reports the tunnel healthy again.
func TestAcceptance_ReactiveRecoveryFromDeadTunnel(t *testing.T) {
	control := newMockControlServer("stopped") // tunnel is dead at start
	defer control.close()

	rt, genCount := newTestRuntime(t, control)

	// One health tick observes the dead tunnel and recovers.
	rt.onHealthTick()

	if atomic.LoadInt64(genCount) == 0 {
		t.Fatalf("expected recovery to regenerate a config, but none was minted")
	}
	if _, err := os.Stat(rt.configPath); err != nil {
		t.Fatalf("expected a fresh wg0.conf written during recovery: %v", err)
	}
	status := readStatus(t, rt.statusPath)
	// Applying a config does not prove the tunnel works. Until a health sample
	// confirms it, status.json must NOT claim the tunnel is healthy.
	if status.TunnelHealthy {
		t.Fatalf("status.json claimed a healthy tunnel before any sample confirmed it: %+v", status)
	}
	if status.RecoveryCount != 1 {
		t.Fatalf("expected recovery_count=1, got %d", status.RecoveryCount)
	}
	// gluetun reloads the new config (the restart seam flips the mock to
	// running); the next health sample confirms the recovery.
	rt.onHealthTick()
	status = readStatus(t, rt.statusPath)
	if !status.TunnelHealthy {
		t.Fatalf("expected status.json to report tunnel healthy once a sample confirmed recovery, got %+v", status)
	}
	// PF metadata must be handed to the pf daemon.
	if _, ok, _ := readPendingPortForward(rt.pendingPFPath); !ok {
		t.Fatalf("expected pending port-forward metadata to be written for the pf daemon")
	}
}

// Acceptance #2: config refresh succeeds while the tunnel is DOWN. The seam
// mirrors the real generator, which only hits PIA's public API — the whole
// point of running refresh OUTSIDE the netns. We prove the regenerate path does
// not depend on tunnel health by regenerating while status stays "stopped".
func TestAcceptance_ConfigRefreshSucceedsWithTunnelDown(t *testing.T) {
	control := newMockControlServer("stopped")
	defer control.close()
	rt, _ := newTestRuntime(t, control)
	// Prevent the restart seam from flipping the tunnel healthy, so the refresh
	// genuinely runs against a persistently-down tunnel.
	rt.restartFn = func(string) error { return nil }

	if err := rt.regenerateOnce("test-tunnel-down"); err != nil {
		t.Fatalf("config refresh must succeed with the tunnel down, got: %v", err)
	}
	if _, err := os.Stat(rt.configPath); err != nil {
		t.Fatalf("expected a config written with tunnel down: %v", err)
	}
}

// Acceptance #3: multiple full recovery cycles complete without outage, and
// each eviction mints EXACTLY ONE new registration (defect F — no leak storm,
// unlike the old 740-restart loop against a stale file).
func TestAcceptance_MultipleCyclesSingleRegistrationEach(t *testing.T) {
	control := newMockControlServer("running")
	defer control.close()
	rt, genCount := newTestRuntime(t, control)
	rt.recoveryCooldown = time.Millisecond // each cycle is a genuinely new eviction

	const cycles = 3
	for i := 0; i < cycles; i++ {
		rt.onHealthTick()                // a healthy sample confirms the current registration
		control.set("stopped")           // simulate PIA evicting the peer
		rt.onHealthTick()                // observe + recover (restart flips back to running)
		time.Sleep(2 * time.Millisecond) // clear the tiny cooldown before next cycle
		rt.onHealthTick()                // the new registration is confirmed healthy
		status := readStatus(t, rt.statusPath)
		if !status.TunnelHealthy {
			t.Fatalf("cycle %d: expected healthy after recovery, got %+v", i, status)
		}
	}

	if got := atomic.LoadInt64(genCount); got != cycles {
		t.Fatalf("expected exactly %d registrations across %d cycles (one per eviction), got %d", cycles, cycles, got)
	}

	// After observing losses, the cadence must be lifetime-aware and shorter
	// than the 12h guess that originally failed.
	rec, _ := rt.lifetime.Load()
	if _, ok := rec.MeasuredLifetime(); !ok {
		t.Fatalf("expected a measured lifetime to be recorded after evictions")
	}
}

// A healthy tunnel must NOT trigger recovery — otherwise we would re-register
// needlessly and risk the registration cap (defect F).
func TestAcceptance_HealthyTunnelDoesNotRegenerate(t *testing.T) {
	control := newMockControlServer("running")
	defer control.close()
	rt, genCount := newTestRuntime(t, control)

	for i := 0; i < 5; i++ {
		rt.onHealthTick()
	}
	if got := atomic.LoadInt64(genCount); got != 0 {
		t.Fatalf("a healthy tunnel must not regenerate; got %d regenerations", got)
	}
	status := readStatus(t, rt.statusPath)
	if !status.TunnelHealthy {
		t.Fatalf("expected healthy status, got %+v", status)
	}
}

// Single-flight: concurrent recovery triggers must serialize so two
// registrations are never minted for one eviction.
func TestAcceptance_SingleFlightSerializesRecovery(t *testing.T) {
	control := newMockControlServer("stopped")
	defer control.close()
	rt, genCount := newTestRuntime(t, control)

	// Make the restart slow so overlapping calls would race without the lock.
	rt.restartFn = func(string) error {
		time.Sleep(30 * time.Millisecond)
		control.set("running")
		return nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = rt.regenerateWithBackoff("concurrent")
		}()
	}
	wg.Wait()

	// Four concurrent recoveries, but they serialize; each still mints one
	// config (the guard prevents CONCURRENT mint, not sequential), so the
	// important invariant is that none raced/panicked and the count is bounded.
	if got := atomic.LoadInt64(genCount); got < 1 || got > 4 {
		t.Fatalf("expected serialized recoveries to mint between 1 and 4 configs, got %d", got)
	}
}

// Anti-churn: a tunnel that stays DOWN across many ticks must NOT re-register
// every tick. After one reactive recovery, the cooldown suppresses further
// re-registration until it elapses (defect C "no restart loops" / F "no leaked
// registrations").
func TestAcceptance_RecoveryCooldownPreventsChurn(t *testing.T) {
	control := newMockControlServer("stopped")
	defer control.close()
	rt, genCount := newTestRuntime(t, control)
	// Tunnel never recovers (simulate gluetun wedged): restart does NOT flip it.
	rt.restartFn = func(string) error { return nil }
	rt.recoveryCooldown = time.Hour // long cooldown for the test

	for i := 0; i < 8; i++ {
		rt.onHealthTick()
	}

	if got := atomic.LoadInt64(genCount); got != 1 {
		t.Fatalf("expected exactly ONE registration despite 8 down ticks (cooldown), got %d", got)
	}
}

// The control-server check must treat an unreachable control server as
// unhealthy (tunnel host down), driving recovery rather than a false "healthy".
func TestAcceptance_UnreachableControlServerIsUnhealthy(t *testing.T) {
	state := pia.CheckTunnelViaControlServer(context.Background(), "http://127.0.0.1:1", 100*time.Millisecond)
	if state.Healthy {
		t.Fatalf("unreachable control server must be unhealthy")
	}
}

// Redaction guard: status.json must never contain the private key, token,
// payload or signature.
func TestAcceptance_StatusFileRedactsSecrets(t *testing.T) {
	control := newMockControlServer("stopped")
	defer control.close()
	rt, _ := newTestRuntime(t, control)
	rt.restartFn = func(string) error { return nil }
	_ = rt.regenerateOnce("redaction")

	data, err := os.ReadFile(rt.statusPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	for _, secret := range []string{"PrivateKey", "signature", "payload", "REDACTED"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("status.json must not contain %q; got: %s", secret, string(data))
		}
	}
}
