package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/thecodingrasta/pia-wg-config-generator/pia"
	cli "github.com/urfave/cli/v2"
)

// =============================================================================
// refresh - OUT-OF-NETNS config daemon (the self-recovering half)
//
// WHY THIS EXISTS
// The original `daemon` ran config regeneration (token + server list + addKey,
// all PIA PUBLIC-API calls) INSIDE gluetun's network namespace. When PIA evicts
// the WireGuard peer the tunnel dies, the namespace loses egress, and every
// re-registration attempt fails with "network is unreachable" forever - the
// repairer is trapped inside the thing it must repair. Observed 2026-10: an
// 11h45m-old registration died and the 12h timer could never recover it.
//
// `refresh` runs on the LAN (NOT network_mode:service:gluetun), so its PIA
// public-API calls always have egress. It:
//   - regenerates wg0.conf proactively at a LIFETIME-AWARE cadence,
//   - detects a dead tunnel via gluetun's control server and REACTS, under
//     single-flight + exponential backoff + an escalating cooldown so a wedged
//     tunnel can never become a registration-churn loop,
//   - restarts gluetun so it loads the new config; gluetun-monitor re-attaches
//     the stranded network_mode:service:gluetun siblings,
//   - hands PF metadata to the in-netns `pf` daemon via pending_port_forward.json,
//   - writes an accurate status.json and alerts ONCE per genuine failed outage.
// =============================================================================

// recoveryCooldownCap bounds the escalating cooldown between reactive
// recoveries. A recovery that did not restore the tunnel doubles the wait, so a
// persistent fault costs at most ~2 registrations an hour, never a storm.
const recoveryCooldownCap = 30 * time.Minute

func buildRefreshCommand() *cli.Command {
	return &cli.Command{
		Name:      "refresh",
		Usage:     "Out-of-netns self-recovering config daemon: regenerate wg0.conf on a lifetime-aware cadence and reactively on tunnel loss",
		UsageText: "pia-wg-config refresh --region REGION --state-dir /gluetun/wireguard [options]",
		Flags: append(buildAuthFlags(),
			buildIPv6Flag(),
			&cli.StringFlag{Name: "state-dir", Usage: "Directory for config/status/lifetime state", Value: "/state"},
			&cli.StringFlag{Name: "region", Aliases: []string{"r"}, Value: defaultRegion, Usage: "PIA Region ID or friendly name"},
			&cli.BoolFlag{Name: "server", Aliases: []string{"s"}, Value: false, Usage: "Include server common name as a comment"},
			&cli.BoolFlag{Name: "port-forwarding", Aliases: []string{"p"}, Value: false, Usage: "Hand port-forwarding metadata to the pf daemon"},
			&cli.DurationFlag{Name: "refresh-interval", Usage: "Fallback proactive refresh interval before a lifetime has been measured", Value: defaultDaemonInterval},
			&cli.DurationFlag{Name: "refresh-jitter", Usage: "Max jitter applied to the refresh interval", Value: defaultDaemonJitterMax},
			&cli.DurationFlag{
				Name:  "max-registration-age",
				Usage: "Hard cap on registration age before a proactive refresh (0 = derive from measured lifetime). Refresh fires at <= 50% of this.",
				Value: 0,
			},
			&cli.BoolFlag{
				Name:  "adopt-healthy-registration",
				Usage: "On startup, keep an existing healthy, young registration instead of re-registering and restarting gluetun",
				Value: true,
			},
			&cli.DurationFlag{Name: "health-check-interval", Usage: "How often to check tunnel health via gluetun's control server", Value: defaultHealthCheckInterval},
			&cli.IntFlag{
				Name:  "unhealthy-threshold",
				Usage: "Consecutive unhealthy health checks required before a reactive recovery (debounces gluetun restarts; 1 = act on the first)",
				Value: defaultUnhealthyThreshold,
			},
			&cli.StringFlag{Name: "control-server-url", Usage: "gluetun HTTP control server base URL (reachable from OUTSIDE the tunnel)", Value: defaultControlServerURL},
			&cli.DurationFlag{Name: "control-timeout", Usage: "Timeout for a gluetun control-server request", Value: defaultControlTimeout},
			&cli.DurationFlag{Name: "recovery-backoff-min", Usage: "Initial backoff after a failed recovery attempt", Value: defaultRecoveryBackoffMin},
			&cli.DurationFlag{Name: "recovery-backoff-max", Usage: "Maximum backoff between recovery attempts", Value: defaultRecoveryBackoffMax},
			&cli.DurationFlag{Name: "recovery-cooldown", Usage: "Minimum time between reactive recoveries (doubles after each recovery that did not restore the tunnel, capped at 30m)", Value: defaultRecoveryCooldown},
			&cli.StringFlag{Name: "restart-container", Usage: "gluetun container to restart after writing a new config", Value: ""},
			&cli.StringFlag{Name: "docker-socket", Usage: "Docker socket path used by --restart-container", Value: defaultDockerSocket},
			&cli.StringFlag{
				Name:    "docker-host",
				Usage:   "Docker API endpoint for --restart-container, e.g. tcp://pia-wg-refresh-proxy:2375 (a least-privilege socket proxy). Overrides --docker-socket.",
				EnvVars: []string{"DOCKER_HOST"},
			},
			&cli.StringFlag{Name: "on-config-change", Usage: "Hook after each successful config write. Placeholders: {config},{state_dir},{forwarded_port},{port}.", Value: ""},
			&cli.StringFlag{
				Name:  "on-failure",
				Usage: "Hook run ONCE per outage when recovery has genuinely failed (after --alert-after consecutive failed attempts). Receives PIA_WG_EVENT/PIA_WG_MESSAGE/... in its environment.",
			},
			&cli.StringFlag{
				Name:  "on-recovered",
				Usage: "Optional hook run once when the tunnel is healthy again after an --on-failure alert was sent.",
			},
			&cli.IntFlag{Name: "alert-after", Usage: "Consecutive failed recovery attempts before the failure hook fires (0 disables alerting)", Value: defaultAlertAfter},
			&cli.DurationFlag{Name: "hook-timeout", Usage: "Hard timeout for the failure/recovered hooks; they never block recovery", Value: defaultHookTimeout},
		),
		Action: runRefresh,
	}
}

