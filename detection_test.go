package main

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thecodingrasta/pia-wg-config-generator/pia"
)

// =============================================================================
// Mid-life silent eviction detection (gap A).
//
// Measured against real gluetun v3.41.3 after PIA silently dropped the peer:
//   /v1/vpn/status  = "running" in 57 of 60 one-second samples (it only dips to
//                     stopping/starting for 1-2 s while gluetun restarts its
//                     internal loop against the same dead config);
//   /v1/publicip/ip = empty in 60 of 60 samples.
// A status-only poll therefore almost never sees the loss. These tests replay
// that exact pattern.
// =============================================================================

const realPublicIP = "98.159.234.246"

func detectionRuntime(t *testing.T, control *mockControlServer, threshold int) (*refreshRuntime, *int64) {
	t.Helper()
	rt, gen := newTestRuntime(t, control)
	rt.unhealthyThreshold = threshold
	// The restart seam in newTestRuntime flips the mock back to "running"; the
	// public IP must come back with it, as it does on a real reconnect.
	prev := rt.restartFn
	rt.restartFn = func(c string) error {
		err := prev(c)
		control.setIP(realPublicIP)
		return err
	}
	return rt, gen
}

// The headline case: status says "running" the whole time, the public IP is
// empty. The old detector would never fire; the new one must, once the loss has
// persisted for the debounce threshold.
func TestDetection_RunningButNoPublicIPTriggersRecoveryAfterThreshold(t *testing.T) {
	control := newMockControlServer("running")
	defer control.close()
	control.setIP(realPublicIP)
	rt, gen := detectionRuntime(t, control, 2)

	rt.onHealthTick() // healthy: confirms the registration
	if got := atomic.LoadInt64(gen); got != 0 {
		t.Fatalf("a healthy tunnel must not regenerate, got %d", got)
	}

	control.setIP("") // silent eviction: gluetun still says "running"
	rt.onHealthTick()
	if got := atomic.LoadInt64(gen); got != 0 {
		t.Fatalf("one unhealthy sample must not trigger recovery (debounce), got %d registrations", got)
	}
	rt.onHealthTick()
	if got := atomic.LoadInt64(gen); got != 1 {
		t.Fatalf("expected exactly one recovery after 2 consecutive unhealthy samples, got %d", got)
	}
}

// Replays the measured pattern literally: status flaps running/stopping while
// the public IP stays empty. Every sample is unhealthy, so the debounce counts
// them and recovery fires; a status-only detector would have been reset by the
// "running" samples.
func TestDetection_FlappingStatusWithEmptyIPStillRecovers(t *testing.T) {
	control := newMockControlServer("running")
	defer control.close()
	control.setIP(realPublicIP)
	rt, gen := detectionRuntime(t, control, 3)
	rt.onHealthTick()

	control.setIP("")
	for _, s := range []string{"running", "stopping", "running"} {
		control.set(s)
		rt.onHealthTick()
	}
	if got := atomic.LoadInt64(gen); got != 1 {
		t.Fatalf("flapping status with an empty public IP must recover exactly once, got %d", got)
	}
}

// A single bad sample (for example gluetun restarted by gluetun-monitor) must
// not cause a re-registration, and a healthy sample resets the count.
func TestDetection_TransientBlipIsDebouncedAndCounterResets(t *testing.T) {
	control := newMockControlServer("running")
	defer control.close()
	control.setIP(realPublicIP)
	rt, gen := detectionRuntime(t, control, 2)
	rt.onHealthTick()

	for i := 0; i < 5; i++ {
		control.setIP("")
		rt.onHealthTick() // unhealthy #1
		control.setIP(realPublicIP)
		rt.onHealthTick() // healthy: resets the streak
	}
	if got := atomic.LoadInt64(gen); got != 0 {
		t.Fatalf("isolated blips must never trigger recovery, got %d registrations", got)
	}
}

