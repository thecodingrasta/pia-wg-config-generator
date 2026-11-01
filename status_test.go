package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thecodingrasta/pia-wg-config-generator/pia"
)

// rawStatus reads status.json as an untyped map so tests can assert which keys
// are PRESENT, not just what a zero value decodes to.
func rawStatus(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if strings.Contains(string(data), "0001-01-01") {
		t.Fatalf("status.json contains a zero time (year 1):\n%s", data)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	return m
}

func mustHave(t *testing.T, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Errorf("status.json is missing %q: %v", k, m)
		}
	}
}

func mustLack(t *testing.T, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; ok {
			t.Errorf("status.json should not contain %q here: %v", k, m)
		}
	}
}

// Walks the whole lifecycle gap D asks about: fresh start, healthy, proactive
// refresh, a measured loss, recovery. At every step no field is a placeholder.
func TestStatusLifecycle(t *testing.T) {
	control := newMockControlServer("running")
	defer control.close()
	control.setIP(realPublicIP)
	rt, _ := detectionRuntime(t, control, 1)
	rt.recoveryCooldown = time.Millisecond

	// ---- 1. fresh start: the startup config was applied --------------------
	if err := rt.regenerateWithBackoff("startup"); err != nil {
		t.Fatal(err)
	}
	rt.armNextRefresh()
	m := rawStatus(t, rt.statusPath)
	mustHave(t, m, "last_generate_utc", "next_refresh_utc", "registration_utc", "expected_expiry_utc",
		"lifetime_source", "registration_age_seconds", "mode", "region")
	if m["lifetime_source"] != "fallback" {
		t.Errorf("before any eviction the lifetime source must be 'fallback', got %v", m["lifetime_source"])
	}
	mustLack(t, m, "measured_lifetime_seconds", "unhealthy_since_utc", "last_recovery_utc", "last_healthy_utc")
	if m["tunnel_healthy"] != false {
		t.Errorf("a config that no sample has confirmed must not claim a healthy tunnel: %v", m["tunnel_healthy"])
	}
	st := readStatus(t, rt.statusPath)
	if !st.NextRefreshUTC.After(st.LastGenerateUTC.Add(11 * time.Hour)) {
		t.Errorf("next_refresh_utc should be ~12h after the registration, got %s vs %s", st.NextRefreshUTC, st.LastGenerateUTC)
	}
	if !st.ExpectedExpiryUTC.Equal(st.RegistrationUTC.Add(12 * time.Hour)) {
		t.Errorf("fallback expected_expiry_utc should be birth+12h, got %s (birth %s)", st.ExpectedExpiryUTC, st.RegistrationUTC)
	}
	firstGenerate, firstNext, firstBirth := st.LastGenerateUTC, st.NextRefreshUTC, st.RegistrationUTC

	// ---- 2. healthy samples must NOT erase what the apply step recorded ----
	// (This is the live bug: the once-a-minute health writer used to overwrite
	//  status.json and reset last_generate_utc / next_refresh_utc to year 1.)
	rt.onHealthTick()
	rt.onHealthTick()
	m = rawStatus(t, rt.statusPath)
	mustHave(t, m, "last_generate_utc", "next_refresh_utc", "last_health_check_utc", "last_healthy_utc", "control_status", "public_ip_present")
	mustLack(t, m, "unhealthy_since_utc", "last_error")
	st = readStatus(t, rt.statusPath)
	if !st.TunnelHealthy || st.PublicIPPresent == nil || !*st.PublicIPPresent {
		t.Errorf("expected healthy with a public IP: %+v", st)
	}
	if !st.LastGenerateUTC.Equal(firstGenerate) || !st.NextRefreshUTC.Equal(firstNext) {
		t.Errorf("health samples overwrote the apply-time fields: generate %s->%s next %s->%s",
			firstGenerate, st.LastGenerateUTC, firstNext, st.NextRefreshUTC)
	}

	// ---- 3. a proactive refresh -------------------------------------------
	time.Sleep(5 * time.Millisecond)
	if err := rt.regenerateWithBackoff("proactive"); err != nil {
		t.Fatal(err)
	}
	st = readStatus(t, rt.statusPath)
	if !st.LastGenerateUTC.After(firstGenerate) || !st.RegistrationUTC.After(firstBirth) {
		t.Errorf("a refresh must advance last_generate_utc and the registration: %+v", st)
	}
	if st.RecoveryCount != 0 {
		t.Errorf("a proactive refresh is not a recovery, recovery_count=%d", st.RecoveryCount)
	}
	if !st.NextRefreshUTC.After(firstNext) {
		t.Errorf("next_refresh_utc must move out after a refresh: %s -> %s", firstNext, st.NextRefreshUTC)
	}

	// ---- 4. a measured loss -------------------------------------------------
	birth := time.Now().UTC().Add(-3 * time.Hour)
	if err := rt.lifetime.Save(pia.LifetimeRecord{CurrentRegistrationUTC: birth}); err != nil {
		t.Fatal(err)
	}
	rt.onHealthTick() // healthy sample for the (re-dated) registration
	control.setIP("")
	rt.onHealthTick() // loss detected, recovery runs
	m = rawStatus(t, rt.statusPath)
	mustHave(t, m, "measured_lifetime_seconds", "last_recovery_utc", "recovery_count", "unhealthy_since_utc")
	if m["lifetime_source"] != "measured" {
		t.Errorf("after a loss the lifetime source must be 'measured', got %v", m["lifetime_source"])
	}
	st = readStatus(t, rt.statusPath)
	if st.MeasuredLifetimeSecs < 3*3600-120 || st.MeasuredLifetimeSecs > 3*3600+120 {
		t.Errorf("measured lifetime = %ds, want about 3h", st.MeasuredLifetimeSecs)
	}
	if st.RecoveryCount != 1 || st.TunnelHealthy {
		t.Errorf("right after applying the recovery the tunnel is unconfirmed: %+v", st)
	}
	wantExpiry := st.RegistrationUTC.Add(time.Duration(st.MeasuredLifetimeSecs) * time.Second)
	if !st.ExpectedExpiryUTC.Equal(wantExpiry) {
		t.Errorf("measured expected_expiry_utc should be birth+measured, got %s want %s", st.ExpectedExpiryUTC, wantExpiry)
	}

	// ---- 5. after recovery is confirmed ------------------------------------
	rt.onHealthTick()
	m = rawStatus(t, rt.statusPath)
	mustLack(t, m, "unhealthy_since_utc", "last_error")
	mustHave(t, m, "last_recovery_utc", "last_healthy_utc")
	st = readStatus(t, rt.statusPath)
	if !st.TunnelHealthy || st.RecoveryCount != 1 {
		t.Errorf("recovered state wrong: %+v", st)
	}
}