const (
	defaultUnhealthyThreshold = 2
	defaultAlertAfter         = 3
	defaultHookTimeout        = 15 * time.Second
)

// refreshRuntime bundles the resolved config and the mutable lifecycle state of
// the refresh loop.
type refreshRuntime struct {
	username, password     string
	region                 string
	stateDir               string
	verbose, serverName    bool
	portForwarding         bool
	ipv6Mode               pia.IPv6Mode
	ipv6ModeStr            string
	fallbackInterval       time.Duration
	jitterMax              time.Duration
	maxRegistrationAge     time.Duration
	adoptHealthy           bool
	healthInterval         time.Duration
	unhealthyThreshold     int
	controlURL             string
	controlTimeout         time.Duration
	backoffMin, backoffMax time.Duration
	restartContainer       string
	dockerSocket           string
	dockerHost             string
	configHookCmd          string

	configPath        string
	statusPath        string
	forwardedPortPath string
	pendingPFPath     string
	lifetime          *pia.LifetimeStore

	// Injectable seams. Nil means "use the real implementation". They exist so
	// the control loop can be driven deterministically in tests without real PIA
	// credentials or a Docker daemon.
	generateFn func() (pia.GenerateResult, error)
	restartFn  func(container string) error

	// recoveryCooldown is the BASE minimum time between reactive recoveries; the
	// effective value doubles per recovery that failed to restore the tunnel.
	recoveryCooldown time.Duration

	// Alerting.
	policy *outagePolicy
	alerts *alertDispatcher

	// flightMu serialises regeneration so the proactive timer and a
	// health-triggered recovery never mint duplicate registrations.
	flightMu sync.Mutex

	// stateMu guards everything below. It is SEPARATE from flightMu because
	// status writers read this state while flightMu is held, and sync.Mutex is
	// not reentrant.
	stateMu sync.Mutex

	lastGenerateUTC time.Time
	nextRefreshUTC  time.Time
	lastRecoveryUTC time.Time
	recoveryCount   int64

	tunnelHealthy       bool
	controlStatus       string
	publicIPKnown       bool
	publicIPPresent     bool
	lastHealthCheckUTC  time.Time
	lastHealthyUTC      time.Time
	unhealthySinceUTC   time.Time
	consecutiveUnhealth int

	// confirmedHealthy is true once the CURRENT registration has been seen with
	// a healthy tunnel. Only then is its death a real lifetime measurement; a
	// registration that never came up says nothing about PIA's eviction time.
	confirmedHealthy bool
	// healthySinceRecovery is false from the moment a reactive recovery applies
	// until a healthy sample proves it worked.
	healthySinceRecovery bool
	// ineffectiveRecoveries counts reactive recoveries in a row that did not
	// restore the tunnel; it drives the escalating cooldown.
	ineffectiveRecoveries int

	regenError   string // last regeneration failure (redacted); cleared on success
	healthError  string // last health-check failure (redacted); cleared when healthy
	ipSignalWarn bool   // the "public IP signal unavailable" warning is logged once
}

