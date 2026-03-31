package pia

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/pkg/errors"
)

const piaTokenURL = "https://www.privateinternetaccess.com/api/client/v2/token"

var (
	ErrTokenDenied        = errors.New("PIA token denied")
	ErrCurlNotFound       = errors.New("curl not found")
	ErrOpenSSLCurlMissing = errors.New("OpenSSL curl path not set or not found")
)

type curlDebug struct {
	HTTPCode    string
	ContentType string
	FinalURL    string
}

type tokenJSON struct {
	Token string `json:"token"`
}

type curlResult struct {
	RawBody   []byte
	Debug     curlDebug
	ExitError error
}

func (p *PIAClient) fetchTokenViaCurl(ctx context.Context) (string, error) {
	// 1) System curl first (works on Linux, will probably fail on Windows due to PIAs WAF).
	systemCurlPath, systemCurlOk := findSystemCurl()
	if runtime.GOOS == "windows" {
		systemCurlOk = false // TEMPORARY
	}
	// 2) Known-good OpenSSL curl path (included or user defined).
	openSSLCurlPath, openSSLCurlOk, err := findOpenSSLCurl(p.curlPath)
	if err != nil {
		return "", errors.Wrap(err, "token request failed (curl)")
	}
	if !openSSLCurlOk { //&& runtime.GOOS == "windows"
		p.LogLine(p.verbose, "Unable to attempt OpenSSL Curl")
	}

	var attempts []struct {
		name string
		path string
		ok   bool
	}
	attempts = append(attempts, struct {
		name string
		path string
		ok   bool
	}{
		name: "system curl",
		path: systemCurlPath,
		ok:   systemCurlOk,
	})
	attempts = append(attempts, struct {
		name string
		path string
		ok   bool
	}{
		name: "OpenSSL curl",
		path: openSSLCurlPath,
		ok:   openSSLCurlOk,
	})

	var lastErr error
	for _, a := range attempts {
		if !a.ok {
			continue
		}

		res := runCurlTokenRequest(ctx, a.path, p.username, p.password)

		// curl itself failed to run
		if res.ExitError != nil {
			lastErr = fmt.Errorf("%s failed: %w", a.name, res.ExitError)
			if p.verbose {
				p.logCurlFailure(a.name, res, lastErr)
			}
			continue
		}

		// Interpret HTTP response
		token, err := parseTokenFromCurlOutput(res.RawBody)
		if err == nil {
			p.LogLine(p.verbose, fmt.Sprintf("Token fetched successfully via %s (http_code=%s content_type=%s final_url=%s)",
				a.name, res.Debug.HTTPCode, res.Debug.ContentType, res.Debug.FinalURL))
			return token, nil
		}

		// Surface good diagnostics
		lastErr = fmt.Errorf("%s token request denied: http_code=%s content_type=%s final_url=%s body=%s",
			a.name,
			res.Debug.HTTPCode,
			res.Debug.ContentType,
			res.Debug.FinalURL,
			sanitizeBody(res.RawBody),
		)

		if p.verbose {
			p.logCurlFailure(a.name, res, lastErr)
		}

		// If we get a 429 code, don't bother trying again - We'll have to wait.
		if strings.TrimSpace(res.Debug.HTTPCode) == "429" || isTooManyAttempts(string(res.RawBody)) {
			return "", ErrTooManyAttempts
		}
	}

	if lastErr == nil {
		// No curl available at all
		if runtime.GOOS == "windows" {
			return "", fmt.Errorf("%w (install OpenSSL Curl or set %s)", ErrCurlNotFound, openSSLCurlEnvHint())
		}
		return "", ErrCurlNotFound
	}

	return "", lastErr
}

