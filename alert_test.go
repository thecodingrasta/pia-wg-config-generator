package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thecodingrasta/pia-wg-config-generator/pia"
)

// -----------------------------------------------------------------------------
// outagePolicy: WHEN an alert is due
// -----------------------------------------------------------------------------

func TestPolicy_FiresExactlyOncePerOutageAfterThreshold(t *testing.T) {
	p := newOutagePolicy(3)
	now := time.Now()

	for i := 1; i <= 2; i++ {
		if fire, _ := p.failure(now); fire {
			t.Fatalf("attempt %d fired; threshold is 3", i)
		}
	}
	fire, attempts := p.failure(now)
	if !fire || attempts != 3 {
		t.Fatalf("attempt 3 must fire, got fire=%v attempts=%d", fire, attempts)
	}
	for i := 4; i <= 20; i++ {
		if fire, _ := p.failure(now); fire {
			t.Fatalf("attempt %d fired again: the alert must be sent once per outage", i)
		}
	}
}

func TestPolicy_NoAlertForTransientFailureThatRecovers(t *testing.T) {
	p := newOutagePolicy(3)
	now := time.Now()
	p.failure(now)
	p.failure(now) // two failed retries, below threshold
	recovered, _, attempts := p.healthy(now)
	if recovered {
		t.Fatal("a recovered notice must not be sent when no failure alert was")
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	// The streak was cleared: a fresh failure starts again from 1.
	if fire, n := p.failure(now); fire || n != 1 {
		t.Fatalf("expected a fresh count after recovery, got fire=%v n=%d", fire, n)
	}
}

func TestPolicy_RecoveredNoticeOnlyAfterAFailureAlert(t *testing.T) {
	p := newOutagePolicy(2)
	start := time.Now()
	p.failure(start)
	if fire, _ := p.failure(start.Add(time.Minute)); !fire {
		t.Fatal("failure alert should have fired")
	}
	recovered, outage, attempts := p.healthy(start.Add(10 * time.Minute))
	if !recovered {
		t.Fatal("recovered notice must be due after an alerted outage")
	}
	if outage < 9*time.Minute || outage > 11*time.Minute || attempts != 2 {
		t.Fatalf("outage=%s attempts=%d", outage, attempts)
	}
	// Only once.
	if again, _, _ := p.healthy(start.Add(11 * time.Minute)); again {
		t.Fatal("recovered notice must fire once")
	}
}

func TestPolicy_NewOutageAfterRecoveryCanAlertAgain(t *testing.T) {
	p := newOutagePolicy(1)
	now := time.Now()
	if fire, _ := p.failure(now); !fire {
		t.Fatal("first outage should alert")
	}
	p.healthy(now)
	if fire, _ := p.failure(now); !fire {
		t.Fatal("a second, separate outage must be able to alert again")
	}
}

func TestPolicy_ZeroThresholdDisablesAlerting(t *testing.T) {
	p := newOutagePolicy(0)
	for i := 0; i < 50; i++ {
		if fire, _ := p.failure(time.Now()); fire {
			t.Fatal("threshold 0 must never alert")
		}
	}
}

func TestPolicy_NilIsSafe(t *testing.T) {
	var p *outagePolicy
	if fire, _ := p.failure(time.Now()); fire {
		t.Fatal("nil policy fired")
	}
	if rec, _, _ := p.healthy(time.Now()); rec {
		t.Fatal("nil policy reported recovery")
	}
	if n, _ := p.snapshot(); n != 0 {
		t.Fatal("nil policy has failures")
	}
}

// -----------------------------------------------------------------------------
// alertDispatcher: HOW an alert is delivered
// -----------------------------------------------------------------------------

type recordedHook struct {
	command string
	env     map[string]string
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

func newRecordingDispatcher(fail, recovered string, timeout time.Duration, run hookRunner) *alertDispatcher {
	d := newAlertDispatcher(fail, recovered, "uk_southampton", timeout, []string{"p0000000", "Sup3rS3cretFakePassw0rd!"}, false)
	d.run = run
	return d
}

func TestDispatcher_RoutesFailureAndRecoveredToTheirHooks(t *testing.T) {
	var mu sync.Mutex
	var got []recordedHook
	d := newRecordingDispatcher("notify-fail", "notify-ok", time.Second, func(_ context.Context, cmd string, env []string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, recordedHook{cmd, envMap(env)})
		return "ok", nil
	})
	d.start()
	d.send(alertEvent{Kind: alertEventFailure, Message: "boom", Attempts: 3})
	d.send(alertEvent{Kind: alertEventRecovered, Message: "back", Attempts: 3, Outage: 90 * time.Second})
	d.stop()

	if len(got) != 2 {
		t.Fatalf("expected 2 deliveries, got %d", len(got))
	}
	if got[0].command != "notify-fail" || got[0].env["PIA_WG_EVENT"] != "failure" || got[0].env["PIA_WG_MESSAGE"] != "boom" {
		t.Fatalf("failure delivery wrong: %+v", got[0])
	}
	if got[1].command != "notify-ok" || got[1].env["PIA_WG_EVENT"] != "recovered" || got[1].env["PIA_WG_OUTAGE_SECONDS"] != "90" {
		t.Fatalf("recovered delivery wrong: %+v", got[1])
	}
	if got[0].env["PIA_WG_REGION"] != "uk_southampton" || got[0].env["PIA_WG_ATTEMPTS"] != "3" {
		t.Fatalf("context vars missing: %+v", got[0].env)
	}
}

func TestDispatcher_NoHookConfiguredIsNoOp(t *testing.T) {
	var calls int32
	d := newRecordingDispatcher("", "", time.Second, func(context.Context, string, []string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", nil
	})
	d.start()
	d.send(alertEvent{Kind: alertEventFailure, Message: "x"})
	d.send(alertEvent{Kind: alertEventRecovered, Message: "y"})
	d.stop()
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("no hook configured: nothing must run")
	}
}