func (rt *refreshRuntime) secrets() []string {
	return []string{rt.username, rt.password}
}

// redact scrubs credentials from text bound for status.json, logs or alerts.
func (rt *refreshRuntime) redact(s string) string {
	return pia.Redact(s, rt.secrets()...)
}

// generate produces a fresh config + registration metadata. It uses the
// injected seam when set, otherwise the real PIA client/generator - which makes
// only PIA PUBLIC-API calls (token, server list, addKey) and therefore works
// with the tunnel DOWN.
func (rt *refreshRuntime) generate() (pia.GenerateResult, error) {
	if rt.generateFn != nil {
		return rt.generateFn()
	}
	piaClient, err := pia.NewPIAClient(rt.username, rt.password, rt.region, rt.verbose, rt.portForwarding)
	if err != nil {
		return pia.GenerateResult{}, fmt.Errorf("client setup: %w", err)
	}
	return pia.NewPIAWgGenerator(piaClient, pia.PIAWgGeneratorConfig{
		Verbose: rt.verbose, ServerName: rt.serverName, IPv6Mode: rt.ipv6Mode,
	}).GenerateWithMetadata()
}

// restartGluetun restarts gluetun via the injected seam when set, otherwise
// through the configured Docker endpoint (socket or socket proxy).
func (rt *refreshRuntime) restartGluetun() error {
	if rt.restartContainer == "" {
		return nil
	}
	if rt.restartFn != nil {
		return rt.restartFn(rt.restartContainer)
	}
	return restartDockerContainerVia(rt.dockerHost, rt.dockerSocket, rt.restartContainer, rt.verbose)
}

