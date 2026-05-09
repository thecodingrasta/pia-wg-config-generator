package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thecodingrasta/pia-wg-config-generator/pia"
	cli "github.com/urfave/cli/v2"
)

const (
	defaultAppName          = "pia-wg-config"
	defaultRegion           = "amsterdam404"
	defaultDaemonInterval   = 12 * time.Hour
	defaultDaemonJitterMax  = 30 * time.Minute
	defaultDaemonRetryDelay = 5 * time.Minute
	defaultGatewayTimeout   = 2 * time.Minute
	defaultGatewayInterval  = 5 * time.Second
	defaultStatusFileName   = "status.json"
	defaultConfigFileName   = "wg0.conf"
	defaultForwardedPortOut = "forwarded_port"

	// PIA requires BindPort roughly every 15 minutes. We renew at 14m to be safe.
	defaultPFRenewInterval = 14 * time.Minute
)

// pfLeaseState holds the data needed to renew a port-forwarding lease.
// It is written by the main daemon goroutine and read by the renew goroutine,
// so all access must go through the helper methods that hold the mutex.
type pfLeaseState struct {
	mu        sync.Mutex
	enabled   bool
	gateway   string
	signature pia.PFSignatureResponse
	port      string
}

func (s *pfLeaseState) set(gateway string, sig pia.PFSignatureResponse, port string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = true
	s.gateway = gateway
	s.signature = sig
	s.port = port
}

func (s *pfLeaseState) disable() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = false
}

// snapshot returns a consistent copy of the lease state for the renew goroutine.
func (s *pfLeaseState) snapshot() (enabled bool, gateway string, sig pia.PFSignatureResponse, port string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled, s.gateway, s.signature, s.port
}

func main() {
	app := buildApp()

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

func buildApp() *cli.App {
	app := &cli.App{
		Name:      defaultAppName,
		Usage:     "Generate and manage WireGuard configs for Private Internet Access (PIA)",
		UsageText: buildUsageText(),
		Flags:     buildGenerateFlags(),
		Commands: []*cli.Command{
			buildGenerateCommand(),
			buildRegionsCommand(),
			buildDaemonCommand(),
		},
		// Backwards compat: `pia-wg-config -o wg0.conf USER PASS` still works.
		Action: func(c *cli.Context) error {
			return runGenerate(c)
		},
	}

	return app
}

func buildUsageText() string {
	return `pia-wg-config [global options] command [command options]

Examples:
  pia-wg-config generate --username USER --password PASS --region nl_amsterdam -o wg0.conf
  pia-wg-config gen --username USER --password PASS
  pia-wg-config --username USER --password PASS
  pia-wg-config regions --username USER --password PASS --pf-only`
}

// ---------- Shared Flags/Helpers ----------

func buildAuthFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "username",
			Aliases: []string{"u"},
			Usage:   "PIA Username (or set PIA_USERNAME env var)",
			EnvVars: []string{"PIA_USERNAME"},
		},
		&cli.StringFlag{
			Name:    "password",
			Aliases: []string{"w"},
			Usage:   "PIA Password (or set PIA_PASSWORD env var)",
			EnvVars: []string{"PIA_PASSWORD"},
		},
		&cli.BoolFlag{
			Name:    "verbose",
			Aliases: []string{"v"},
			Value:   false,
			Usage:   "Enable Verbose Output",
		},
	}
}

func buildIPv6Flag() cli.Flag {
	return &cli.StringFlag{
		Name:  "ipv6-mode",
		Usage: "IPv6 routing mode: 'on' (route IPv6 through VPN, default), 'off' (IPv4 only), 'kill' (route IPv6 + ip6tables killswitch)",
		Value: "on",
	}
}

