package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thecodingrasta/pia-wg-config-generator/pia"
	cli "github.com/urfave/cli/v2"
)

const (
	defaultAppName          = "pia-wg-config"
	defaultRegion           = "amsterdam404"
	defaultDaemonInterval   = 12 * time.Hour
	defaultDaemonJitterMax  = 30 * time.Minute
	defaultStatusFileName   = "status.json"
	defaultConfigFileName   = "wg0.conf"
	defaultForwardedPortOut = "forwarded_port"

	// PIA requires bindPort roughly every 15m. We renew at 14m to be safe.
	defaultPFRenewInterval = 14 * time.Minute
)

type pfLeaseState struct {
	Enabled   bool
	Gateway   string
	Signature pia.PFSignatureResponse
	Port      string
}

func main() {
	app := &cli.App{
		Name:  defaultAppName,
		Usage: "Generate and manage WireGuard configs for Private Internet Access (PIA)",
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

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
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

// ---------- generate ----------

func buildGenerateCommand() *cli.Command {
	return &cli.Command{
		Name:    "generate",
		Aliases: []string{"gen"},
		Usage:   "Generate a WireGuard config for PIA",
		Flags: append(buildAuthFlags(),
			&cli.StringFlag{
				Name:    "outfile",
				Aliases: []string{"o"},
				Usage:   "File to write the WireGuard config to (prints to stdout if omitted)",
			},
			&cli.StringFlag{
				Name:    "region",
				Aliases: []string{"r"},
				Value:   defaultRegion,
				Usage:   "PIA region ID or friendly name (e.g. uk_southampton or \"UK Southampton\")",
			},
			&cli.BoolFlag{
				Name:    "server",
				Aliases: []string{"s"},
				Value:   false,
				Usage:   "Include the servers common name metadata in the config",
			},
			&cli.BoolFlag{
				Name:    "port-forwarding",
				Aliases: []string{"p"},
				Value:   false,
				Usage:   "Only pick servers that support port forwarding",
			},
		),
		Action: runGenerate,
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
		pia.PIAWgGeneratorConfig{Verbose: verbose, ServerName: serverName},
	)

	if verbose {
		log.Print("Generating WireGuard Config")
	}
	gen, err := wgConfigGenerator.GenerateWithMetadata()
	if err != nil {
		return err
	}

	config := gen.Config

	outfile := strings.TrimSpace(c.String("outfile"))
	if outfile != "" {
		if err := atomicWriteFile(outfile, []byte(config), 0644); err != nil {
			return err
		}
		if verbose {
			log.Printf("Successfully Wrote Config: %s", outfile)
		}
		return nil
	}

	_, err = io.WriteString(os.Stdout, config)
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
}

func buildDaemonCommand() *cli.Command {
	return &cli.Command{
		Name:  "daemon",
		Usage: "Run Continuously: Refresh WireGuard configs and maintain port forwarding",
		Flags: append(buildAuthFlags(),
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
				Usage:   "Include server common name metadata in the config",
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
			&cli.StringFlag{
				Name:  "on-port-change",
				Usage: "Hook command to run when forwarded port changes. Use {port} placeholder.",
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
	hookCmd := c.String("on-port-change")

	if region == "" {
		region = defaultRegion
	}

	configPath := mustStatePath(stateDir, defaultConfigFileName)
	statusPath := mustStatePath(stateDir, defaultStatusFileName)
	forwardedPortPath := mustStatePath(stateDir, defaultForwardedPortOut)

	interval := c.Duration("refresh-interval")
	jitterMax := c.Duration("refresh-jitter")

	if verbose {
		log.Printf("Daemon Starting. region=%s stateDir=%s interval=%s", region, stateDir, interval)
	}

	// PF lease state + renew loop
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
		}

		piaClient, clientErr := pia.NewPIAClient(username, password, region, verbose, portForwarding)
		if clientErr != nil {
			status.LastError = clientErr.Error()
			_ = writeStatus(statusPath, status)
			sleepUntil(next, verbose)
			continue
		}

		wgConfigGenerator := pia.NewPIAWgGenerator(
			piaClient,
			pia.PIAWgGeneratorConfig{Verbose: verbose, ServerName: serverName},
		)

		gen, genErr := wgConfigGenerator.GenerateWithMetadata()
		if genErr != nil {
			status.LastError = genErr.Error()
			_ = writeStatus(statusPath, status)
			sleepUntil(next, verbose)
			continue
		}

		if err := atomicWriteFile(configPath, []byte(gen.Config), 0644); err != nil {
			status.LastError = err.Error()
			_ = writeStatus(statusPath, status)
			sleepUntil(next, verbose)
			continue
		}

		// Port forwarding: acquire lease + write forwarded_port + update renew state.
		if portForwarding {
			portStr, pfErr := acquireAndBindPort(piaClient, gen.Key, verbose)
			if pfErr != nil {
				status.LastError = pfErr.Error()
				_ = writeStatus(statusPath, status)
				sleepUntil(next, verbose)
				continue
			}

			status.LastForwardPort = portStr

			if err := atomicWriteFile(forwardedPortPath, []byte(portStr), 0644); err != nil {
				status.LastError = err.Error()
				_ = writeStatus(statusPath, status)
				sleepUntil(next, verbose)
				continue
			}

			if portStr != lastPort {
				lastPort = portStr
				_ = runHook(hookCmd, portStr, verbose)
			}

			// Update renew loop state.
			// If gateway/signature changes (e.g. new server), renew loop follows the new values.
			updateLeaseState(lease, piaClient, gen.Key, portStr, verbose)
		}

		_ = writeStatus(statusPath, status)

		if verbose {
			log.Printf("Updated: %s (pf=%t)", configPath, portForwarding)
		}

		sleepUntil(next, verbose)
	}
}

func acquireAndBindPort(piaClient *pia.PIAClient, keyMeta pia.AddKeyResult, verbose bool) (string, error) {
	// Determine Gateway
	gateway := strings.TrimSpace(keyMeta.Gateway)
	if gateway == "" {
		gateway = strings.TrimSpace(keyMeta.ServerVip)
	}
	if gateway == "" {
		return "", errors.New("port forwarding enabled but no gateway/server_vip returned by API")
	}

	token, err := piaClient.GetToken()
	if err != nil {
		return "", err
	}

	pfClient := pia.NewPFClient(verbose)

	sig, payload, err := pfClient.GetSignature(gateway, token)
	if err != nil {
		return "", err
	}

	if err := pfClient.BindPort(gateway, sig); err != nil {
		return "", err
	}

	return strconv.Itoa(payload.Port), nil
}

func updateLeaseState(lease *pfLeaseState, piaClient *pia.PIAClient, keyMeta pia.AddKeyResult, portStr string, verbose bool) {
	gateway := strings.TrimSpace(keyMeta.Gateway)
	if gateway == "" {
		gateway = strings.TrimSpace(keyMeta.ServerVip)
	}
	if gateway == "" {
		lease.Enabled = false
		return
	}

	token, err := piaClient.GetToken()
	if err != nil {
		if verbose {
			log.Printf("PF Lease Update Token Error: %v", err)
		}
		lease.Enabled = false
		return
	}

	pfClient := pia.NewPFClient(verbose)
	sig, _, err := pfClient.GetSignature(gateway, token)
	if err != nil {
		if verbose {
			log.Printf("PF Lease Update Signature Error: %v", err)
		}
		lease.Enabled = false
		return
	}

	lease.Enabled = true
	lease.Gateway = gateway
	lease.Signature = sig
	lease.Port = portStr
}

func startPFRenewLoop(lease *pfLeaseState, verbose bool) {
	go func() {
		ticker := time.NewTicker(defaultPFRenewInterval)
		defer ticker.Stop()

		for range ticker.C {
			if !lease.Enabled {
				continue
			}

			pfClient := pia.NewPFClient(verbose)
			if err := pfClient.BindPort(lease.Gateway, lease.Signature); err != nil {
				if verbose {
					log.Printf("PF Renew Failed: %v", err)
				}
				continue
			}

			if verbose {
				log.Printf("PF Renewed (Port %s)", lease.Port)
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