func runRefresh(c *cli.Context) error {
	username, password, err := resolveCredentials(c)
	if err != nil {
		return err
	}
	ipv6Mode, err := parseIPv6Mode(c.String("ipv6-mode"))
	if err != nil {
		return err
	}

	region := strings.TrimSpace(c.String("region"))
	if region == "" {
		region = defaultRegion
	}
	stateDir := strings.TrimSpace(c.String("state-dir"))
	dockerHost := strings.TrimSpace(c.String("docker-host"))
	if dockerHost != "" {
		// Fail at startup, not at the first (already stressful) recovery.
		if _, _, err := dockerEndpoint(dockerHost, ""); err != nil {
			return err
		}
	}

	rt := &refreshRuntime{
		username:           username,
		password:           password,
		region:             region,
		stateDir:           stateDir,
		verbose:            c.Bool("verbose"),
		serverName:         c.Bool("server"),
		portForwarding:     c.Bool("port-forwarding"),
		ipv6Mode:           ipv6Mode,
		ipv6ModeStr:        c.String("ipv6-mode"),
		fallbackInterval:   c.Duration("refresh-interval"),
		jitterMax:          c.Duration("refresh-jitter"),
		maxRegistrationAge: c.Duration("max-registration-age"),
		adoptHealthy:       c.Bool("adopt-healthy-registration"),
		healthInterval:     c.Duration("health-check-interval"),
		unhealthyThreshold: c.Int("unhealthy-threshold"),
		controlURL:         strings.TrimSpace(c.String("control-server-url")),
		controlTimeout:     c.Duration("control-timeout"),
		backoffMin:         c.Duration("recovery-backoff-min"),
		backoffMax:         c.Duration("recovery-backoff-max"),
		recoveryCooldown:   c.Duration("recovery-cooldown"),
		restartContainer:   strings.TrimSpace(c.String("restart-container")),
		dockerSocket:       strings.TrimSpace(c.String("docker-socket")),
		dockerHost:         dockerHost,
		configHookCmd:      c.String("on-config-change"),
		configPath:         mustStatePath(stateDir, defaultConfigFileName),
		statusPath:         mustStatePath(stateDir, defaultStatusFileName),
		forwardedPortPath:  mustStatePath(stateDir, defaultForwardedPortOut),
		pendingPFPath:      mustStatePath(stateDir, defaultPendingPFName),
		lifetime:           pia.NewLifetimeStore(stateDir),
		policy:             newOutagePolicy(c.Int("alert-after")),
	}

	rt.alerts = newAlertDispatcher(
		c.String("on-failure"), c.String("on-recovered"), region,
		c.Duration("hook-timeout"), append(rt.secrets(), alertSecretsFromEnv()...), rt.verbose,
	)
	rt.alerts.start()

	if rt.verbose {
		endpoint := "unix socket " + rt.dockerSocket
		if rt.dockerHost != "" {
			endpoint = rt.dockerHost
		}
		log.Printf("Refresh daemon starting (OUT of gluetun netns). region=%s stateDir=%s control=%s healthInterval=%s unhealthyThreshold=%d pf=%t ipv6=%s docker=%s alertAfter=%d",
			rt.region, rt.stateDir, rt.controlURL, rt.healthInterval, rt.unhealthyThreshold, rt.portForwarding, rt.ipv6ModeStr, endpoint, c.Int("alert-after"))
	}

	// Startup: keep a healthy, young registration; otherwise register afresh.
	// Always re-registering (the previous behaviour) restarted gluetun on every
	// redeploy of this container and silently reset the measured registration
	// clock.
	if !rt.adoptHealthy || !rt.tryAdopt() {
		if err := rt.regenerateWithBackoff("startup"); err != nil && rt.verbose {
			log.Printf("Initial refresh did not complete cleanly: %s (continuing; loop will retry)", rt.redact(err.Error()))
		}
	}

	healthTicker := time.NewTicker(rt.healthInterval)
	defer healthTicker.Stop()

	proactiveTimer := time.NewTimer(rt.armNextRefresh())
	defer proactiveTimer.Stop()

	for {
		select {
		case <-healthTicker.C:
			rt.onHealthTick()
			rt.rearm(proactiveTimer)
		case <-proactiveTimer.C:
			if rt.verbose {
				log.Printf("Proactive lifetime-aware refresh firing")
			}
			if err := rt.regenerateWithBackoff("proactive"); err != nil && rt.verbose {
				log.Printf("Proactive refresh failed: %s", rt.redact(err.Error()))
			}
			proactiveTimer.Reset(rt.armNextRefresh())
		}
	}
}

// alertSecretsFromEnv lists the exact secret values (the webhook URL) that must
// be scrubbed from anything the alert path logs.
func alertSecretsFromEnv() []string {
	return []string{envOrEmpty("PIA_WG_ALERT_WEBHOOK")}
}

// nextProactiveDelay computes the delay until the next proactive refresh. It is
// anchored to the CURRENT registration's birth, so a reactive recovery (which
// creates a new registration) pushes the schedule out accordingly instead of
// leaving a stale timer from the old registration.
func (rt *refreshRuntime) nextProactiveDelay() time.Duration {
	rec, err := rt.lifetime.Load()
	if err != nil {
		return applyJitter(rt.fallbackInterval, rt.jitterMax)
	}
	interval := applyJitter(rec.SafeRefreshInterval(rt.fallbackInterval, rt.maxRegistrationAge), rt.jitterMax)
	if rec.CurrentRegistrationUTC.IsZero() {
		return interval
	}
	delay := time.Until(rec.CurrentRegistrationUTC.Add(interval))
	if delay < time.Minute {
		delay = time.Minute
	}
	return delay
}

