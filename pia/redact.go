package pia

import (
	"regexp"
	"strings"
)

// redactedPlaceholder replaces every secret value found in a string.
const redactedPlaceholder = "<redacted>"

var (
	// scheme://user:pass@host  ->  scheme://<redacted>@host
	reURLUserInfo = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://)[^/\s@]+@`)

	// Query-string parameters PIA's APIs put credentials in: addKey?pt=...,
	// getSignature?token=..., bindPort?payload=...&signature=...
	reQuerySecret = regexp.MustCompile(`(?i)([?&;])(pt|token|payload|signature|password|passwd|username|user|privatekey|private_key|key|auth|authorization)=([^&\s"'<>]*)`)

	// JSON / key=value / "Key: value" forms of the same names, including a
	// WireGuard config line such as "PrivateKey = <base64>".
	reKeyedSecret = regexp.MustCompile(`(?i)\b(privatekey|private_key|token|payload|signature|password|passwd|username)\b(["']?\s*[:=]\s*["']?)([^\s"',&}\]]+)`)

	// "Authorization: Basic xxx" / "Bearer xxx".
	reAuthHeader = regexp.MustCompile(`(?i)\b(basic|bearer)\s+[A-Za-z0-9+/=._\-]{8,}`)

	// A long unbroken base64-ish run. WireGuard keys are 44 characters, PIA
	// tokens, payloads and signatures are longer. Checked for a letter AND a
	// digit below so ordinary long words and paths are left alone.
	reLongBlob = regexp.MustCompile(`[A-Za-z0-9+/_\-]{40,}={0,2}`)
)

// Redact removes credentials from s so it is safe for status.json, logs and
// outbound alerts. It is deliberately aggressive: a false positive costs a
// little diagnostic detail, a false negative leaks an account.
//
// known lists exact secret values (account username, password, webhook URL)
// that are scrubbed wherever they appear, even when no heuristic would match.
func Redact(s string, known ...string) string {
	if s == "" {
		return s
	}

	for _, k := range known {
		k = strings.TrimSpace(k)
		// Very short values would shred ordinary text; real credentials are
		// longer than this.
		if len(k) >= 4 {
			s = strings.ReplaceAll(s, k, redactedPlaceholder)
		}
	}

	s = reURLUserInfo.ReplaceAllString(s, "${1}"+redactedPlaceholder+"@")
	s = reQuerySecret.ReplaceAllString(s, "${1}${2}="+redactedPlaceholder)
	s = reKeyedSecret.ReplaceAllString(s, "${1}${2}"+redactedPlaceholder)
	s = reAuthHeader.ReplaceAllString(s, "${1} "+redactedPlaceholder)
	s = reLongBlob.ReplaceAllStringFunc(s, func(m string) string {
		if hasLetter(m) && hasDigit(m) {
			return redactedPlaceholder
		}
		return m
	})
	return s
}

func hasLetter(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}

func hasDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}