func runCurlTokenRequest(ctx context.Context, curlPath string, username string, password string) curlResult {
	args := []string{
		"-sS", "-L",
		"--request", "POST",
		"-H", "Accept: application/json",
		//"-A", "curl/8.5.0",
		"--form", "username=" + username,
		"--form", "password=" + password,
		"-w", "\nhttp_code=%{http_code}\ncontent_type=%{content_type}\nfinal_url=%{url_effective}\n",
		piaTokenURL,
	}

	cmd := exec.CommandContext(ctx, curlPath, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Tight timeout outside if caller didn't set one.
	// (Caller should pass ctx with timeout; but we also protect ourselves.)
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		ctx, cancel = context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		cmd = exec.CommandContext(ctx, curlPath, args...)
	}

	err := cmd.Run()
	outBytes := stdout.Bytes()

	// If curl exits non-zero, still capture stderr + whatever stdout it produced.
	if err != nil {
		combined := outBytes
		if len(stderr.Bytes()) > 0 {
			combined = append(combined, []byte("\n---stderr---\n")...)
			combined = append(combined, stderr.Bytes()...)
		}

		return curlResult{
			RawBody:   combined,
			Debug:     curlDebug{},
			ExitError: err,
		}
	}

	body, dbg := splitCurlBodyAndDebug(outBytes)
	return curlResult{
		RawBody:   body,
		Debug:     dbg,
		ExitError: nil,
	}
}

func splitCurlBodyAndDebug(out []byte) ([]byte, curlDebug) {
	// Curl output format:
	// <body>
	// http_code=200
	// content_type=application/json; charset=utf-8
	// final_url=https://...
	s := string(out)
	httpIdx := strings.LastIndex(s, "\nhttp_code=")
	if httpIdx == -1 {
		// No footer found, treat everything as body.
		return out, curlDebug{}
	}

	body := strings.TrimRight(s[:httpIdx], "\n")
	footer := s[httpIdx:]

	dbg := curlDebug{
		HTTPCode:    extractFooterValue(footer, "http_code="),
		ContentType: extractFooterValue(footer, "content_type="),
		FinalURL:    extractFooterValue(footer, "final_url="),
	}

	return []byte(body), dbg
}

func extractFooterValue(footer string, key string) string {
	for _, line := range strings.Split(footer, "\n") {
		if strings.HasPrefix(line, key) {
			return strings.TrimSpace(strings.TrimPrefix(line, key))
		}
	}
	return ""
}

func parseTokenFromCurlOutput(body []byte) (string, error) {
	trim := strings.TrimSpace(string(body))
	if trim == "" {
		return "", fmt.Errorf("%w: empty response", ErrTokenDenied)
	}

	// PIA sometimes returns plain text like: "HTTP Token: Access denied."
	if strings.HasPrefix(trim, "HTTP ") || strings.Contains(strings.ToLower(trim), "access denied") {
		return "", ErrTokenDenied
	}

	var decoded tokenJSON
	if err := json.Unmarshal([]byte(trim), &decoded); err != nil {
		// If it’s not JSON, treat as denial but keep the error for debugging.
		return "", fmt.Errorf("%w: invalid JSON (%v)", ErrTokenDenied, err)
	}

	token := strings.TrimSpace(decoded.Token)
	if token == "" {
		return "", ErrTokenDenied
	}

	return token, nil
}

func findSystemCurl() (string, bool) {
	// On Windows: curl.exe is usually present in System32
	// On Linux/macOS: curl in PATH
	name := "curl"
	if runtime.GOOS == "windows" {
		name = "curl.exe"
	}

	path, err := exec.LookPath(name)
	if err == nil && strings.TrimSpace(path) != "" {
		return path, true
	}
	return "", false
}

func openSSLCurlEnvHint() string {
	// whichever you want to document, but we accept 3.
	return "CURL_PATH"
}

func (p *PIAClient) logCurlFailure(name string, res curlResult, err error) {
	if !p.verbose {
		return
	}
	p.LogLine(true, fmt.Sprintf("%s failure: %v", name, err))
	if res.Debug.HTTPCode != "" || res.Debug.ContentType != "" || res.Debug.FinalURL != "" {
		p.LogLine(true, fmt.Sprintf("%s debug: http_code=%s content_type=%s final_url=%s",
			name, res.Debug.HTTPCode, res.Debug.ContentType, res.Debug.FinalURL))
	}
	p.LogLine(true, fmt.Sprintf("%s body: %s", name, sanitizeBody(res.RawBody)))
}