// armNextRefresh computes and RECORDS the next proactive refresh time (it is
// what status.json reports as next_refresh_utc), and returns the delay.
func (rt *refreshRuntime) armNextRefresh() time.Duration {
	delay := rt.nextProactiveDelay()
	rt.stateMu.Lock()
	rt.nextRefreshUTC = time.Now().UTC().Add(delay)
	rt.stateMu.Unlock()
	rt.flushStatus()
	return delay
}

// rearm resets the proactive timer when the registration it was anchored to has
// been replaced (a reactive recovery).
func (rt *refreshRuntime) rearm(t *time.Timer) {
	rt.stateMu.Lock()
	due := rt.nextRefreshUTC
	rt.stateMu.Unlock()

	rec, err := rt.lifetime.Load()
	if err != nil || rec.CurrentRegistrationUTC.IsZero() {
		return
	}
	// Re-arm only when the registration is newer than the schedule implies.
	if due.After(rec.CurrentRegistrationUTC.Add(time.Minute)) && time.Since(rec.CurrentRegistrationUTC) > rt.healthInterval*2 {
		return
	}
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(rt.armNextRefresh())
}

// tryAdopt keeps an existing registration when it is provably fine: a config is
// on disk, it was generated for this region, the tunnel is healthy right now,
// and the registration is comfortably inside its refresh interval. It returns
// true when adopted (no re-registration, no gluetun restart).
func (rt *refreshRuntime) tryAdopt() bool {
	cfg, err := pia.ParseEndpointFromConfig(rt.configPath)
	if err != nil || cfg == "" {
		return false
	}
	rec, err := rt.lifetime.Load()
	if err != nil || rec.CurrentRegistrationUTC.IsZero() {
		return false
	}
	if prev, ok := readPreviousRegion(rt.statusPath); !ok || prev != rt.region {
		return false
	}
	age, _ := pia.RegistrationAge(rec.CurrentRegistrationUTC, time.Now().UTC())
	if age >= rec.SafeRefreshInterval(rt.fallbackInterval, rt.maxRegistrationAge)-rt.healthInterval {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), rt.controlTimeout)
	defer cancel()
	state := pia.CheckTunnelViaControlServer(ctx, rt.controlURL, rt.controlTimeout)
	if !state.Healthy {
		return false
	}

	now := time.Now().UTC()
	rt.stateMu.Lock()
	rt.lastGenerateUTC = rec.CurrentRegistrationUTC // when this config was actually applied
	rt.confirmedHealthy = true
	rt.healthySinceRecovery = true
	rt.stateMu.Unlock()
	rt.observe(state, now)
	if rt.verbose {
		log.Printf("[startup] adopted existing healthy registration born %s (age %s); not re-registering and not restarting gluetun",
			rec.CurrentRegistrationUTC.Format(time.RFC3339), age.Round(time.Second))
	}
	return true
}

// observe folds one control-server sample into the lifecycle state, writes
// status.json, and emits the "recovered" notice when an alerted outage ended.
func (rt *refreshRuntime) observe(state pia.TunnelState, now time.Time) {
	var recovered bool
	var outage time.Duration
	var attempts int

	rt.stateMu.Lock()
	rt.controlStatus = state.Status
	rt.publicIPKnown = state.PublicIPKnown
	rt.publicIPPresent = state.PublicIPPresent
	rt.lastHealthCheckUTC = now
	rt.tunnelHealthy = state.Healthy
	if state.Healthy {
		rt.lastHealthyUTC = now
		rt.confirmedHealthy = true
		rt.healthySinceRecovery = true
		rt.ineffectiveRecoveries = 0
		rt.consecutiveUnhealth = 0
		rt.unhealthySinceUTC = time.Time{}
		rt.healthError = ""
	} else {
		rt.consecutiveUnhealth++
		if rt.unhealthySinceUTC.IsZero() {
			rt.unhealthySinceUTC = now
		}
		if state.Err != nil {
			rt.healthError = rt.redact(state.Err.Error())
		}
	}
	warn := !state.PublicIPKnown && state.Healthy && !rt.ipSignalWarn
	if warn {
		rt.ipSignalWarn = true
	}
	rt.stateMu.Unlock()

	if warn && rt.verbose {
		log.Printf("WARNING: gluetun's /v1/publicip/ip is not readable; mid-life silent evictions can only be detected by VPN status, which may miss them (see docs/gluetun.md)")
	}

	if state.Healthy {
		recovered, outage, attempts = rt.policy.healthy(now)
		if recovered {
			rt.alerts.send(alertEvent{
				Kind:     alertEventRecovered,
				Message:  fmt.Sprintf("tunnel is healthy again after an outage of %s (%d failed attempts)", outage.Round(time.Second), attempts),
				Attempts: attempts,
				Outage:   outage,
			})
		}
	}
	rt.flushStatus()
}