// If the public-IP endpoint cannot be read, a running tunnel is left alone.
// Otherwise an upgrade of gluetun that adds authentication would put the daemon
// into an endless re-registration loop.
func TestDetection_UnreadablePublicIPNeverTriggersRecovery(t *testing.T) {
	control := newMockControlServer("running") // public IP endpoint answers 404
	defer control.close()
	rt, gen := detectionRuntime(t, control, 1)

	for i := 0; i < 30; i++ {
		rt.onHealthTick()
	}
	if got := atomic.LoadInt64(gen); got != 0 {
		t.Fatalf("unreadable public IP + running must stay quiet, got %d registrations", got)
	}
	if st := readStatus(t, rt.statusPath); !st.TunnelHealthy || st.PublicIPPresent != nil {
		t.Fatalf("expected healthy with public_ip_present omitted, got %+v", st)
	}
}

// A persistent fault must cost at most a bounded number of registrations: the
// cooldown doubles after each recovery that did not restore the tunnel.
func TestDetection_EscalatingCooldownBoundsRegistrationChurn(t *testing.T) {
	control := newMockControlServer("running")
	defer control.close()
	rt, _ := detectionRuntime(t, control, 1)
	rt.recoveryCooldown = 3 * time.Minute

	cases := []struct {
		ineffective int
		want        time.Duration
	}{
		{0, 3 * time.Minute},
		{1, 6 * time.Minute},
		{2, 12 * time.Minute},
		{3, 24 * time.Minute},
		{4, recoveryCooldownCap},
		{10, recoveryCooldownCap},
	}
	for _, tc := range cases {
		rt.ineffectiveRecoveries = tc.ineffective
		rt.stateMu.Lock()
		got := rt.effectiveCooldownLocked()
		rt.stateMu.Unlock()
		if got != tc.want {
			t.Errorf("ineffective=%d: cooldown = %s, want %s", tc.ineffective, got, tc.want)
		}
	}
}

// End to end: a tunnel that never comes back is recovered, then NOT recovered
// again until the (escalated) cooldown passes.
func TestDetection_PersistentFaultDoesNotChurnRegistrations(t *testing.T) {
	control := newMockControlServer("running")
	defer control.close()
	control.setIP(realPublicIP)
	rt, gen := detectionRuntime(t, control, 1)
	rt.recoveryCooldown = time.Hour // long: nothing may re-register inside the test
	// Recovery "works" mechanically but the tunnel stays down (e.g. PIA outage).
	rt.restartFn = func(string) error { return nil }

	rt.onHealthTick() // healthy -> confirmed
	control.setIP("")
	for i := 0; i < 50; i++ {
		rt.onHealthTick()
	}
	if got := atomic.LoadInt64(gen); got != 1 {
		t.Fatalf("a persistent fault must produce exactly one registration inside the cooldown, got %d", got)
	}
}

// A registration that was never seen healthy says nothing about PIA's eviction
// time, so its death must not be recorded as a lifetime (it would shorten the
// proactive refresh cadence).
func TestDetection_LossOfNeverHealthyRegistrationIsNotMeasured(t *testing.T) {
	control := newMockControlServer("stopped") // down from the very beginning
	defer control.close()
	rt, _ := detectionRuntime(t, control, 1)

	if err := rt.regenerateWithBackoff("startup"); err != nil {
		t.Fatal(err)
	}
	rt.onHealthTick() // still down -> recovery of a never-healthy registration

	rec, _ := rt.lifetime.Load()
	if _, ok := rec.MeasuredLifetime(); ok {
		t.Fatalf("a never-healthy registration must not produce a measured lifetime, got %v", rec.ObservedLifetimesSeconds)
	}
}

