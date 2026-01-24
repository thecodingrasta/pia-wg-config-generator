package pia

import (
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Validate we can generate a config with metadata
func TestIntegration_GenerateWithMetadata_Works(t *testing.T) {
	username := strings.TrimSpace(os.Getenv("PIA_USERNAME"))
	password := strings.TrimSpace(os.Getenv("PIA_PASSWORD"))
	if username == "" || password == "" {
		t.Skip("Optional: Set PIA_USERNAME and PIA_PASSWORD To Run The Integration Tests")
	}

	region := strings.TrimSpace(os.Getenv("PIA_REGION"))
	if region == "" {
		region = "uk_southampton"
	}

	curlPath := strings.TrimSpace(os.Getenv("CURL_PATH"))
	if curlPath == "" && runtime.GOOS == "windows" {
		curlPath = "openssl_curl.exe"
	}

	// Optional PF Testing
	pfEnabled := os.Getenv("PIA_PF") == "1"

	client, err := NewPIAClient(username, password, curlPath, region, true, pfEnabled)
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
		t.Fatalf("Expected Config Got Empty String")
	}

	excerpt := excerptConfig(res.Config, 20)
	t.Log("---- WireGuard Config (excerpt) ----")
	t.Log("\n" + excerpt)
	t.Log("---- End Excerpt ----")

	// Validate our config has the critical bitz.
	assertConfigContains(t, res.Config, "PrivateKey = ")
	assertConfigContains(t, res.Config, "Address = ")
	assertConfigContains(t, res.Config, "DNS = ")
	assertConfigContains(t, res.Config, "[Peer]")
	assertConfigContains(t, res.Config, "PublicKey = ")
	assertConfigContains(t, res.Config, "AllowedIPs = 0.0.0.0/0, ::/0")
	assertConfigContains(t, res.Config, "Endpoint = ")

	// Validate the endpoint metadata looks real.
	if strings.TrimSpace(res.Key.ServerIP) == "" {
		t.Fatalf("Expected Server IP From API Got Empty Key: %+v", res.Key)
	}
	if res.Key.ServerPort <= 0 || res.Key.ServerPort > 65535 {
		t.Fatalf("Invalid Server Port From API: %d. Key: %+v", res.Key.ServerPort, res.Key)
	}

	// Confirm the config endpoint matches the metadata exactly.
	if !strings.Contains(res.Config, "Endpoint = "+res.Key.ServerIP+":"+strconv.Itoa(res.Key.ServerPort)) {
		t.Fatalf("Config Endpoint Does Not Match API Metadata. Endpoint=%s:%d\nConfig:\n%s",
			res.Key.ServerIP, res.Key.ServerPort, res.Config)
	}

	// Optional: PF flow validation (real gateway, rolling port).
	if !pfEnabled {
		return
	}

	// The gateway can be in different fields depending on the API.
	gw := strings.TrimSpace(res.Key.Gateway)
	if gw == "" {
		gw = strings.TrimSpace(res.Key.ServerVip)
	}
	if gw == "" {
		t.Skip("PF Is Enabled But The API Did Not Return Gateway/ServerVip; Cannot Validate PF Bind")
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
		t.Fatalf("Invalid Forwarded Port Returned: %d (payload=%+v)", payload.Port, payload)
	}
	if payload.ExpiresAt.IsZero() {
		// Some implementations may omit, but if it exists we want it sane.
		// Not fatal if PIA returns zero, but worth a warning.
		t.Logf("Warning: payload.ExpiresAt is zero (payload=%+v)", payload)
	}

	if err := pf.BindPort(gw, sig); err != nil {
		t.Fatalf("BindPort: %v", err)
	}
}

// Validate we can fetch and parse the regions
func TestIntegration_GetAvailableRegions_Works(t *testing.T) {
	username := strings.TrimSpace(os.Getenv("PIA_USERNAME"))
	password := strings.TrimSpace(os.Getenv("PIA_PASSWORD"))
	if username == "" || password == "" {
		t.Skip("Optional: Set PIA_USERNAME and PIA_PASSWORD To Run The Integration Tests")
	}

	// Init
	client, err := NewPIAClient(username, password, "", "uk_southampton", false, false)
	if err != nil {
		t.Fatalf("NewPIAClient: %v", err)
	}

	// Fetch
	regions, err := client.GetAvailableRegions()
	if err != nil {
		t.Fatalf("GetAvailableRegions: %v", err)
	}
	if len(regions) == 0 {
		t.Fatalf("Expected Some Regions, Got None")
	}

	// Spot check format.
	if strings.TrimSpace(regions[0].ID) == "" || strings.TrimSpace(regions[0].Name) == "" {
		t.Fatalf("Expected Region ID And Name Set, Got: %+v", regions[0])
	}
}