func buildGenerateFlags() []cli.Flag {
	return append(buildAuthFlags(),
		buildIPv6Flag(),
		&cli.StringFlag{
			Name:    "outfile",
			Aliases: []string{"o"},
			Usage:   "File to write the WireGuard config to (prints to stdout if omitted)",
		},
		&cli.StringFlag{
			Name:    "region",
			Aliases: []string{"r"},
			Value:   defaultRegion,
			Usage:   "PIA region ID, friendly name, or server common name (e.g. nl_amsterdam, \"Netherlands\", or amsterdam404)",
		},
		&cli.BoolFlag{
			Name:    "server",
			Aliases: []string{"s"},
			Value:   false,
			Usage:   "Include the server's common name as a comment in the config",
		},
		&cli.BoolFlag{
			Name:    "port-forwarding",
			Aliases: []string{"p"},
			Value:   false,
			Usage:   "Only pick servers that support port forwarding",
		},
	)
}

func parseIPv6Mode(s string) (pia.IPv6Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "":
		return pia.IPv6ModeOn, nil
	case "off":
		return pia.IPv6ModeOff, nil
	case "kill":
		return pia.IPv6ModeKill, nil
	default:
		return 0, fmt.Errorf("unknown --ipv6-mode %q: must be 'on', 'off', or 'kill'", s)
	}
}

func resolveCredentials(c *cli.Context) (string, string, error) {
	username := strings.TrimSpace(c.Args().Get(0))
	password := strings.TrimSpace(c.Args().Get(1))

	if username == "" {
		username = strings.TrimSpace(c.String("username"))
	}
	if password == "" {
		password = strings.TrimSpace(c.String("password"))
	}

	if username == "" || password == "" {
		return "", "", errors.New("Missing Credentials: Provide USERNAME PASSWORD args or set --username/--password (or env PIA_USERNAME/PIA_PASSWORD)")
	}

	return username, password, nil
}

func mustStatePath(dir string, name string) string {
	return filepath.Join(dir, name)
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}

	tmpName := tmpFile.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}

	if err := tmpFile.Chmod(perm); err != nil {
		_ = tmpFile.Close()
		return err
	}

	if err := tmpFile.Close(); err != nil {
		return err
	}

	return os.Rename(tmpName, path)
}