// onHealthTick samples the tunnel and, when it has been unhealthy long enough
// and is out of cooldown, triggers a reactive recovery.
func (rt *refreshRuntime) onHealthTick() {
	ctx, cancel := context.WithTimeout(context.Background(), rt.controlTimeout)
	defer cancel()

	state := pia.CheckTunnelViaControlServer(ctx, rt.controlURL, rt.controlTimeout)
	now := time.Now().UTC()
	rt.observe(state, now)

	if state.Healthy {
		return
	}

	rt.stateMu.Lock()
	consecutive := rt.consecutiveUnhealth
	threshold := rt.unhealthyThreshold
	if threshold < 1 {
		threshold = 1
	}
	cooldown := rt.effectiveCooldownLocked()
	inCooldown := !rt.lastRecoveryUTC.IsZero() && time.Since(rt.lastRecoveryUTC) < cooldown
	rt.stateMu.Unlock()

	if consecutive < threshold {
		if rt.verbose {
			log.Printf("Tunnel unhealthy (status=%q, %d/%d consecutive) - waiting for confirmation before recovering", state.Status, consecutive, threshold)
		}
		return
	}

	// Cooldown guard: after a reactive recovery, give the fresh registration +
	// gluetun restart time to hand-shake before re-triggering. Without it a
	// tunnel that stays down (gluetun wedged, PIA region down) would
	// re-register on every tick - a churn loop that leaks registrations.
	if inCooldown {
		if rt.verbose {
			log.Printf("Tunnel still UNHEALTHY (status=%q) but within recovery cooldown (%s) - waiting for the new tunnel to settle",
				state.Status, cooldown.Round(time.Second))
		}
		return
	}

	if rt.verbose {
		log.Printf("Tunnel UNHEALTHY (status=%q err=%s) - recovering", state.Status, rt.redact(fmt.Sprint(state.Err)))
	}

	rt.accountForPreviousRecovery(now)
	rt.recordLossIfMeasurable()

	if err := rt.regenerateWithBackoff("reactive-recovery"); err != nil && rt.verbose {
		log.Printf("Reactive recovery attempt returned: %s", rt.redact(err.Error()))
	}
}

// effectiveCooldownLocked is the base cooldown doubled once per recovery that
// failed to restore the tunnel, capped at recoveryCooldownCap. Caller holds
// stateMu.
func (rt *refreshRuntime) effectiveCooldownLocked() time.Duration {
	cooldown := rt.recoveryCooldown
	if cooldown <= 0 {
		cooldown = defaultRecoveryCooldown
	}
	for i := 0; i < rt.ineffectiveRecoveries && cooldown < recoveryCooldownCap; i++ {
		cooldown *= 2
	}
	if cooldown > recoveryCooldownCap {
		cooldown = recoveryCooldownCap
	}
	return cooldown
}

// accountForPreviousRecovery notes when a reactive recovery already happened
// and the tunnel never came back: that recovery was ineffective, which both
// escalates the cooldown and counts toward the failure alert.
func (rt *refreshRuntime) accountForPreviousRecovery(now time.Time) {
	rt.stateMu.Lock()
	ineffective := rt.recoveryCount > 0 && !rt.healthySinceRecovery
	if ineffective {
		rt.ineffectiveRecoveries++
	}
	rt.stateMu.Unlock()

	if ineffective {
		rt.noteFailedAttempt(now, "the previous recovery applied a new config but the tunnel did not come back")
	}
}