// Validate the structure of a config we've generated
func TestIntegration_ConfigShape_IsValid(t *testing.T) {
	username := strings.TrimSpace(os.Getenv("PIA_USERNAME"))
	password := strings.TrimSpace(os.Getenv("PIA_PASSWORD"))
	if username == "" || password == "" {
		t.Skip("Optional: Set PIA_USERNAME and PIA_PASSWORD To Run The Integration Tests")
	}

	curlPath := strings.TrimSpace(os.Getenv("CURL_PATH"))
	if curlPath == "" && runtime.GOOS == "windows" {
		curlPath = "openssl_curl.exe"
	}

	// Init
	client, err := NewPIAClient(username, password, curlPath, "uk_southampton", false, false)
	if err != nil {
		t.Fatalf("NewPIAClient: %v", err)
	}

	// Generate
	gen := NewPIAWgGenerator(client, PIAWgGeneratorConfig{})
	res, err := gen.GenerateWithMetadata()
	if err != nil {
		t.Fatalf("GenerateWithMetadata: %v", err)
	}

	cfg := res.Config

	// Output an excerpt of the config we generated
	excerpt := excerptConfig(cfg, 20)
	t.Log("---- WireGuard Config (excerpt) ----")
	t.Log("\n" + excerpt)
	t.Log("---- End Excerpt ----")

	// Ensure the keys exist and are non-empty(ish).
	mustMatch(t, cfg, `(?m)^\[Interface\]\s*$`)
	mustMatch(t, cfg, `(?m)^PrivateKey = .+$`)
	mustMatch(t, cfg, `(?m)^Address = .+$`)
	mustMatch(t, cfg, `(?m)^DNS = .+$`)
	mustMatch(t, cfg, `(?m)^\[Peer\]\s*$`)
	mustMatch(t, cfg, `(?m)^PublicKey = .+$`)
	mustMatch(t, cfg, `(?m)^AllowedIPs = 0\.0\.0\.0/0, ::/0$`)
	mustMatch(t, cfg, `(?m)^Endpoint = .+:\d+$`)
	mustMatch(t, cfg, `(?m)^PersistentKeepalive = 25$`)

	// Validate the endpoint port is numeric and in range.
	re := regexp.MustCompile(`(?m)^Endpoint = .+:(\d+)$`)
	m := re.FindStringSubmatch(cfg)
	if len(m) != 2 {
		t.Fatalf("Could Not Extract Endpoint Port From Config:\n%s", cfg)
	}
	port, err := strconv.Atoi(m[1])
	if err != nil || port <= 0 || port > 65535 {
		t.Fatalf("Invalid Endpoint Port In Config: %q", m[1])
	}

	_ = time.Now() // keeps the import available if you extend checks
}

