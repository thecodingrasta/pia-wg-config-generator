package pia

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// LifetimeRecord persists what we have learned about how long a PIA WireGuard
// registration actually survives before the server evicts the peer. This is the
// evidence base for a lifetime-aware refresh cadence instead of a hardcoded
// 12h guess.
//
// Observed root cause (2026-10): a registration died at ~11h45m — BELOW PIA's
// 24h token TTL — so the trigger is server-side peer eviction, not token
// expiry. The only robust response is to (a) refresh proactively well inside
// the measured lifetime and (b) react immediately to a lost handshake. This
// record feeds (a); the refresh loop's health check drives (b).
type LifetimeRecord struct {
	Version int `json:"version"`
	// CurrentRegistrationUTC is when the active registration was created.
	CurrentRegistrationUTC time.Time `json:"current_registration_utc"`
	// ObservedLifetimesSeconds holds the measured age (seconds) of past
	// registrations at the moment their handshake was lost. Newest last.
	ObservedLifetimesSeconds []int64 `json:"observed_lifetimes_seconds"`
	// LastObservedLossUTC is when we last recorded a registration dying.
	LastObservedLossUTC time.Time `json:"last_observed_loss_utc,omitempty"`
}

const (
	lifetimeRecordVersion   = 1
	lifetimeRecordFileName  = "registration_lifetime.json"
	maxObservedLifetimes    = 20
	lifetimeSafetyFraction  = 0.5 // refresh at <= 50% of measured lifetime
	minLifetimeAwareRefresh = 30 * time.Minute
)

// LifetimeStore reads and writes the lifetime record under a state directory.
type LifetimeStore struct {
	path string
}

func NewLifetimeStore(stateDir string) *LifetimeStore {
	return &LifetimeStore{path: filepath.Join(stateDir, lifetimeRecordFileName)}
}

func (s *LifetimeStore) Load() (LifetimeRecord, error) {
	var rec LifetimeRecord
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return LifetimeRecord{Version: lifetimeRecordVersion}, nil
		}
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		// Corrupt record must not wedge the daemon — start fresh.
		return LifetimeRecord{Version: lifetimeRecordVersion}, nil
	}
	if rec.Version == 0 {
		rec.Version = lifetimeRecordVersion
	}
	return rec, nil
}

func (s *LifetimeStore) Save(rec LifetimeRecord) error {
	rec.Version = lifetimeRecordVersion
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteState(s.path, data, 0644)
}

// RecordNewRegistration marks the birth time of a freshly created registration.
func (s *LifetimeStore) RecordNewRegistration(now time.Time) (LifetimeRecord, error) {
	rec, err := s.Load()
	if err != nil {
		return rec, err
	}
	rec.CurrentRegistrationUTC = now.UTC()
	if err := s.Save(rec); err != nil {
		return rec, err
	}
	return rec, nil
}

// RecordObservedLoss appends the lifetime (now - current registration birth) to
// the observed set when a handshake loss is detected. It is a no-op when the
// birth time is unknown or the computed lifetime is non-positive (clock skew).
func (s *LifetimeStore) RecordObservedLoss(now time.Time) (LifetimeRecord, error) {
	rec, err := s.Load()
	if err != nil {
		return rec, err
	}
	age, ok := RegistrationAge(rec.CurrentRegistrationUTC, now)
	if !ok || age <= 0 {
		return rec, nil
	}
	rec.ObservedLifetimesSeconds = append(rec.ObservedLifetimesSeconds, int64(age.Seconds()))
	if len(rec.ObservedLifetimesSeconds) > maxObservedLifetimes {
		rec.ObservedLifetimesSeconds = rec.ObservedLifetimesSeconds[len(rec.ObservedLifetimesSeconds)-maxObservedLifetimes:]
	}
	rec.LastObservedLossUTC = now.UTC()
	if err := s.Save(rec); err != nil {
		return rec, err
	}
	return rec, nil
}

// MeasuredLifetime returns the most conservative (shortest) observed lifetime,
// which is the safe basis for the refresh cadence. ok is false when nothing has
// been measured yet.
func (r LifetimeRecord) MeasuredLifetime() (time.Duration, bool) {
	if len(r.ObservedLifetimesSeconds) == 0 {
		return 0, false
	}
	sorted := append([]int64(nil), r.ObservedLifetimesSeconds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return time.Duration(sorted[0]) * time.Second, true
}

// ExpectedExpiryUTC estimates when the current registration will be evicted,
// based on the measured lifetime. ok is false when either the birth time or a
// measured lifetime is unknown.
func (r LifetimeRecord) ExpectedExpiryUTC() (time.Time, bool) {
	if r.CurrentRegistrationUTC.IsZero() {
		return time.Time{}, false
	}
	life, ok := r.MeasuredLifetime()
	if !ok {
		return time.Time{}, false
	}
	return r.CurrentRegistrationUTC.UTC().Add(life), true
}

// Where an expected-expiry estimate came from.
const (
	// LifetimeMeasured means the estimate uses a lifetime actually observed
	// when a previous registration was evicted.
	LifetimeMeasured = "measured"
	// LifetimeFallback means no eviction has been observed yet, so the estimate
	// is the configured fallback lifetime: a PLANNED replacement deadline, not
	// a prediction of when PIA will evict.
	LifetimeFallback = "fallback"
)

// ExpectedExpiry always yields an estimate once the registration's birth is
// known, labelled with its source. With a measured lifetime it is birth +
// shortest observed lifetime. Before any eviction is observed it is birth +
// fallback (capped by userMaxAge when set), labelled LifetimeFallback. The zero
// time and an empty source mean the birth is unknown.
func (r LifetimeRecord) ExpectedExpiry(fallback, userMaxAge time.Duration) (time.Time, string) {
	if r.CurrentRegistrationUTC.IsZero() {
		return time.Time{}, ""
	}
	if life, ok := r.MeasuredLifetime(); ok {
		return r.CurrentRegistrationUTC.UTC().Add(life), LifetimeMeasured
	}
	life := fallback
	if userMaxAge > 0 && (life <= 0 || userMaxAge < life) {
		life = userMaxAge
	}
	if life <= 0 {
		return time.Time{}, ""
	}
	return r.CurrentRegistrationUTC.UTC().Add(life), LifetimeFallback
}

// SafeRefreshInterval computes the proactive refresh cadence. When a lifetime
// has been measured it returns safetyFraction * measured (clamped to a sane
// floor). Otherwise it falls back to the caller-supplied configured interval,
// which is the conservative behaviour before any eviction has been observed.
//
// userMaxAge, when > 0, hard-caps the interval regardless of measurement, so an
// operator can force an aggressive cadence (e.g. --max-registration-age=5h).
func (r LifetimeRecord) SafeRefreshInterval(configured time.Duration, userMaxAge time.Duration) time.Duration {
	interval := configured

	if life, ok := r.MeasuredLifetime(); ok {
		safe := time.Duration(float64(life) * lifetimeSafetyFraction)
		if safe < minLifetimeAwareRefresh {
			safe = minLifetimeAwareRefresh
		}
		interval = safe
	}

	if userMaxAge > 0 {
		cap := time.Duration(float64(userMaxAge) * lifetimeSafetyFraction)
		if cap < minLifetimeAwareRefresh {
			cap = minLifetimeAwareRefresh
		}
		if interval > cap {
			interval = cap
		}
	}

	if interval < minLifetimeAwareRefresh {
		interval = minLifetimeAwareRefresh
	}
	return interval
}

// atomicWriteState mirrors main.atomicWriteFile for use inside the pia package
// (write to a temp file in the same dir, then rename).
func atomicWriteState(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
