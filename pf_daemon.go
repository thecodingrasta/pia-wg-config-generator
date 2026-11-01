package main

import (
	"errors"
	"log"
	"strings"
	"time"

	"github.com/thecodingrasta/pia-wg-config-generator/pia"
	cli "github.com/urfave/cli/v2"
)

// =============================================================================
// pf — IN-NETNS port-forwarding daemon
//
// Runs with network_mode:service:gluetun. It owns ONLY the in-tunnel work:
// wait for the PF gateway, GetSignature once per registration, then BindPort
// every ~14m. It reads the PF metadata the refresh daemon dropped in
// pending_port_forward.json, and re-reads it whenever the file changes (a new
// registration), so a refresh-driven gluetun restart is picked up automatically.
// =============================================================================

func buildPFCommand() *cli.Command {
	return &cli.Command{
		Name:      "pf",
		Usage:     "In-netns port-forwarding daemon: acquire and renew the PIA port-forward lease (run with network_mode:service:gluetun)",
		UsageText: "pia-wg-config pf --state-dir /gluetun/wireguard [options]",
		Flags: append(buildAuthFlags(),
			&cli.StringFlag{Name: "state-dir", Usage: "Directory shared with the refresh daemon", Value: "/state"},
			&cli.StringFlag{Name: "region", Aliases: []string{"r"}, Value: defaultRegion, Usage: "PIA Region ID or friendly name (must match the refresh daemon)"},
			&cli.BoolFlag{Name: "wait-for-gateway", Usage: "Wait for the PF gateway before binding", Value: true},
			&cli.DurationFlag{Name: "gateway-timeout", Usage: "Max wait for the PF gateway", Value: defaultGatewayTimeout},
			&cli.DurationFlag{Name: "gateway-check-interval", Usage: "How often to probe the PF gateway", Value: defaultGatewayInterval},
			&cli.DurationFlag{Name: "poll-interval", Usage: "How often to look for a new pending PF registration", Value: 15 * time.Second},
			&cli.StringFlag{Name: "on-port-change", Usage: "Hook when the forwarded port changes. Use {port}.", Value: ""},
		),
		Action: runPF,
	}
}

func runPF(c *cli.Context) error {
	username, password, err := resolveCredentials(c)
	if err != nil {
		return err
	}
	verbose := c.Bool("verbose")
	stateDir := strings.TrimSpace(c.String("state-dir"))
	region := strings.TrimSpace(c.String("region"))
	if region == "" {
		region = defaultRegion
	}
	waitEnabled := c.Bool("wait-for-gateway")
	gatewayTimeout := c.Duration("gateway-timeout")
	gatewayInterval := c.Duration("gateway-check-interval")
	pollInterval := c.Duration("poll-interval")
	portHookCmd := c.String("on-port-change")

	pendingPFPath := mustStatePath(stateDir, defaultPendingPFName)
	forwardedPortPath := mustStatePath(stateDir, defaultForwardedPortOut)

	if verbose {
		log.Printf("PF daemon starting (IN gluetun netns). stateDir=%s waitForGateway=%t", stateDir, waitEnabled)
	}

	lease := &pfLeaseState{}
	startPFRenewLoop(lease, verbose)

	var activeRegistration time.Time
	var lastPort string
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for range ticker.C {
		pending, hasPending, err := readPendingPortForward(pendingPFPath)
		if err != nil {
			if verbose {
				log.Printf("PF: pending read failed: %v", err)
			}
			continue
		}
		if !hasPending {
			continue
		}

		// Only (re)bind when the pending registration is NEW. This is the
		// single-flight guard on the PF side — a stable lease keeps renewing via
		// the background loop and we never thrash GetSignature.
		if !pending.CreatedUTC.After(activeRegistration) {
			continue
		}

		if verbose {
			log.Printf("PF: new registration detected (created %s); acquiring lease", pending.CreatedUTC.Format(time.RFC3339))
		}

		// A PIA client is needed only for GetToken (public API). PF acquisition
		// itself talks to the in-tunnel gateway. port-forwarding is true so the
		// client filters to PF-capable servers, matching the refresher.
		piaClient, clientErr := pia.NewPIAClient(username, password, region, verbose, true)
		if clientErr != nil {
			if verbose {
				log.Printf("PF: client setup failed: %v (retry next poll)", clientErr)
			}
			continue
		}

		portStr, sig, gw, pfErr := acquirePortForwardAfterGatewayWait(
			piaClient, pending.Key, waitEnabled, gatewayTimeout, gatewayInterval, verbose,
		)
		if pfErr != nil {
			if verbose {
				log.Printf("PF: acquire failed: %v (will retry on next poll)", pfErr)
			}
			lease.disable()
			continue
		}

		if err := atomicWriteFile(forwardedPortPath, []byte(portStr), 0644); err != nil && verbose {
			log.Printf("PF: forwarded_port write failed: %v", err)
		}
		if portStr != lastPort {
			lastPort = portStr
			_ = runHook(portHookCmd, portStr, verbose)
		}
		lease.set(gw, sig, portStr)
		activeRegistration = pending.CreatedUTC
		if verbose {
			log.Printf("PF: lease active (port %s)", portStr)
		}
	}
	return errors.New("pf poll loop exited unexpectedly")
}
