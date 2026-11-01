package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/thecodingrasta/pia-wg-config-generator/pia"
)

// =============================================================================
// Failure alerting
//
// Goals (from the operator's brief):
//   - fire ONLY when recovery has genuinely failed: after N consecutive failed
//     attempts, never on a transient retry and never on a successful recovery;
//   - fire exactly ONCE per outage, then a separate optional "recovered" notice;
//   - never block or delay recovery: hooks run on their own goroutine with a
//     hard timeout, and their failure is logged and otherwise ignored;
//   - never leak credentials: every string handed to a hook, a log line or
//     status.json passes through pia.Redact first, and the hook runs with a
//     scrubbed environment that does not contain the PIA account credentials.
// =============================================================================

// Alert event kinds passed to hooks in PIA_WG_EVENT.
const (
	alertEventFailure   = "failure"
	alertEventRecovered = "recovered"
)

// outagePolicy decides when an alert is due. It is pure state with no I/O so it
// can be tested exhaustively.
type outagePolicy struct {
	mu          sync.Mutex
	threshold   int // alert after this many consecutive failed attempts; <=0 disables alerting
	failures    int // consecutive failed attempts in the current outage
	alerted     bool
	outageStart time.Time
}

func newOutagePolicy(threshold int) *outagePolicy {
	return &outagePolicy{threshold: threshold}
}

// failure records one failed recovery attempt and reports whether the failure
// alert is now due. It returns true exactly once per outage.
func (p *outagePolicy) failure(now time.Time) (fire bool, attempts int) {
	if p == nil {
		return false, 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.outageStart.IsZero() {
		p.outageStart = now
	}
	p.failures++
	if p.threshold <= 0 || p.alerted || p.failures < p.threshold {
		return false, p.failures
	}
	p.alerted = true
	return true, p.failures
}

// healthy records a confirmed-healthy tunnel. It reports whether a "recovered"
// notice is due, which is only the case when a failure alert was actually sent
// for this outage.
func (p *outagePolicy) healthy(now time.Time) (recovered bool, outage time.Duration, attempts int) {
	if p == nil {
		return false, 0, 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	recovered = p.alerted
	if !p.outageStart.IsZero() {
		outage = now.Sub(p.outageStart)
	}
	attempts = p.failures

	p.failures = 0
	p.alerted = false
	p.outageStart = time.Time{}
	return recovered, outage, attempts
}

// snapshot returns the current consecutive-failure count (for status.json).
func (p *outagePolicy) snapshot() (failures int, alerted bool) {
	if p == nil {
		return 0, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failures, p.alerted
}

// alertEvent is one notification to deliver.
type alertEvent struct {
	Kind     string
	Message  string // already redacted
	Attempts int
	Outage   time.Duration
}

// hookRunner executes one hook command. It is a seam so tests never spawn a
// shell; production uses runHookProcess.
type hookRunner func(ctx context.Context, command string, env []string) (string, error)

// alertDispatcher delivers events to the failure/recovered hooks off the
// recovery path.
type alertDispatcher struct {
	failureCmd   string
	recoveredCmd string
	region       string
	timeout      time.Duration
	secrets      []string // exact values scrubbed from hook output before logging
	verbose      bool
	run          hookRunner

	queue chan alertEvent
	done  chan struct{}
	wg    sync.WaitGroup
}

const alertQueueSize = 8

func newAlertDispatcher(failureCmd, recoveredCmd, region string, timeout time.Duration, secrets []string, verbose bool) *alertDispatcher {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &alertDispatcher{
		failureCmd:   strings.TrimSpace(failureCmd),
		recoveredCmd: strings.TrimSpace(recoveredCmd),
		region:       region,
		timeout:      timeout,
		secrets:      secrets,
		verbose:      verbose,
		run:          runHookProcess,
		queue:        make(chan alertEvent, alertQueueSize),
		done:         make(chan struct{}),
	}
}

// start launches the single delivery worker. Events are delivered one at a time
// and in order, so a failure alert always precedes its recovered notice.
func (d *alertDispatcher) start() {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		for {
			select {
			case ev := <-d.queue:
				d.deliver(ev)
			case <-d.done:
				// Drain whatever is already queued, then stop.
				for {
					select {
					case ev := <-d.queue:
						d.deliver(ev)
					default:
						return
					}
				}
			}
		}
	}()
}

// stop drains the queue and waits for the worker. Tests use it to make
// delivery deterministic; the daemon never needs to call it.
func (d *alertDispatcher) stop() {
	close(d.done)
	d.wg.Wait()
}

// send queues an event without ever blocking the caller. If the queue is full
// the event is dropped and logged: losing a notification is better than
// stalling a recovery.
func (d *alertDispatcher) send(ev alertEvent) {
	if d == nil {
		return
	}
	select {
	case d.queue <- ev:
	default:
		log.Printf("alert queue full; dropped %q event", ev.Kind)
	}
}

func (d *alertDispatcher) deliver(ev alertEvent) {
	cmd := d.failureCmd
	if ev.Kind == alertEventRecovered {
		cmd = d.recoveredCmd
	}
	if cmd == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()

	out, err := d.run(ctx, cmd, d.hookEnv(ev))
	out = strings.TrimSpace(pia.Redact(out, d.secrets...))
	switch {
	case err != nil:
		// Never propagated: a broken alert path must not affect recovery.
		log.Printf("alert hook (%s) failed: %v %s", ev.Kind, redactErr(err, d.secrets), out)
	case d.verbose:
		log.Printf("alert hook (%s) delivered %s", ev.Kind, out)
	}
}

// hookEnv builds the hook's environment. It is an allow-list: the PIA account
// credentials in this process's environment are NOT inherited.
func (d *alertDispatcher) hookEnv(ev alertEvent) []string {
	env := []string{
		"PIA_WG_EVENT=" + ev.Kind,
		"PIA_WG_MESSAGE=" + ev.Message,
		"PIA_WG_REGION=" + d.region,
		fmt.Sprintf("PIA_WG_ATTEMPTS=%d", ev.Attempts),
		fmt.Sprintf("PIA_WG_OUTAGE_SECONDS=%d", int64(ev.Outage.Seconds())),
	}
	for _, key := range []string{"PATH", "HOME", "TZ", "LANG", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PIA_WG_ALERT_") {
			env = append(env, kv)
		}
	}
	return env
}

// runHookProcess runs command via `sh -c` with the supplied environment and
// returns its combined, size-capped output.
func runHookProcess(ctx context.Context, command string, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = env
	var buf cappedBuffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if ctx.Err() != nil {
		return buf.String(), fmt.Errorf("hook timed out: %w", ctx.Err())
	}
	return buf.String(), err
}

// cappedBuffer keeps at most 2 KiB so a chatty hook cannot fill the log.
type cappedBuffer struct{ bytes.Buffer }

func (c *cappedBuffer) Write(p []byte) (int, error) {
	const max = 2048
	if remaining := max - c.Len(); remaining > 0 {
		if len(p) > remaining {
			c.Buffer.Write(p[:remaining])
		} else {
			c.Buffer.Write(p)
		}
	}
	return len(p), nil // always claim success so the child never blocks on a full pipe
}

// redactErr scrubs an error's text.
func redactErr(err error, secrets []string) string {
	if err == nil {
		return ""
	}
	return pia.Redact(err.Error(), secrets...)
}
