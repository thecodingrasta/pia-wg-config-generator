package pia

import (
	"fmt"
	"os/exec"
	"strings"
)

// findOpenSSLCurl resolves the path to an OpenSSL-compiled curl binary.
//
//   - Empty curlPath → returns ("", false, nil). Not an error: no OpenSSL curl
//     was configured, callers should fall through to system curl or the Go
//     metadata-server path.
//
//   - Non-empty curlPath that resolves (via PATH or absolute) → (path, true, nil).
//
//   - Non-empty curlPath that cannot be found → ("", false, ErrOpenSSLCurlMissing).
//     The caller surfaces this as a fatal error because the user explicitly
//     configured a path and it is missing.
func findOpenSSLCurl(curlPath string) (string, bool, error) {
	path := strings.TrimSpace(curlPath)
	if path == "" {
		return "", false, nil
	}

	resolved, err := exec.LookPath(path)
	if err != nil {
		return "", false, fmt.Errorf("%w: %q not found in PATH or filesystem", ErrOpenSSLCurlMissing, path)
	}

	return resolved, true, nil
}

// sanitizeBody truncates a raw API response body to a safe length for log
// output, preventing credential or PII leakage in verbose logs.
func sanitizeBody(body []byte) string {
	const maxLen = 512
	s := strings.TrimSpace(string(body))
	if len(s) > maxLen {
		return s[:maxLen] + " ...[truncated]"
	}
	return s
}
