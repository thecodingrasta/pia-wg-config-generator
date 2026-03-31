//go:build integration

package pia

// Integration tests hit the real PIA API and are skipped automatically when
// PIA_USERNAME / PIA_PASSWORD are not set. They are never run as part of the
// standard `make test` target — use `make test-integration` instead.
//
// Environment variables:
//   PIA_USERNAME  — PIA account username (required)
//   PIA_PASSWORD  — PIA account password (required)
//   PIA_REGION    — Region ID or name to use (default: uk_southampton)
//   PIA_PF        — Set to "1" to also exercise the port-forwarding flow
//
// Design principles:
//   - Each test makes the minimum number of API calls necessary.
//   - Credentials and the server list are fetched once per test; tokens are
//     not shared across tests to avoid expiry races.
//   - No test polls or sleeps — PF bind is exercised but not the renew loop.

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// credsOrSkip returns (username, password) or skips the test if they are absent.
func credsOrSkip(t *testing.T) (string, string) {
	t.Helper()
	username := strings.TrimSpace(os.Getenv("PIA_USERNAME"))
	password := strings.TrimSpace(os.Getenv("PIA_PASSWORD"))
	if username == "" || password == "" {
		t.Skip("Set PIA_USERNAME and PIA_PASSWORD to run integration tests")
	}
	return username, password
}

func regionFromEnv() string {
	if r := strings.TrimSpace(os.Getenv("PIA_REGION")); r != "" {
		return r
	}
	return "uk_southampton"
}

// TestIntegration_GenerateWithMetadata_Works is the primary smoke test.
// It authenticates, registers a key, generates a full config, and validates
// that every critical field is present and sane.
func TestIntegration_GenerateWithMetadata_Works(t *testing.T) {
	username, password := credsOrSkip(t)
	pfEnabled := os.Getenv("PIA_PF") == "1"

	client, err := NewPIAClient(username, password, regionFromEnv(), true, pfEnabled)
	if err != nil {
		t.Fatalf("NewPIAClient: %v", err)
	}

	gen := NewPIAWgGenerator(client, PIAWgGeneratorConfig{
		Verbose:    true,
		ServerName: true,
	})

	res, err := gen.GenerateWithMetadata()
	if err != nil {
		t.Fatalf("GenerateWithMetadata: %v", err)
	}
	if res.Config == "" {
		t.Fatalf("Expected Config, got empty string")
	}

	t.Log("---- WireGuard Config (excerpt) ----")
	t.Log("\n" + excerptConfig(res.Config, 20))
	t.Log("---- End Excerpt ----")

	// Required keys.
	assertConfigContains(t, res.Config, "[Interface]")
	assertConfigContains(t, res.Config, "PrivateKey = ")
	assertConfigContains(t, res.Config, "Address = ")
	assertConfigContains(t, res.Config, "DNS = ")
	assertConfigContains(t, res.Config, "[Peer]")
	assertConfigContains(t, res.Config, "PublicKey = ")
	// Default mode is IPv6ModeOn — dual-stack AllowedIPs.
	assertConfigContains(t, res.Config, "AllowedIPs = 0.0.0.0/0, ::/0")
	assertConfigContains(t, res.Config, "Endpoint = ")

	// ServerCommonName must be a comment, not a bare key.
	if strings.Contains(res.Config, "\nServerCommonName =") {
		t.Fatalf("ServerCommonName must not appear as a bare WireGuard key:\n%s", res.Config)
	}

	// Metadata sanity.
	if strings.TrimSpace(res.Key.ServerIP) == "" {
		t.Fatalf("Expected ServerIP from API, got empty. Key: %+v", res.Key)
	}
	if res.Key.ServerPort <= 0 || res.Key.ServerPort > 65535 {
		t.Fatalf("Invalid ServerPort: %d. Key: %+v", res.Key.ServerPort, res.Key)
	}

	// Config endpoint must match the API metadata exactly.
	wantEndpoint := "Endpoint = " + res.Key.ServerIP + ":" + strconv.Itoa(res.Key.ServerPort)
	if !strings.Contains(res.Config, wantEndpoint) {
		t.Fatalf("Config endpoint does not match API metadata.\nWant: %s\nConfig:\n%s",
			wantEndpoint, res.Config)
	}

	if !pfEnabled {
		return
	}

	// --- Optional: Port-forwarding flow ---
	gw := strings.TrimSpace(res.Key.Gateway)
	if gw == "" {
		gw = strings.TrimSpace(res.Key.ServerVip)
	}
	if gw == "" {
		t.Skip("PF enabled but API returned no gateway/server_vip; cannot validate PF bind")
	}

	token, err := client.GetToken()
	if err != nil {
		t.Fatalf("GetToken (for PF): %v", err)
	}

	pf := NewPFClient(true)

	sig, payload, err := pf.GetSignature(gw, token)
	if err != nil {
		t.Fatalf("GetSignature: %v", err)
	}
	if payload.Port <= 0 || payload.Port > 65535 {
		t.Fatalf("Invalid forwarded port: %d (payload=%+v)", payload.Port, payload)
	}
	if payload.ExpiresAt.IsZero() {
		t.Logf("Warning: payload.ExpiresAt is zero (payload=%+v)", payload)
	}

	if err := pf.BindPort(gw, sig); err != nil {
		t.Fatalf("BindPort: %v", err)
	}

	t.Logf("Port forwarding OK — port %d (expires %s)", payload.Port, payload.ExpiresAt.Format(time.RFC3339))
}