// The hook environment is an allow-list: the PIA account credentials in the
// daemon's own environment must not reach the hook.
func TestDispatcher_HookEnvironmentDoesNotLeakCredentials(t *testing.T) {
	t.Setenv("PIA_USERNAME", "p0000000")
	t.Setenv("PIA_PASSWORD", "Sup3rS3cretFakePassw0rd!")
	t.Setenv("WIREGUARD_PASSWORD", "alsosecret")
	t.Setenv("PIA_WG_ALERT_WEBHOOK", "https://ntfy.example.invalid/topic")
	t.Setenv("PIA_WG_ALERT_FORMAT", "ntfy")

	d := newAlertDispatcher("h", "", "r", time.Second, nil, false)
	env := envMap(d.hookEnv(alertEvent{Kind: alertEventFailure, Message: "m"}))

	for _, banned := range []string{"PIA_USERNAME", "PIA_PASSWORD", "WIREGUARD_PASSWORD"} {
		if _, present := env[banned]; present {
			t.Errorf("%s leaked into the hook environment", banned)
		}
	}
	if env["PIA_WG_ALERT_WEBHOOK"] == "" || env["PIA_WG_ALERT_FORMAT"] != "ntfy" {
		t.Errorf("PIA_WG_ALERT_* settings must be passed through: %+v", env)
	}
}

// A hook that hangs must be killed by the timeout, and a hook that fails must
// not panic or propagate. Neither may delay the caller of send().
func TestDispatcher_TimeoutAndFailureNeverBlockOrPropagate(t *testing.T) {
	d := newRecordingDispatcher("slow", "", 50*time.Millisecond, func(ctx context.Context, _ string, _ []string) (string, error) {
		<-ctx.Done() // a hook that never returns on its own
		return "", ctx.Err()
	})
	d.start()

	began := time.Now()
	d.send(alertEvent{Kind: alertEventFailure, Message: "x"})
	if took := time.Since(began); took > 20*time.Millisecond {
		t.Fatalf("send() blocked the caller for %s", took)
	}
	d.stop() // waits for the hung hook to be cut off by its timeout
	if took := time.Since(began); took > time.Second {
		t.Fatalf("hung hook was not cut off by its timeout: %s", took)
	}
}

