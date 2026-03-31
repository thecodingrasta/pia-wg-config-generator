// token_cache.go (pia package)
package pia

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type cachedTokenFile struct {
	Token      string    `json:"token"`
	ExpiresUTC time.Time `json:"expires_utc"`
	IssuedUTC  time.Time `json:"issued_utc"`
	Version    int       `json:"version"`
}

var (
	ErrNoCachedToken   = errors.New("no cached token")
	ErrTooManyAttempts = errors.New("PIA token endpoint rate-limited (too_many_attempts)")
)

func readCachedToken(now time.Time) (string, time.Time, error) {
	path, err := getTokenCachePath()
	if err != nil {
		return "", time.Time{}, err
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", time.Time{}, ErrNoCachedToken
		}
		return "", time.Time{}, err
	}

	var file cachedTokenFile
	if err := json.Unmarshal(b, &file); err != nil {
		// Corrupt cache -> treat as cache miss
		return "", time.Time{}, ErrNoCachedToken
	}

	token := strings.TrimSpace(file.Token)
	if token == "" {
		return "", time.Time{}, ErrNoCachedToken
	}

	// Expiry sanity: if missing/zero -> treat as miss
	if file.ExpiresUTC.IsZero() {
		return "", time.Time{}, ErrNoCachedToken
	}

	// Use UTC comparisons consistently
	nowUTC := now.UTC()

	// Small skew tolerance so we don’t return a token that’s about to die
	const skew = 30 * time.Second
	if nowUTC.After(file.ExpiresUTC.Add(-skew)) {
		return "", time.Time{}, ErrNoCachedToken
	}

	return token, file.ExpiresUTC, nil
}

func writeCachedToken(token string, ttl time.Duration) error {
	path, err := getTokenCachePath()
	if err != nil {
		return err
	}

	t := strings.TrimSpace(token)
	if t == "" {
		return errors.New("token cannot be empty")
	}

	now := time.Now().UTC()
	payload := cachedTokenFile{
		Token:      t,
		IssuedUTC:  now,
		ExpiresUTC: now.Add(ttl),
		Version:    1,
	}

	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}

	// Ensure directory exists (it should, but executable dirs can be weird in some setups)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Atomic write: write to temp file then rename
	tmp, err := os.CreateTemp(dir, ".pia_token.*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	// Best-effort cleanup on failure
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	// Best-effort restrictive perms on non-Windows
	// (Windows ignores chmod)
	if runtime.GOOS != "windows" {
		_ = os.Chmod(tmpName, 0600)
	}

	return os.Rename(tmpName, path)
}

// getTokenCachePath returns a path alongside the running binary.
// Cross-platform: uses os.Executable() and falls back to cwd.
func getTokenCachePath() (string, error) {
	exePath, err := os.Executable()
	if err == nil && strings.TrimSpace(exePath) != "" {
		exeDir := filepath.Dir(exePath)
		return filepath.Join(exeDir, ".pia_token.json"), nil
	}

	// Fallback: current working directory
	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		return "", cwdErr
	}
	return filepath.Join(cwd, ".pia_token.json"), nil
}

func isTooManyAttempts(body string) bool {
	// PIA returns {"status":"error","code":"too_many_attempts",...}
	return strings.Contains(body, "too_many_attempts") || strings.Contains(strings.ToLower(body), "try again later")
}