// TestIntegration_GetAvailableRegions_Works validates the server list endpoint.
// It reuses the already-cached server list (no second network call).
func TestIntegration_GetAvailableRegions_Works(t *testing.T) {
	username, password := credsOrSkip(t)

	client, err := NewPIAClient(username, password, regionFromEnv(), false, false)
	if err != nil {
		t.Fatalf("NewPIAClient: %v", err)
	}

	regions, err := client.GetAvailableRegions()
	if err != nil {
		t.Fatalf("GetAvailableRegions: %v", err)
	}
	if len(regions) == 0 {
		t.Fatalf("Expected at least one region, got none")
	}
	if strings.TrimSpace(regions[0].ID) == "" || strings.TrimSpace(regions[0].Name) == "" {
		t.Fatalf("Expected region ID and Name to be set, got: %+v", regions[0])
	}

	t.Logf("Got %d regions; first: %s (%s)", len(regions), regions[0].ID, regions[0].Name)
}

// TestIntegration_ConfigShape_IsValid performs regex-level structural
// validation of a generated config, independently of metadata field values.
func TestIntegration_ConfigShape_IsValid(t *testing.T) {
	username, password := credsOrSkip(t)

	client, err := NewPIAClient(username, password, regionFromEnv(), false, false)
	if err != nil {
		t.Fatalf("NewPIAClient: %v", err)
	}

	res, err := NewPIAWgGenerator(client, PIAWgGeneratorConfig{}).GenerateWithMetadata()
	if err != nil {
		t.Fatalf("GenerateWithMetadata: %v", err)
	}

	cfg := res.Config
	t.Log("---- WireGuard Config (excerpt) ----")
	t.Log("\n" + excerptConfig(cfg, 20))
	t.Log("---- End Excerpt ----")

	mustMatch(t, cfg, `(?m)^\[Interface\]\s*$`)
	mustMatch(t, cfg, `(?m)^PrivateKey = .+$`)
	mustMatch(t, cfg, `(?m)^Address = .+$`)
	mustMatch(t, cfg, `(?m)^DNS = .+$`)
	mustMatch(t, cfg, `(?m)^\[Peer\]\s*$`)
	mustMatch(t, cfg, `(?m)^PublicKey = .+$`)
	// Default IPv6ModeOn → dual-stack.
	mustMatch(t, cfg, `(?m)^AllowedIPs = 0\.0\.0\.0/0, ::/0$`)
	mustMatch(t, cfg, `(?m)^Endpoint = .+:\d+$`)
	mustMatch(t, cfg, `(?m)^PersistentKeepalive = 25$`)

	// Endpoint port must be a valid port number.
	re := regexp.MustCompile(`(?m)^Endpoint = .+:(\d+)$`)
	m := re.FindStringSubmatch(cfg)
	if len(m) != 2 {
		t.Fatalf("Could not extract endpoint port from config:\n%s", cfg)
	}
	port, err := strconv.Atoi(m[1])
	if err != nil || port <= 0 || port > 65535 {
		t.Fatalf("Invalid endpoint port in config: %q", m[1])
	}

	_ = time.Now() // keeps import alive for future assertions
}