func TestDispatcher_FailingHookIsIgnored(t *testing.T) {
	var calls int32
	d := newRecordingDispatcher("bad", "", time.Second, func(context.Context, string, []string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "curl: (6) Could not resolve host", errors.New("exit status 6")
	})
	d.start()
	d.send(alertEvent{Kind: alertEventFailure, Message: "x"})
	d.stop()
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("hook should have been attempted exactly once, got %d", calls)
	}
}

// A flood of events must never block the recovery path; excess events are
// dropped rather than queued without bound.
func TestDispatcher_FloodNeverBlocks(t *testing.T) {
	release := make(chan struct{})
	d := newRecordingDispatcher("h", "", time.Second, func(context.Context, string, []string) (string, error) {
		<-release
		return "", nil
	})
	d.start()

	done := make(chan struct{})
	go func() {
		for i := 0; i < alertQueueSize*10; i++ {
			d.send(alertEvent{Kind: alertEventFailure})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("send() blocked under load")
	}
	close(release)
	d.stop()
}

func TestDispatcher_NilIsSafe(t *testing.T) {
	var d *alertDispatcher
	d.send(alertEvent{Kind: alertEventFailure}) // must not panic
}

// -----------------------------------------------------------------------------
// End to end through refreshRuntime: what actually reaches a hook.
// -----------------------------------------------------------------------------

type captured struct {
	mu     sync.Mutex
	events []map[string]string
}

func (c *captured) runner(_ context.Context, _ string, env []string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, envMap(env))
	return "", nil
}

func (c *captured) kinds() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, e := range c.events {
		out = append(out, e["PIA_WG_EVENT"])
	}
	return out
}

func alertingRuntime(t *testing.T, control *mockControlServer, alertAfter int) (*refreshRuntime, *captured) {
	t.Helper()
	rt, _ := detectionRuntime(t, control, 1)
	rt.username = "p0000000"
	rt.password = "Sup3rS3cretFakePassw0rd!"
	rt.policy = newOutagePolicy(alertAfter)
	cap := &captured{}
	rt.alerts = newRecordingDispatcher("fail", "ok", time.Second, cap.runner)
	rt.alerts.start()
	return rt, cap
}