// recordLossIfMeasurable stores the dead registration's lifetime, but only when
// that registration was actually seen healthy. The loss time used is the last
// healthy sample (the latest moment we KNOW it was alive), which can only err
// on the safe, shorter side.
func (rt *refreshRuntime) recordLossIfMeasurable() {
	rt.stateMu.Lock()
	confirmed := rt.confirmedHealthy
	lastHealthy := rt.lastHealthyUTC
	rt.stateMu.Unlock()

	if !confirmed || lastHealthy.IsZero() {
		if rt.verbose {
			log.Printf("Not recording a lifetime: the current registration was never confirmed healthy, so its failure says nothing about PIA's eviction time")
		}
		return
	}
	if _, err := rt.lifetime.RecordObservedLoss(lastHealthy); err != nil && rt.verbose {
		log.Printf("Could not record observed loss: %v", err)
	}
}

// noteFailedAttempt feeds the alert policy and sends the failure alert once the
// threshold is reached. cause must already be redacted.
func (rt *refreshRuntime) noteFailedAttempt(now time.Time, cause string) {
	fire, attempts := rt.policy.failure(now)
	if !fire {
		return
	}
	rt.alerts.send(alertEvent{
		Kind: alertEventFailure,
		Message: fmt.Sprintf("PIA WireGuard recovery has FAILED after %d consecutive attempts (region %s): %s",
			attempts, rt.region, cause),
		Attempts: attempts,
	})
}

// regenerateWithBackoff performs ONE successful regeneration, retrying with
// exponential backoff on failure. Single-flight via flightMu guarantees the
// proactive timer and a health-triggered recovery never run together.
func (rt *refreshRuntime) regenerateWithBackoff(trigger string) error {
	rt.flightMu.Lock()
	defer rt.flightMu.Unlock()

	backoff := rt.backoffMin
	var lastErr error
	for attempt := 1; attempt <= maxRegenAttempts; attempt++ {
		err := rt.regenerateOnce(trigger)
		if err == nil {
			if trigger == "reactive-recovery" {
				rt.stateMu.Lock()
				rt.lastRecoveryUTC = time.Now().UTC()
				rt.recoveryCount++
				rt.healthySinceRecovery = false
				rt.confirmedHealthy = false
				rt.consecutiveUnhealth = 0
				rt.stateMu.Unlock()
				rt.flushStatus()
			}
			return nil
		}
		lastErr = err
		cause := rt.redact(err.Error())
		rt.stateMu.Lock()
		rt.regenError = cause
		rt.stateMu.Unlock()
		rt.flushStatus()
		rt.noteFailedAttempt(time.Now().UTC(), cause)
		if rt.verbose {
			log.Printf("[%s] regen attempt %d/%d failed: %s; backoff %s", trigger, attempt, maxRegenAttempts, cause, backoff.Round(time.Second))
		}
		time.Sleep(backoff)
		backoff *= 2
		if backoff > rt.backoffMax {
			backoff = rt.backoffMax
		}
	}
	return fmt.Errorf("regeneration failed after %d attempts: %w", maxRegenAttempts, lastErr)
}

const maxRegenAttempts = 6