func runHook(command string, port string, verbose bool) error {
	if strings.TrimSpace(command) == "" {
		return nil
	}

	expanded := strings.ReplaceAll(command, "{port}", port)
	if verbose {
		log.Printf("Running Hook: %s", expanded)
	}

	cmd := exec.Command("sh", "-c", expanded)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runConfigHook(command string, stateDir string, configPath string, forwardedPortPath string, port string, verbose bool) error {
	if strings.TrimSpace(command) == "" {
		return nil
	}

	expanded := expandConfigHookCommand(command, stateDir, configPath, forwardedPortPath, port)
	if verbose {
		log.Printf("Running Config Hook: %s", expanded)
	}

	cmd := exec.Command("sh", "-c", expanded)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func expandConfigHookCommand(command string, stateDir string, configPath string, forwardedPortPath string, port string) string {
	replacements := map[string]string{
		"{state_dir}":      stateDir,
		"{config}":         configPath,
		"{forwarded_port}": forwardedPortPath,
		"{port}":           port,
	}

	expanded := command
	for old, new := range replacements {
		expanded = strings.ReplaceAll(expanded, old, new)
	}
	return expanded
}

// ---------- generate ----------

func buildGenerateCommand() *cli.Command {
	return &cli.Command{
		Name:      "generate",
		Aliases:   []string{"gen"},
		Usage:     "Generate a WireGuard config for PIA",
		UsageText: "pia-wg-config generate --username USER --password PASS [options]",
		Flags:     buildGenerateFlags(),
		Action:    runGenerate,
	}
}

func runGenerate(c *cli.Context) error {
	username, password, err := resolveCredentials(c)
	if err != nil {
		return err
	}

	verbose := c.Bool("verbose")
	serverName := c.Bool("server")
	portForwarding := c.Bool("port-forwarding")

	region := strings.TrimSpace(c.String("region"))
	if region == "" {
		region = defaultRegion
	}

	ipv6Mode, err := parseIPv6Mode(c.String("ipv6-mode"))
	if err != nil {
		return err
	}

	if verbose {
		log.Print("Creating PIA Client")
	}
	piaClient, err := pia.NewPIAClient(username, password, region, verbose, portForwarding)
	if err != nil {
		return err
	}

	if verbose {
		log.Print("Creating WG Config Generator")
	}
	wgConfigGenerator := pia.NewPIAWgGenerator(
		piaClient,
		pia.PIAWgGeneratorConfig{Verbose: verbose, ServerName: serverName, IPv6Mode: ipv6Mode},
	)

	if verbose {
		log.Print("Generating WireGuard Config")
	}
	gen, err := wgConfigGenerator.GenerateWithMetadata()
	if err != nil {
		return err
	}

	outfile := strings.TrimSpace(c.String("outfile"))
	if outfile != "" {
		if err := atomicWriteFile(outfile, []byte(gen.Config), 0644); err != nil {
			return err
		}
		if verbose {
			log.Printf("Successfully Wrote Config: %s", outfile)
		}
		return nil
	}

	_, err = io.WriteString(os.Stdout, gen.Config)
	return err
}

// ---------- regions ----------

type regionRow struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	PortForwarding bool   `json:"port_forwarding"`
}

func buildRegionsCommand() *cli.Command {
	return &cli.Command{
		Name:  "regions",
		Usage: "List available PIA regions",
		Flags: append(buildAuthFlags(),
			&cli.BoolFlag{
				Name:  "pf-only",
				Usage: "Only show regions that support port forwarding",
				Value: false,
			},
			&cli.BoolFlag{
				Name:  "json",
				Usage: "Output as JSON",
				Value: false,
			},
		),
		Action: runRegions,
	}
}

func runRegions(c *cli.Context) error {
	username, password, err := resolveCredentials(c)
	if err != nil {
		return err
	}

	verbose := c.Bool("verbose")
	pfOnly := c.Bool("pf-only")
	asJSON := c.Bool("json")

	piaClient, err := pia.NewPIAClient(username, password, defaultRegion, verbose, pfOnly)
	if err != nil {
		return err
	}

	regions, err := piaClient.GetAvailableRegions()
	if err != nil {
		return err
	}

	rows := make([]regionRow, 0, len(regions))
	for _, r := range regions {
		row := regionRow{
			ID:             r.ID,
			Name:           r.Name,
			PortForwarding: r.PortForward,
		}
		if pfOnly && !row.PortForwarding {
			continue
		}
		rows = append(rows, row)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	for _, row := range rows {
		pf := "no"
		if row.PortForwarding {
			pf = "yes"
		}
		fmt.Printf("%s\t%s\tpf:%s\n", row.ID, row.Name, pf)
	}

	return nil
}

// ---------- Daemon ----------

type daemonStatus struct {
	Region            string    `json:"region"`
	LastGenerateUTC   time.Time `json:"last_generate_utc"`
	LastForwardPort   string    `json:"last_forward_port,omitempty"`
	LastError         string    `json:"last_error,omitempty"`
	NextRefreshUTC    time.Time `json:"next_refresh_utc"`
	PortForwarding    bool      `json:"port_forwarding"`
	ServerNameEnabled bool      `json:"server_name_enabled"`
	IPv6Mode          string    `json:"ipv6_mode"`
}

func buildDaemonCommand() *cli.Command {
	return &cli.Command{
		Name:  "daemon",
		Usage: "Run continuously: refresh WireGuard configs and maintain port forwarding",
		Flags: append(buildAuthFlags(),
			buildIPv6Flag(),
			&cli.StringFlag{
				Name:  "state-dir",
				Usage: "Directory to write state files (config, status, forwarded port)",
				Value: "/state",
			},
			&cli.StringFlag{
				Name:    "region",
				Aliases: []string{"r"},
				Value:   defaultRegion,
				Usage:   "PIA Region ID or friendly name",
			},
			&cli.BoolFlag{
				Name:    "server",
				Aliases: []string{"s"},
				Value:   false,
				Usage:   "Include server common name as a comment in the config",
			},
			&cli.BoolFlag{
				Name:    "port-forwarding",
				Aliases: []string{"p"},
				Value:   false,
				Usage:   "Acquire and renew a PIA port forwarding lease",
			},
			&cli.DurationFlag{
				Name:  "refresh-interval",
				Usage: "How often to regenerate the WireGuard config (jitter is applied)",
				Value: defaultDaemonInterval,
			},
			&cli.DurationFlag{
				Name:  "refresh-jitter",
				Usage: "Max jitter applied to the refresh interval",
				Value: defaultDaemonJitterMax,
			},
			&cli.DurationFlag{
				Name:  "retry-delay",
				Usage: "How long to wait before retrying after a failed refresh",
				Value: defaultDaemonRetryDelay,
			},
			&cli.BoolFlag{
				Name:  "wait-for-gateway",
				Usage: "Wait for the PIA port-forwarding gateway to be reachable before requesting a forwarded port",
				Value: true,
			},
			&cli.DurationFlag{
				Name:  "gateway-timeout",
				Usage: "Maximum time to wait for the PIA port-forwarding gateway after a config refresh",
				Value: defaultGatewayTimeout,
			},
			&cli.DurationFlag{
				Name:  "gateway-check-interval",
				Usage: "How often to check the PIA port-forwarding gateway while waiting",
				Value: defaultGatewayInterval,
			},
			&cli.StringFlag{
				Name:  "on-port-change",
				Usage: "Hook command to run when the forwarded port changes. Use {port} placeholder.",
				Value: "",
			},
			&cli.StringFlag{
				Name:  "on-config-change",
				Usage: "Hook command to run after each successful config refresh. Placeholders: {config}, {state_dir}, {forwarded_port}, {port}.",
				Value: "",
			},
		),
		Action: runDaemon,
	}
}

func runDaemon(c *cli.Context) error {
	username, password, err := resolveCredentials(c)
	if err != nil {
		return err
	}

	verbose := c.Bool("verbose")
	serverName := c.Bool("server")
	portForwarding := c.Bool("port-forwarding")
	region := strings.TrimSpace(c.String("region"))
	stateDir := strings.TrimSpace(c.String("state-dir"))
	portHookCmd := c.String("on-port-change")
	configHookCmd := c.String("on-config-change")

	if region == "" {
		region = defaultRegion
	}

	ipv6Mode, err := parseIPv6Mode(c.String("ipv6-mode"))
	if err != nil {
		return err
	}

	configPath := mustStatePath(stateDir, defaultConfigFileName)
	statusPath := mustStatePath(stateDir, defaultStatusFileName)
	forwardedPortPath := mustStatePath(stateDir, defaultForwardedPortOut)

	interval := c.Duration("refresh-interval")
	jitterMax := c.Duration("refresh-jitter")
	retryDelay := c.Duration("retry-delay")
	waitForGatewayEnabled := c.Bool("wait-for-gateway")
	gatewayTimeout := c.Duration("gateway-timeout")
	gatewayInterval := c.Duration("gateway-check-interval")

	if verbose {
		log.Printf("Daemon Starting. region=%s stateDir=%s interval=%s retryDelay=%s waitForGateway=%t gatewayTimeout=%s ipv6Mode=%s",
			region, stateDir, interval, retryDelay, waitForGatewayEnabled, gatewayTimeout, c.String("ipv6-mode"))
	}

	// Shared port-forwarding lease state — written by this goroutine,
	// read by the renew goroutine via snapshot(). Mutex protected.
	lease := &pfLeaseState{}
	startPFRenewLoop(lease, verbose)

	var lastPort string

	for {
		next := time.Now().UTC().Add(applyJitter(interval, jitterMax))

		status := daemonStatus{
			Region:            region,
			LastGenerateUTC:   time.Now().UTC(),
			NextRefreshUTC:    next,
			PortForwarding:    portForwarding,
			ServerNameEnabled: serverName,
			IPv6Mode:          c.String("ipv6-mode"),
		}

		piaClient, clientErr := pia.NewPIAClient(username, password, region, verbose, portForwarding)
		if clientErr != nil {
			status.LastError = clientErr.Error()
			_ = writeStatus(statusPath, status)
			sleepAfterFailure("client setup failed", clientErr, retryDelay, verbose)
			continue
		}

		wgConfigGenerator := pia.NewPIAWgGenerator(
			piaClient,
			pia.PIAWgGeneratorConfig{Verbose: verbose, ServerName: serverName, IPv6Mode: ipv6Mode},
		)

		gen, genErr := wgConfigGenerator.GenerateWithMetadata()
		if genErr != nil {
			status.LastError = genErr.Error()
			_ = writeStatus(statusPath, status)
			sleepAfterFailure("config generation failed", genErr, retryDelay, verbose)
			continue
		}

		if err := atomicWriteFile(configPath, []byte(gen.Config), 0644); err != nil {
			status.LastError = err.Error()
			_ = writeStatus(statusPath, status)
			sleepAfterFailure("config write failed", err, retryDelay, verbose)
			continue
		}

		currentPort := lastPort
		var refreshErr error
		var refreshErrStage string
		if err := runConfigHook(configHookCmd, stateDir, configPath, forwardedPortPath, currentPort, verbose); err != nil {
			refreshErr = err
			refreshErrStage = "config hook failed"
			status.LastError = err.Error()
			lease.disable()
			if verbose {
				log.Printf("Config Hook Failed: %v", err)
			}
		}

		if refreshErr == nil && portForwarding {
			gateway, gatewayErr := gatewayFromKeyMeta(gen.Key)
			if gatewayErr != nil {
				refreshErr = gatewayErr
				refreshErrStage = "port forwarding gateway missing"
				status.LastError = gatewayErr.Error()
				lease.disable()
			} else if waitForGatewayEnabled {
				if err := waitForGateway(gateway, gatewayTimeout, gatewayInterval, verbose); err != nil {
					refreshErr = err
					refreshErrStage = "port forwarding gateway wait failed"
					status.LastError = err.Error()
					lease.disable()
				}
			}
		}

		if refreshErr == nil && portForwarding {
			portStr, sig, gw, pfErr := acquireAndBindPort(piaClient, gen.Key, verbose)
			if pfErr != nil {
				refreshErr = pfErr
				refreshErrStage = "port forwarding failed"
				status.LastError = pfErr.Error()
				lease.disable()
			} else {

				currentPort = portStr
				status.LastForwardPort = portStr

				if err := atomicWriteFile(forwardedPortPath, []byte(portStr), 0644); err != nil {
					refreshErr = err
					refreshErrStage = "forwarded port write failed"
					status.LastError = err.Error()
				} else {

					if portStr != lastPort {
						lastPort = portStr
						_ = runHook(portHookCmd, portStr, verbose)
					}

					// Store the gateway + signature for the renew loop.
					// The same signature is reused for every BindPort renewal — do NOT
					// call GetSignature again until the next full config refresh.
					lease.set(gw, sig, portStr)
				}
			}
		}

		_ = writeStatus(statusPath, status)

		if refreshErr != nil {
			if verbose {
				log.Printf("Updated config at %s, but %s: %v", configPath, refreshErrStage, refreshErr)
			}
			sleepAfterFailure(refreshErrStage, refreshErr, retryDelay, verbose)
			continue
		}

		if verbose {
			log.Printf("Updated: %s (pf=%t)", configPath, portForwarding)
		}

		sleepUntil(next, verbose)
	}
}

// acquireAndBindPort authenticates, obtains a port-forwarding signature from
// the gateway, performs the initial bind, and returns the port string, the
// reusable signature, and the normalised gateway address.
// Only ONE GetToken and ONE GetSignature call is made per config refresh.
func acquireAndBindPort(piaClient *pia.PIAClient, keyMeta pia.AddKeyResult, verbose bool) (portStr string, sig pia.PFSignatureResponse, gateway string, err error) {
	gateway, err = gatewayFromKeyMeta(keyMeta)
	if err != nil {
		return "", pia.PFSignatureResponse{}, "", err
	}

	token, err := piaClient.GetToken()
	if err != nil {
		return "", pia.PFSignatureResponse{}, "", err
	}

	pfClient := pia.NewPFClient(verbose)

	sig, payload, err := pfClient.GetSignature(gateway, token)
	if err != nil {
		return "", pia.PFSignatureResponse{}, "", err
	}

	if err := pfClient.BindPort(gateway, sig); err != nil {
		return "", pia.PFSignatureResponse{}, "", err
	}

	return strconv.Itoa(payload.Port), sig, gateway, nil
}

func gatewayFromKeyMeta(keyMeta pia.AddKeyResult) (string, error) {
	gateway := strings.TrimSpace(keyMeta.Gateway)
	if gateway == "" {
		gateway = strings.TrimSpace(keyMeta.ServerVip)
	}
	if gateway == "" {
		return "", errors.New("port forwarding enabled but no gateway/server_vip returned by API")
	}
	return gateway, nil
}

func waitForGateway(gateway string, timeout time.Duration, interval time.Duration, verbose bool) error {
	address := pia.NormalizeGateway(gateway)
	if address == "" {
		return errors.New("port forwarding gateway is empty")
	}

	if interval <= 0 {
		interval = defaultGatewayInterval
	}
	deadline := time.Now().Add(timeout)
	var lastErr error

	for {
		dialTimeout := minDuration(3*time.Second, interval)
		conn, err := net.DialTimeout("tcp", address, dialTimeout)
		if err == nil {
			_ = conn.Close()
			if verbose {
				log.Printf("PIA gateway reachable: %s", address)
			}
			return nil
		}
		lastErr = err

		if timeout <= 0 || !time.Now().Add(interval).Before(deadline) {
			return fmt.Errorf("PIA port-forwarding gateway %s is not reachable through the active tunnel: %w", address, lastErr)
		}

		if verbose {
			log.Printf("Waiting for PIA gateway %s: %v", address, lastErr)
		}
		time.Sleep(interval)
	}
}

func minDuration(a time.Duration, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// startPFRenewLoop starts a background goroutine that calls BindPort every
// defaultPFRenewInterval using whatever lease state is currently active.
// It must be started once; the main loop updates the lease via lease.set().
func startPFRenewLoop(lease *pfLeaseState, verbose bool) {
	go func() {
		ticker := time.NewTicker(defaultPFRenewInterval)
		defer ticker.Stop()

		for range ticker.C {
			enabled, gateway, sig, port := lease.snapshot()
			if !enabled {
				continue
			}

			pfClient := pia.NewPFClient(verbose)
			if err := pfClient.BindPort(gateway, sig); err != nil {
				if verbose {
					log.Printf("PF Renew Failed: %v", err)
				}
				continue
			}

			if verbose {
				log.Printf("PF Renewed (Port %s)", port)
			}
		}
	}()
}

func writeStatus(path string, status daemonStatus) error {
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0644)
}

func sleepUntil(t time.Time, verbose bool) {
	d := time.Until(t)
	if d <= 0 {
		return
	}
	if verbose {
		log.Printf("Sleeping %s Until %s", d.Round(time.Second), t.Format(time.RFC3339))
	}
	time.Sleep(d)
}

func sleepAfterFailure(stage string, err error, d time.Duration, verbose bool) {
	if d <= 0 {
		return
	}
	if verbose {
		if err != nil {
			log.Printf("%s: %v; retrying in %s", stage, err, d.Round(time.Second))
		} else {
			log.Printf("%s; retrying in %s", stage, d.Round(time.Second))
		}
	}
	time.Sleep(d)
}

func applyJitter(interval time.Duration, jitterMax time.Duration) time.Duration {
	if jitterMax <= 0 {
		return interval
	}

	max := big.NewInt(int64(jitterMax))
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return interval
	}

	offset := time.Duration(n.Int64()) - (jitterMax / 2)
	return interval + offset
}