// Validate IPv6 Off Mode Produces IPv4-Only AllowedIPs
func TestIntegration_IPv6Mode_Off_Works(t *testing.T) {
	username := strings.TrimSpace(os.Getenv("PIA_USERNAME"))
	password := strings.TrimSpace(os.Getenv("PIA_PASSWORD"))
	if username == "" || password == "" {
		t.Skip("Optional: Set PIA_USERNAME and PIA_PASSWORD To Run The Integration Tests")
	}

	region := strings.TrimSpace(os.Getenv("PIA_REGION"))
	if region == "" {
		region = "uk_southampton"
	}

	curlPath := strings.TrimSpace(os.Getenv("CURL_PATH"))
	if curlPath == "" && runtime.GOOS == "windows" {
		curlPath = "openssl_curl.exe"
	}

	// PF Off For This Test (We Are Only Validating The Template Output)
	client, err := NewPIAClient(username, password, curlPath, region, false, false)
	if err != nil {
		t.Fatalf("NewPIAClient: %v", err)
	}

	gen := NewPIAWgGenerator(client, PIAWgGeneratorConfig{
		Verbose:    false,
		ServerName: true,
		IPv6Mode:   IPv6ModeOff,
	})

	res, err := gen.GenerateWithMetadata()
	if err != nil {
		t.Fatalf("GenerateWithMetadata: %v", err)
	}

	if res.Config == "" {
		t.Fatalf("Expected Config Got Empty String")
	}

	excerpt := excerptConfig(res.Config, 22)
	t.Log("---- WireGuard Config (excerpt) ----")
	t.Log("\n" + excerpt)
	t.Log("---- End Excerpt ----")

	// Ensure IPv6 Route Is NOT Present
	assertConfigContains(t, res.Config, "AllowedIPs = 0.0.0.0/0")
	if strings.Contains(res.Config, "::/0") {
		t.Fatalf("Expected IPv6 To Be Disabled But Found ::/0 In Config:\n%s", res.Config)
	}

	// Ensure No PostUp/PostDown Are Injected In Off Mode
	if regexp.MustCompile(`(?m)^PostUp =`).MatchString(res.Config) {
		t.Fatalf("Did Not Expect PostUp In IPv6 Off Mode:\n%s", res.Config)
	}
	if regexp.MustCompile(`(?m)^PostDown =`).MatchString(res.Config) {
		t.Fatalf("Did Not Expect PostDown In IPv6 Off Mode:\n%s", res.Config)
	}
}

// Validate IPv6 Kill Mode Writes PostUp/PostDown Rules (wg-quick Style)
func TestIntegration_IPv6Mode_Kill_WritesPostUpDown(t *testing.T) {
	username := strings.TrimSpace(os.Getenv("PIA_USERNAME"))
	password := strings.TrimSpace(os.Getenv("PIA_PASSWORD"))
	if username == "" || password == "" {
		t.Skip("Optional: Set PIA_USERNAME and PIA_PASSWORD To Run The Integration Tests")
	}

	region := strings.TrimSpace(os.Getenv("PIA_REGION"))
	if region == "" {
		region = "uk_southampton"
	}

	curlPath := strings.TrimSpace(os.Getenv("CURL_PATH"))
	if curlPath == "" && runtime.GOOS == "windows" {
		curlPath = "openssl_curl.exe"
	}

	// PF should be off for this test (We're only validating the template output)
	client, err := NewPIAClient(username, password, curlPath, region, false, false)
	if err != nil {
		t.Fatalf("NewPIAClient: %v", err)
	}

	gen := NewPIAWgGenerator(client, PIAWgGeneratorConfig{
		Verbose:    false,
		ServerName: true,
		IPv6Mode:   IPv6ModeKill,
	})

	res, err := gen.GenerateWithMetadata()
	if err != nil {
		t.Fatalf("GenerateWithMetadata: %v", err)
	}

	if res.Config == "" {
		t.Fatalf("Expected Config Got Empty String")
	}

	excerpt := excerptConfig(res.Config, 28)
	t.Log("---- WireGuard Config (excerpt) ----")
	t.Log("\n" + excerpt)
	t.Log("---- End Excerpt ----")

	// In Kill Mode We Keep ::/0 (Then Block IPv6 Egress As A Killswitch)
	assertConfigContains(t, res.Config, "AllowedIPs = 0.0.0.0/0, ::/0")

	// Must Include PostUp/PostDown Lines
	mustMatch(t, res.Config, `(?m)^PostUp = .+$`)
	mustMatch(t, res.Config, `(?m)^PostDown = .+$`)

	// Light Sanity Check That Rules Are Actually IPv6-Focused
	if !strings.Contains(res.Config, "ip6tables") {
		t.Fatalf("Expected ip6tables Rules In Kill Mode But None Were Found:\n%s", res.Config)
	}
}

func assertConfigContains(t *testing.T, cfg string, needle string) {
	t.Helper()
	if !strings.Contains(cfg, needle) {
		t.Fatalf("Expected Config To Contain %q\nConfig:\n%s", needle, cfg)
	}
}

func mustMatch(t *testing.T, cfg string, pattern string) {
	t.Helper()
	re := regexp.MustCompile(pattern)
	if !re.MatchString(cfg) {
		t.Fatalf("Expected Config To Match %q\nConfig:\n%s", pattern, cfg)
	}
}

func excerptConfig(cfg string, maxLines int) string {
	lines := strings.Split(cfg, "\n")
	if len(lines) <= maxLines {
		return cfg
	}
	return strings.Join(lines[:maxLines], "\n") + "\n..."
}