// regenerateOnce does a single config regeneration + apply. It never enters the
// tunnel: token/serverlist/addKey are PIA public-API calls, and the gluetun
// restart is a Docker API call. PF binding is delegated to the pf daemon via
// the pending file.
func (rt *refreshRuntime) regenerateOnce(trigger string) error {
	now := time.Now().UTC()

	gen, err := rt.generate()
	if err != nil {
		return fmt.Errorf("config generation: %w", err)
	}

	if err := atomicWriteFile(rt.configPath, []byte(gen.Config), 0644); err != nil {
		return fmt.Errorf("config write: %w", err)
	}

	// Birth time of this registration drives lifetime-aware cadence + expiry.
	if _, err := rt.lifetime.RecordNewRegistration(now); err != nil && rt.verbose {
		log.Printf("Could not record registration birth: %v", err)
	}

	// Hand PF metadata to the in-netns pf daemon BEFORE restarting gluetun, so
	// it is present the moment the tunnel comes back.
	if rt.portForwarding {
		if err := writePendingPortForward(rt.pendingPFPath, gen.Key); err != nil {
			return fmt.Errorf("pending PF write: %w", err)
		}
	}

	if err := runConfigHook(rt.configHookCmd, rt.stateDir, rt.configPath, rt.forwardedPortPath, "", rt.verbose); err != nil && rt.verbose {
		log.Printf("Config hook failed (non-fatal): %v", err)
	}

	// Restart gluetun so it loads the new wg0.conf. gluetun-monitor re-attaches
	// the stranded siblings afterwards.
	if err := rt.restartGluetun(); err != nil {
		return fmt.Errorf("gluetun restart: %w", err)
	}

	rt.stateMu.Lock()
	rt.lastGenerateUTC = now
	rt.regenError = ""
	rt.stateMu.Unlock()
	rt.armNextRefresh()

	if rt.verbose {
		endpoint, _ := pia.ParseEndpointFromConfig(rt.configPath)
		log.Printf("[%s] config applied; new registration born %s (endpoint %s)", trigger, now.Format(time.RFC3339), endpoint)
	}
	return nil
}

// buildStatus assembles status.json from the shared lifecycle state, so every
// writer produces the same complete picture. (The previous per-writer
// construction let the once-a-minute health writer overwrite the fields only
// the apply writer knew, which is why last_generate_utc and next_refresh_utc
// read 0001-01-01 on a live system.)
func (rt *refreshRuntime) buildStatus() daemonStatus {
	rec, _ := rt.lifetime.Load()
	now := time.Now().UTC()
	expiry, source := rec.ExpectedExpiry(rt.fallbackInterval, rt.maxRegistrationAge)
	failures, _ := rt.policy.snapshot()

	rt.stateMu.Lock()
	defer rt.stateMu.Unlock()

	st := daemonStatus{
		Region:             rt.region,
		Mode:               "refresh",
		PortForwarding:     rt.portForwarding,
		ServerNameEnabled:  rt.serverName,
		IPv6Mode:           rt.ipv6ModeStr,
		LastGenerateUTC:    rt.lastGenerateUTC,
		NextRefreshUTC:     rt.nextRefreshUTC,
		RegistrationUTC:    rec.CurrentRegistrationUTC,
		ExpectedExpiryUTC:  expiry,
		LifetimeSource:     source,
		TunnelHealthy:      rt.tunnelHealthy,
		ControlStatus:      rt.controlStatus,
		LastHealthCheckUTC: rt.lastHealthCheckUTC,
		LastHealthyUTC:     rt.lastHealthyUTC,
		UnhealthySinceUTC:  rt.unhealthySinceUTC,
		LastRecoveryUTC:    rt.lastRecoveryUTC,
		RecoveryCount:      rt.recoveryCount,
		FailedAttempts:     failures,
	}
	if age, ok := pia.RegistrationAge(rec.CurrentRegistrationUTC, now); ok {
		st.RegistrationAgeSecs = int64(age.Seconds())
	}
	if life, ok := rec.MeasuredLifetime(); ok {
		st.MeasuredLifetimeSecs = int64(life.Seconds())
	}
	if rt.publicIPKnown {
		present := rt.publicIPPresent
		st.PublicIPPresent = &present
	}
	switch {
	case rt.regenError != "":
		st.LastError = rt.regenError
	case rt.healthError != "":
		st.LastError = rt.healthError
	}
	return st
}

// flushStatus writes status.json. A write failure is logged, never fatal: the
// daemon's job is to keep the tunnel alive, not to keep its diagnostics file
// perfect.
func (rt *refreshRuntime) flushStatus() {
	if err := writeStatus(rt.statusPath, rt.buildStatus()); err != nil && rt.verbose {
		log.Printf("Could not write %s: %v", rt.statusPath, err)
	}
}
