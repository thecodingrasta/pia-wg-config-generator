package pia

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// TunnelState is a point-in-time assessment of whether the active WireGuard
// tunnel is alive. It is produced OUTSIDE gluetun's network namespace by
// querying gluetun's HTTP control server, so it works even when the tunnel
// itself is dead - which is exactly the condition the refresh daemon must
// detect and repair.
//
// Why two signals. Measured against gluetun v3.41.3 (see docs/gluetun.md,
// "Detecting a silent eviction"): after PIA silently drops the peer, gluetun
// keeps restarting its internal VPN loop against the SAME dead config every ~6
// seconds. Its /v1/vpn/status therefore reads "running" for ~95% of the time
// and a once-a-minute poll almost never sees a different value. What does stay
// stable is /v1/publicip/ip, which is empty for as long as the tunnel is not
// carrying traffic. A tunnel is only healthy when BOTH agree.
type TunnelState struct {
	// Healthy is true when gluetun reports the VPN as "running" AND, where the
	// control server can tell us, the tunnel has a public IP. It is the single
	// signal the refresh loop acts on (after debouncing).
	Healthy bool
	// Status is gluetun's raw reported status ("running", "stopped", ...).
	Status string
	// PublicIPKnown is false when /v1/publicip/ip could not be read (older
	// gluetun, authentication required, transport error). The public-IP signal
	// is then ignored rather than guessed, and the state degrades to the
	// status-only behaviour.
	PublicIPKnown bool
	// PublicIPPresent is meaningful only when PublicIPKnown is true.
	PublicIPPresent bool
	// Endpoint is the WireGuard peer endpoint parsed from wg0.conf, used only
	// for logging/observability.
	Endpoint string
	// Err carries the reason the state could not be positively confirmed
	// healthy. A non-nil Err with Healthy=false is the recovery trigger.
	Err error
}

// gluetunStatusResponse is the shape of gluetun's control-server
// GET /v1/vpn/status (and the legacy /v1/openvpn/status) response.
type gluetunStatusResponse struct {
	Status string `json:"status"`
}

// gluetunPublicIPResponse is the shape of GET /v1/publicip/ip. Only public_ip
// is read; the geolocation fields are deliberately ignored.
type gluetunPublicIPResponse struct {
	PublicIP string `json:"public_ip"`
}

// CheckTunnelViaControlServer asks gluetun's HTTP control server whether the
// VPN is up. controlURL is the base URL of the control server reachable from
// the refresh container (e.g. http://127.0.0.1:8000 when the refresher shares
// the host's loopback, or http://gluetun:8000 on a shared user network).
//
// This is served by gluetun on the LAN side, so it stays reachable when the
// tunnel is down. It never enters gluetun's namespace, which is the whole point
// of the config/PF split.
func CheckTunnelViaControlServer(ctx context.Context, controlURL string, timeout time.Duration) TunnelState {
	base := strings.TrimRight(strings.TrimSpace(controlURL), "/")
	if base == "" {
		return TunnelState{Err: errors.New("gluetun control server URL is empty")}
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	client := &http.Client{Timeout: timeout}

	// /v1/vpn/status is the current endpoint; /v1/openvpn/status is the legacy
	// alias still served for compatibility. Try the modern one first.
	for _, path := range []string{"/v1/vpn/status", "/v1/openvpn/status"} {
		status, err := fetchGluetunStatus(ctx, client, base+path)
		if err != nil {
			continue
		}

		state := TunnelState{Status: status}
		if !strings.EqualFold(status, "running") {
			state.Err = fmt.Errorf("gluetun reports VPN status %q", status)
			return state
		}

		// Status says running. Confirm the tunnel actually carries traffic.
		ip, ipErr := fetchGluetunPublicIP(ctx, client, base+"/v1/publicip/ip")
		if ipErr != nil {
			// Cannot tell. Do not invent a failure: fall back to status-only.
			state.Healthy = true
			return state
		}
		state.PublicIPKnown = true
		state.PublicIPPresent = ip != ""
		if !state.PublicIPPresent {
			state.Err = errors.New("gluetun reports VPN status \"running\" but has no public IP (tunnel is not passing traffic)")
			return state
		}
		state.Healthy = true
		return state
	}

	return TunnelState{Err: errors.New("gluetun control server did not return a usable VPN status on /v1/vpn/status or /v1/openvpn/status")}
}

func fetchGluetunStatus(ctx context.Context, client *http.Client, url string) (string, error) {
	var decoded gluetunStatusResponse
	if err := getJSON(ctx, client, url, &decoded); err != nil {
		return "", err
	}
	status := strings.TrimSpace(decoded.Status)
	if status == "" {
		return "", fmt.Errorf("control server %s returned an empty status", url)
	}
	return status, nil
}

// fetchGluetunPublicIP returns the tunnel's public IP, or "" when gluetun has
// none. A non-nil error means the value could not be read at all (distinct from
// "read successfully and it is empty").
func fetchGluetunPublicIP(ctx context.Context, client *http.Client, url string) (string, error) {
	var decoded gluetunPublicIPResponse
	if err := getJSON(ctx, client, url, &decoded); err != nil {
		return "", err
	}
	return strings.TrimSpace(decoded.PublicIP), nil
}

func getJSON(ctx context.Context, client *http.Client, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("control server %s returned HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ParseEndpointFromConfig extracts the "Endpoint = host:port" value from a
// WireGuard config file. Returns an empty string (and no error) when the file
// exists but has no Endpoint line, so callers can treat it as "unknown"
// without failing. A missing file IS an error - the refresher needs to know a
// config has never been written.
func ParseEndpointFromConfig(configPath string) (string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(trimmed), "endpoint") {
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1]), nil
			}
		}
	}
	return "", nil
}

// RegistrationAge returns how long ago the current registration was created,
// given the recorded birth time. A zero birthUTC yields a zero duration and
// ok=false, signalling the age is unknown (e.g. first run before any refresh).
func RegistrationAge(birthUTC time.Time, now time.Time) (age time.Duration, ok bool) {
	if birthUTC.IsZero() {
		return 0, false
	}
	return now.UTC().Sub(birthUTC.UTC()), true
}