// TestIntegration_IPv6Mode_Off_Works validates that IPv6ModeOff produces an
// IPv4-only config with no PostUp/PostDown rules.
func TestIntegration_IPv6Mode_Off_Works(t *testing.T) {
	username, password := credsOrSkip(t)

	client, err := NewPIAClient(username, password, regionFromEnv(), false, false)
	if err != nil {
		t.Fatalf("NewPIAClient: %v", err)
	}

	res, err := NewPIAWgGenerator(client, PIAWgGeneratorConfig{
		ServerName: true,
		IPv6Mode:   IPv6ModeOff,
	}).GenerateWithMetadata()
	if err != nil {
		t.Fatalf("GenerateWithMetadata: %v", err)
	}

	t.Log("---- WireGuard Config (excerpt) ----")
	t.Log("\n" + excerptConfig(res.Config, 22))
	t.Log("---- End Excerpt ----")

	assertConfigContains(t, res.Config, "AllowedIPs = 0.0.0.0/0")
	if strings.Contains(res.Config, "::/0") {
		t.Fatalf("IPv6ModeOff: must not contain ::/0:\n%s", res.Config)
	}
	if regexp.MustCompile(`(?m)^PostUp =`).MatchString(res.Config) {
		t.Fatalf("IPv6ModeOff: must not contain PostUp:\n%s", res.Config)
	}
	if regexp.MustCompile(`(?m)^PostDown =`).MatchString(res.Config) {
		t.Fatalf("IPv6ModeOff: must not contain PostDown:\n%s", res.Config)
	}
}

// TestIntegration_IPv6Mode_Kill_WritesPostUpDown validates that IPv6ModeKill
// produces dual-stack AllowedIPs and ip6tables killswitch PostUp/PostDown rules.
func TestIntegration_IPv6Mode_Kill_WritesPostUpDown(t *testing.T) {
	username, password := credsOrSkip(t)

	client, err := NewPIAClient(username, password, regionFromEnv(), false, false)
	if err != nil {
		t.Fatalf("NewPIAClient: %v", err)
	}

	res, err := NewPIAWgGenerator(client, PIAWgGeneratorConfig{
		ServerName: true,
		IPv6Mode:   IPv6ModeKill,
	}).GenerateWithMetadata()
	if err != nil {
		t.Fatalf("GenerateWithMetadata: %v", err)
	}

	t.Log("---- WireGuard Config (excerpt) ----")
	t.Log("\n" + excerptConfig(res.Config, 28))
	t.Log("---- End Excerpt ----")

	assertConfigContains(t, res.Config, "AllowedIPs = 0.0.0.0/0, ::/0")
	mustMatch(t, res.Config, `(?m)^PostUp = .+$`)
	mustMatch(t, res.Config, `(?m)^PostDown = .+$`)

	if !strings.Contains(res.Config, "ip6tables") {
		t.Fatalf("IPv6ModeKill: expected ip6tables in PostUp/PostDown:\n%s", res.Config)
	}
}

// ---------- Helpers ----------

func assertConfigContains(t *testing.T, cfg string, needle string) {
	t.Helper()
	if !strings.Contains(cfg, needle) {
		t.Fatalf("Expected config to contain %q\nConfig:\n%s", needle, cfg)
	}
}

func mustMatch(t *testing.T, cfg string, pattern string) {
	t.Helper()
	re := regexp.MustCompile(pattern)
	if !re.MatchString(cfg) {
		t.Fatalf("Expected config to match %q\nConfig:\n%s", pattern, cfg)
	}
}

func excerptConfig(cfg string, maxLines int) string {
	lines := strings.Split(cfg, "\n")
	if len(lines) <= maxLines {
		return cfg
	}
	return strings.Join(lines[:maxLines], "\n") + "\n..."
}