// -----------------------------------------------------------------------------
// healthcheck verdicts
// -----------------------------------------------------------------------------

func writeHealthFixture(t *testing.T, st daemonStatus, mtime time.Time, withConfig bool) healthOptions {
	t.Helper()
	dir := t.TempDir()
	o := healthOptions{
		configPath:   dir + "/wg0.conf",
		statusPath:   dir + "/status.json",
		maxStatusAge: 10 * time.Minute,
		maxUnhealthy: 10 * time.Minute,
		expiryGrace:  time.Hour,
		now:          mtime,
	}
	if withConfig {
		if err := os.WriteFile(o.configPath, []byte("[Interface]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeStatus(o.statusPath, st); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(o.statusPath, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestHealthcheck(t *testing.T) {
	now := time.Now().UTC()
	healthy := daemonStatus{Mode: "refresh", TunnelHealthy: true, ExpectedExpiryUTC: now.Add(6 * time.Hour)}

	cases := []struct {
		name       string
		st         daemonStatus
		mtimeAgo   time.Duration
		noConfig   bool
		wantErr    string // substring; "" means healthy
		mutateOpts func(*healthOptions)
	}{
		{name: "healthy", st: healthy},
		{name: "missing config", st: healthy, noConfig: true, wantErr: "wg0.conf is missing"},
		{name: "stale status means the loop is stuck", st: healthy, mtimeAgo: 30 * time.Minute, wantErr: "looks stuck"},
		{
			name: "brief outage during a normal refresh does not flap",
			st:   daemonStatus{Mode: "refresh", TunnelHealthy: false, UnhealthySinceUTC: now.Add(-90 * time.Second), ExpectedExpiryUTC: now.Add(6 * time.Hour)},
		},
		{
			name:    "sustained outage is unhealthy",
			st:      daemonStatus{Mode: "refresh", TunnelHealthy: false, UnhealthySinceUTC: now.Add(-25 * time.Minute)},
			wantErr: "unhealthy for",
		},
		{
			name:    "unhealthy with no recorded start (before the first sample)",
			st:      daemonStatus{Mode: "refresh", TunnelHealthy: false},
			wantErr: "no outage start",
		},
		{
			name: "slightly past expiry is tolerated while the refresh is due",
			st:   daemonStatus{Mode: "refresh", TunnelHealthy: true, ExpectedExpiryUTC: now.Add(-20 * time.Minute)},
		},
		{
			name:    "far past expiry means the proactive refresh is stuck",
			st:      daemonStatus{Mode: "refresh", TunnelHealthy: true, ExpectedExpiryUTC: now.Add(-3 * time.Hour)},
			wantErr: "past its expected expiry",
		},
		{name: "no expiry known yet is fine", st: daemonStatus{Mode: "refresh", TunnelHealthy: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := writeHealthFixture(t, tc.st, now.Add(-tc.mtimeAgo), !tc.noConfig)
			o.now = now
			err := evaluateHealth(o)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("expected healthy, got %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("expected an error containing %q, got healthy", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestHealthcheck_MissingAndCorruptStatus(t *testing.T) {
	dir := t.TempDir()
	o := healthOptions{configPath: dir + "/wg0.conf", statusPath: dir + "/status.json", maxStatusAge: time.Hour, now: time.Now()}
	if err := os.WriteFile(o.configPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := evaluateHealth(o); err == nil || !strings.Contains(err.Error(), "status.json is missing") {
		t.Fatalf("missing status.json: got %v", err)
	}
	if err := os.WriteFile(o.statusPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := evaluateHealth(o); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("corrupt status.json: got %v", err)
	}
}
