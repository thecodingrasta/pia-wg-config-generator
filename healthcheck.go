package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	cli "github.com/urfave/cli/v2"
)

// =============================================================================
// healthcheck - container health probe for the refresh daemon
//
// The compose healthcheck used to grep status.json for tunnel_healthy, so it
// could not honour its own comment ("not past expected expiry") and flapped
// unhealthy during every normal refresh. This command evaluates the same file
// with explicit, bounded tolerances:
//
//   - wg0.conf must exist and be non-empty;
//   - status.json must have been written recently (the daemon rewrites it on
//     every health tick), which catches a hung or crashed loop;
//   - the tunnel may be unhealthy for a short time (a refresh restarts gluetun)
//     but not for longer than --max-unhealthy;
//   - the registration may run past its expected expiry only by --expiry-grace,
//     which catches a refresh that is silently stuck.
//
// It never restarts anything. Docker only labels the container unhealthy.
// =============================================================================

const (
	defaultHealthMaxStatusAge = 10 * time.Minute
	defaultHealthMaxUnhealthy = 10 * time.Minute
	defaultHealthExpiryGrace  = time.Hour
)

func buildHealthcheckCommand() *cli.Command {
	return &cli.Command{
		Name:  "healthcheck",
		Usage: "Exit 0 when the refresh daemon's state is healthy, 1 otherwise (for container healthchecks)",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "state-dir", Usage: "Directory shared with the refresh daemon", Value: "/state"},
			&cli.DurationFlag{Name: "max-status-age", Usage: "status.json older than this means the daemon loop is stuck", Value: defaultHealthMaxStatusAge},
			&cli.DurationFlag{Name: "max-unhealthy", Usage: "Longest continuous tunnel outage tolerated before reporting unhealthy", Value: defaultHealthMaxUnhealthy},
			&cli.DurationFlag{Name: "expiry-grace", Usage: "How far past expected_expiry_utc the registration may run before reporting unhealthy", Value: defaultHealthExpiryGrace},
		},
		Action: func(c *cli.Context) error {
			stateDir := c.String("state-dir")
			verdict := evaluateHealth(healthOptions{
				configPath:   mustStatePath(stateDir, defaultConfigFileName),
				statusPath:   mustStatePath(stateDir, defaultStatusFileName),
				maxStatusAge: c.Duration("max-status-age"),
				maxUnhealthy: c.Duration("max-unhealthy"),
				expiryGrace:  c.Duration("expiry-grace"),
				now:          time.Now().UTC(),
			})
			if verdict != nil {
				return cli.Exit("UNHEALTHY: "+verdict.Error(), 1)
			}
			_, _ = fmt.Fprintln(c.App.Writer, "OK")
			return nil
		},
	}
}

type healthOptions struct {
	configPath, statusPath                  string
	maxStatusAge, maxUnhealthy, expiryGrace time.Duration
	now                                     time.Time
}

// evaluateHealth returns nil when healthy, otherwise an error whose text is the
// single most useful reason.
func evaluateHealth(o healthOptions) error {
	cfg, err := os.Stat(o.configPath)
	if err != nil {
		return fmt.Errorf("wg0.conf is missing: %w", err)
	}
	if cfg.Size() == 0 {
		return fmt.Errorf("wg0.conf is empty")
	}

	info, err := os.Stat(o.statusPath)
	if err != nil {
		return fmt.Errorf("status.json is missing: %w", err)
	}
	if age := o.now.Sub(info.ModTime()); o.maxStatusAge > 0 && age > o.maxStatusAge {
		return fmt.Errorf("status.json was last written %s ago (limit %s): the daemon loop looks stuck",
			age.Round(time.Second), o.maxStatusAge)
	}

	data, err := os.ReadFile(o.statusPath)
	if err != nil {
		return fmt.Errorf("status.json is unreadable: %w", err)
	}
	var st daemonStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("status.json is not valid JSON: %w", err)
	}

	if !st.TunnelHealthy {
		if st.UnhealthySinceUTC.IsZero() {
			return fmt.Errorf("tunnel is not healthy and no outage start is recorded")
		}
		if down := o.now.Sub(st.UnhealthySinceUTC); o.maxUnhealthy > 0 && down > o.maxUnhealthy {
			return fmt.Errorf("tunnel has been unhealthy for %s (limit %s)", down.Round(time.Second), o.maxUnhealthy)
		}
	}

	if !st.ExpectedExpiryUTC.IsZero() {
		if over := o.now.Sub(st.ExpectedExpiryUTC); over > o.expiryGrace {
			return fmt.Errorf("registration is %s past its expected expiry (%s, grace %s): the proactive refresh is not happening",
				over.Round(time.Second), st.ExpectedExpiryUTC.Format(time.RFC3339), o.expiryGrace)
		}
	}
	return nil
}