// THE B.6 SCENARIO, in-process: PIA unreachable -> repeated failed recovery ->
// exactly ONE alert -> unblock -> recovery -> exactly ONE recovered notice.
func TestAlerting_RepeatedFailureAlertsOnceThenRecoveredNoticeAfterUnblock(t *testing.T) {
	control := newMockControlServer("running")
	defer control.close()
	control.setIP(realPublicIP)
	rt, cap := alertingRuntime(t, control, 3)
	rt.onHealthTick() // confirmed healthy

	var piaReachable atomic.Bool
	realGen := rt.generateFn
	rt.generateFn = func() (r pia.GenerateResult, err error) {
		if !piaReachable.Load() {
			// A realistic transport error that embeds the credentials in the URL.
			return r, errors.New(`Error Executing Request: Get "https://98.159.234.245:1337/addKey?pt=FAKEtokenFAKEtokenFAKEtokenFAKEtokenFAKEtoken0123456789abcdef&pubkey=PUBLICkeyPUBLICkeyPUBLICkeyPUBLICkey1234=": dial tcp 98.159.234.245:1337: i/o timeout (user p0000000 password Sup3rS3cretFakePassw0rd!)`)
		}
		return realGen()
	}

	control.setIP("") // silent eviction
	rt.onHealthTick() // recovery: 6 failed attempts, backoff is a few ms
	rt.alerts.stop()

	if got := cap.kinds(); len(got) != 1 || got[0] != "failure" {
		t.Fatalf("expected exactly one failure alert from 6 failed attempts, got %v", got)
	}
	msg := cap.events[0]["PIA_WG_MESSAGE"]
	for _, secret := range []string{"FAKEtoken", "p0000000", "Sup3rS3cretFakePassw0rd!", "PUBLICkey"} {
		if strings.Contains(msg, secret) {
			t.Fatalf("alert message leaks %q: %s", secret, msg)
		}
	}
	if !strings.Contains(msg, "i/o timeout") {
		t.Fatalf("alert lost its diagnostic value: %s", msg)
	}
	st := readStatus(t, rt.statusPath)
	for _, secret := range []string{"FAKEtoken", "p0000000", "Sup3rS3cretFakePassw0rd!"} {
		if strings.Contains(st.LastError, secret) {
			t.Fatalf("status.json last_error leaks %q: %s", secret, st.LastError)
		}
	}

	// PIA reachable again -> recovery succeeds -> healthy sample -> recovered.
	rt.alerts = newRecordingDispatcher("fail", "ok", time.Second, cap.runner)
	rt.alerts.start()
	piaReachable.Store(true)
	control.setIP("")
	rt.stateMu.Lock()
	rt.lastRecoveryUTC = time.Time{} // let the next tick act
	rt.stateMu.Unlock()
	rt.onHealthTick() // regenerates; the restart seam brings the tunnel back
	rt.onHealthTick() // healthy sample -> recovered notice
	rt.alerts.stop()

	got := cap.kinds()
	if len(got) != 2 || got[0] != "failure" || got[1] != "recovered" {
		t.Fatalf("expected [failure recovered], got %v", got)
	}
}

// A transient failure that clears before the threshold produces NO alert at all.
func TestAlerting_TransientFailureBelowThresholdIsSilent(t *testing.T) {
	control := newMockControlServer("running")
	defer control.close()
	control.setIP(realPublicIP)
	rt, cap := alertingRuntime(t, control, 3)
	rt.onHealthTick()

	var n int32
	realGen := rt.generateFn
	rt.generateFn = func() (r pia.GenerateResult, err error) {
		if atomic.AddInt32(&n, 1) <= 2 { // first two attempts fail, third succeeds
			return r, errors.New("dial tcp: i/o timeout")
		}
		return realGen()
	}
	control.setIP("")
	rt.onHealthTick()
	rt.onHealthTick() // healthy again
	rt.alerts.stop()

	if got := cap.kinds(); len(got) != 0 {
		t.Fatalf("a recovery that succeeds within the threshold must not alert, got %v", got)
	}
}

// A recovery that applies cleanly but never restores the tunnel is also a
// genuine failure, and counts toward the alert.
func TestAlerting_IneffectiveRecoveriesAlertOnce(t *testing.T) {
	control := newMockControlServer("running")
	defer control.close()
	control.setIP(realPublicIP)
	rt, cap := alertingRuntime(t, control, 3)
	rt.restartFn = func(string) error { return nil } // applies, but the tunnel stays down
	rt.recoveryCooldown = time.Nanosecond
	rt.onHealthTick()

	control.setIP("")
	for i := 0; i < 8; i++ {
		rt.onHealthTick()
		time.Sleep(time.Millisecond)
	}
	rt.alerts.stop()

	got := cap.kinds()
	if len(got) != 1 || got[0] != "failure" {
		t.Fatalf("expected exactly one failure alert for repeated ineffective recoveries, got %v", got)
	}
}

func TestAlerting_AlertsDisabledSendsNothing(t *testing.T) {
	control := newMockControlServer("running")
	defer control.close()
	control.setIP(realPublicIP)
	rt, cap := alertingRuntime(t, control, 0)
	rt.onHealthTick()
	rt.generateFn = func() (r pia.GenerateResult, err error) { return r, errors.New("down") }
	control.setIP("")
	rt.onHealthTick()
	rt.alerts.stop()
	if got := cap.kinds(); len(got) != 0 {
		t.Fatalf("--alert-after=0 must disable alerts, got %v", got)
	}
}