// A registration that WAS healthy and then died is a real measurement, and the
// recorded loss time is the last moment it was known alive.
func TestDetection_LossOfConfirmedRegistrationIsMeasuredFromLastHealthySample(t *testing.T) {
	control := newMockControlServer("running")
	defer control.close()
	control.setIP(realPublicIP)
	rt, _ := detectionRuntime(t, control, 1)

	if err := rt.regenerateWithBackoff("startup"); err != nil {
		t.Fatal(err)
	}
	// Pretend the registration was born 2h ago.
	birth := time.Now().UTC().Add(-2 * time.Hour)
	if err := rt.lifetime.Save(pia.LifetimeRecord{CurrentRegistrationUTC: birth}); err != nil {
		t.Fatal(err)
	}

	rt.onHealthTick() // healthy: lastHealthy = now
	control.setIP("")
	rt.onHealthTick() // unhealthy -> recovery

	rec, _ := rt.lifetime.Load()
	life, ok := rec.MeasuredLifetime()
	if !ok {
		t.Fatal("expected a measured lifetime")
	}
	if life < 119*time.Minute || life > 121*time.Minute {
		t.Fatalf("measured lifetime = %s, want about 2h", life)
	}
}

// ---------------------------------------------------------------------------
// Startup adoption: redeploying this container must not re-register or restart
// gluetun when the existing registration is fine.
// ---------------------------------------------------------------------------

func adoptionRuntime(t *testing.T, control *mockControlServer, age time.Duration, region string) (*refreshRuntime, *int64) {
	t.Helper()
	rt, gen := detectionRuntime(t, control, 2)
	rt.adoptHealthy = true
	if err := os.WriteFile(rt.configPath, []byte("[Interface]\nPrivateKey = FAKE\n[Peer]\nEndpoint = 203.0.113.9:1337\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rt.lifetime.Save(pia.LifetimeRecord{CurrentRegistrationUTC: time.Now().UTC().Add(-age)}); err != nil {
		t.Fatal(err)
	}
	if region != "" {
		if err := os.WriteFile(rt.statusPath, []byte(`{"region":"`+region+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return rt, gen
}

func TestAdoption_KeepsHealthyYoungRegistration(t *testing.T) {
	control := newMockControlServer("running")
	defer control.close()
	control.setIP(realPublicIP)
	rt, gen := adoptionRuntime(t, control, 90*time.Minute, "uk_southampton")

	if !rt.tryAdopt() {
		t.Fatal("a healthy, young, same-region registration must be adopted")
	}
	if atomic.LoadInt64(gen) != 0 {
		t.Fatal("adoption must not register again")
	}
	st := readStatus(t, rt.statusPath)
	if !st.TunnelHealthy || st.LastGenerateUTC.IsZero() || st.ExpectedExpiryUTC.IsZero() {
		t.Fatalf("adopted status must be complete: %+v", st)
	}
	// The registration clock must be preserved, not reset to "now".
	if age := time.Since(st.RegistrationUTC); age < 89*time.Minute || age > 91*time.Minute {
		t.Fatalf("registration age after adoption = %s, want about 90m", age)
	}
}

func TestAdoption_RefusedWhenNotSafe(t *testing.T) {
	cases := map[string]func(t *testing.T) *refreshRuntime{
		"tunnel unhealthy": func(t *testing.T) *refreshRuntime {
			c := newMockControlServer("stopped")
			t.Cleanup(c.close)
			rt, _ := adoptionRuntime(t, c, time.Hour, "uk_southampton")
			return rt
		},
		"registration too old": func(t *testing.T) *refreshRuntime {
			c := newMockControlServer("running")
			t.Cleanup(c.close)
			c.setIP(realPublicIP)
			rt, _ := adoptionRuntime(t, c, 13*time.Hour, "uk_southampton")
			return rt
		},
		"region changed": func(t *testing.T) *refreshRuntime {
			c := newMockControlServer("running")
			t.Cleanup(c.close)
			c.setIP(realPublicIP)
			rt, _ := adoptionRuntime(t, c, time.Hour, "nl_amsterdam")
			return rt
		},
		"no previous status": func(t *testing.T) *refreshRuntime {
			c := newMockControlServer("running")
			t.Cleanup(c.close)
			c.setIP(realPublicIP)
			rt, _ := adoptionRuntime(t, c, time.Hour, "")
			return rt
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			if build(t).tryAdopt() {
				t.Fatal("adoption must be refused")
			}
		})
	}
}
