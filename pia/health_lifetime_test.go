package pia

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLifetime_SafeRefreshInterval_FallsBackBeforeMeasurement(t *testing.T) {
	rec := LifetimeRecord{Version: lifetimeRecordVersion}
	// No measurement yet -> uses configured fallback.
	got := rec.SafeRefreshInterval(12*time.Hour, 0)
	if got != 12*time.Hour {
		t.Fatalf("expected fallback 12h before measurement, got %s", got)
	}
}

func TestLifetime_SafeRefreshInterval_UsesHalfOfMeasured(t *testing.T) {
	// Observed 11h45m lifetime (the real-world eviction we saw).
	rec := LifetimeRecord{
		Version:                  lifetimeRecordVersion,
		ObservedLifetimesSeconds: []int64{int64((11*time.Hour + 45*time.Minute).Seconds())},
	}
	got := rec.SafeRefreshInterval(12*time.Hour, 0)
	want := time.Duration(float64(11*time.Hour+45*time.Minute) * 0.5)
	if got != want {
		t.Fatalf("expected <=50%% of measured lifetime (%s), got %s", want, got)
	}
	// Crucially, it must be shorter than the configured 12h that failed.
	if got >= 12*time.Hour {
		t.Fatalf("lifetime-aware interval must be shorter than the 12h guess, got %s", got)
	}
}

func TestLifetime_SafeRefreshInterval_TakesShortestObserved(t *testing.T) {
	rec := LifetimeRecord{
		Version:                  lifetimeRecordVersion,
		ObservedLifetimesSeconds: []int64{int64((20 * time.Hour).Seconds()), int64((9 * time.Hour).Seconds()), int64((15 * time.Hour).Seconds())},
	}
	life, ok := rec.MeasuredLifetime()
	if !ok || life != 9*time.Hour {
		t.Fatalf("expected shortest observed lifetime 9h, got %s ok=%t", life, ok)
	}
}

func TestLifetime_UserMaxAgeCapsInterval(t *testing.T) {
	rec := LifetimeRecord{
		Version:                  lifetimeRecordVersion,
		ObservedLifetimesSeconds: []int64{int64((20 * time.Hour).Seconds())},
	}
	// User forces max-registration-age=5h -> interval capped at 2h30m.
	got := rec.SafeRefreshInterval(12*time.Hour, 5*time.Hour)
	want := time.Duration(float64(5*time.Hour) * 0.5)
	if got != want {
		t.Fatalf("expected user cap to force %s, got %s", want, got)
	}
}

func TestLifetime_IntervalNeverBelowFloor(t *testing.T) {
	rec := LifetimeRecord{
		Version:                  lifetimeRecordVersion,
		ObservedLifetimesSeconds: []int64{int64((10 * time.Minute).Seconds())}, // absurdly short
	}
	got := rec.SafeRefreshInterval(12*time.Hour, 0)
	if got < minLifetimeAwareRefresh {
		t.Fatalf("interval must not drop below floor %s, got %s", minLifetimeAwareRefresh, got)
	}
}

func TestLifetimeStore_RoundTripAndObservedLoss(t *testing.T) {
	dir := t.TempDir()
	store := NewLifetimeStore(dir)

	birth := time.Date(2026, 10, 27, 21, 4, 8, 0, time.UTC)
	if _, err := store.RecordNewRegistration(birth); err != nil {
		t.Fatalf("record new registration: %v", err)
	}

	// Handshake lost ~11h45m later.
	loss := birth.Add(11*time.Hour + 45*time.Minute)
	rec, err := store.RecordObservedLoss(loss)
	if err != nil {
		t.Fatalf("record observed loss: %v", err)
	}
	life, ok := rec.MeasuredLifetime()
	if !ok {
		t.Fatalf("expected a measured lifetime after loss")
	}
	if life < 11*time.Hour || life > 12*time.Hour {
		t.Fatalf("expected measured lifetime ~11h45m, got %s", life)
	}

	// Persisted?
	reloaded, err := store.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded.ObservedLifetimesSeconds) != 1 {
		t.Fatalf("expected 1 observed lifetime persisted, got %d", len(reloaded.ObservedLifetimesSeconds))
	}
}

func TestLifetimeStore_CorruptFileStartsFresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, lifetimeRecordFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	rec, err := NewLifetimeStore(dir).Load()
	if err != nil {
		t.Fatalf("corrupt file must not error, got %v", err)
	}
	if rec.Version != lifetimeRecordVersion {
		t.Fatalf("expected fresh record version, got %d", rec.Version)
	}
}

func TestParseEndpointFromConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wg0.conf")
	conf := "[Interface]\nPrivateKey = REDACTED\nAddress = 10.0.0.2\n[Peer]\nEndpoint = 98.159.234.207:1337\n"
	if err := os.WriteFile(path, []byte(conf), 0644); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	got, err := ParseEndpointFromConfig(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != "98.159.234.207:1337" {
		t.Fatalf("expected endpoint parsed, got %q", got)
	}
}

func TestCheckTunnelViaControlServer_RunningIsHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/vpn/status" {
			_ = json.NewEncoder(w).Encode(gluetunStatusResponse{Status: "running"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	state := CheckTunnelViaControlServer(context.Background(), srv.URL, time.Second)
	if !state.Healthy {
		t.Fatalf("expected healthy on status=running, got %+v", state)
	}
}

func TestCheckTunnelViaControlServer_StoppedIsUnhealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(gluetunStatusResponse{Status: "stopped"})
	}))
	defer srv.Close()

	state := CheckTunnelViaControlServer(context.Background(), srv.URL, time.Second)
	if state.Healthy {
		t.Fatalf("expected unhealthy on status=stopped")
	}
	if state.Err == nil {
		t.Fatalf("expected an error explaining the unhealthy state")
	}
}

func TestCheckTunnelViaControlServer_UnreachableIsUnhealthy(t *testing.T) {
	// Port 1 is a closed/unroutable control server — simulates tunnel host down.
	state := CheckTunnelViaControlServer(context.Background(), "http://127.0.0.1:1", 200*time.Millisecond)
	if state.Healthy {
		t.Fatalf("expected unhealthy when control server unreachable")
	}
	if state.Err == nil {
		t.Fatalf("expected an error when control server unreachable")
	}
}

func TestCheckTunnelViaControlServer_LegacyOpenvpnPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/vpn/status" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == "/v1/openvpn/status" {
			_ = json.NewEncoder(w).Encode(gluetunStatusResponse{Status: "running"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	state := CheckTunnelViaControlServer(context.Background(), srv.URL, time.Second)
	if !state.Healthy {
		t.Fatalf("expected healthy via legacy /v1/openvpn/status, got %+v", state)
	}
}
