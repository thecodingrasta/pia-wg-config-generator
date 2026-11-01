package pia

import (
	"strings"
	"testing"
)

// All values below are synthetic. They are shaped like the real ones (44-char
// base64 WireGuard keys, long opaque tokens/payloads/signatures, p1234567-style
// usernames) but are not valid credentials for anything.
const (
	fakeWGKey    = "FAKEk3yFAKEk3yFAKEk3yFAKEk3yFAKEk3yFAKE12="
	fakePubKey   = "PUBLICkeyPUBLICkeyPUBLICkeyPUBLICkey1234="
	fakeToken    = "FAKEtokenFAKEtokenFAKEtokenFAKEtokenFAKEtoken0123456789abcdef"
	fakePayload  = "eyJ0b2tlbiI6IkZBS0UiLCJwb3J0Ijo0MDAwMCwiZXhwaXJlc19hdCI6IjIwMjYifQ=="
	fakeSig      = "FAKEsignatureFAKEsignatureFAKEsignature0123456789ABCDEFGH=="
	fakeUsername = "p0000000"
	fakePassword = "Sup3rS3cretFakePassw0rd!"
)

// secretsThatMustNeverSurvive are checked against every redacted output.
var secretsThatMustNeverSurvive = []string{
	fakeWGKey, fakeToken, fakePayload, fakeSig, fakeUsername, fakePassword,
}

func TestRedact_RealisticErrorStrings(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// keep must still be present: the redaction must not destroy the
		// diagnostic value of the message.
		keep []string
	}{
		{
			name: "addKey transport error embeds token in the URL",
			in:   `Error Executing Request: Get "https://98.159.234.245:1337/addKey?pt=` + fakeToken + `&pubkey=` + fakePubKey + `": dial tcp 98.159.234.245:1337: i/o timeout`,
			keep: []string{"98.159.234.245:1337", "i/o timeout", "addKey"},
		},
		{
			name: "bindPort error embeds payload and signature",
			in:   `Get "https://10.169.0.1:19999/bindPort?payload=` + fakePayload + `&signature=` + fakeSig + `": context deadline exceeded`,
			keep: []string{"10.169.0.1:19999", "context deadline exceeded", "bindPort"},
		},
		{
			name: "getSignature error embeds the account token",
			in:   `Get "https://10.169.0.1:19999/getSignature?token=` + fakeToken + `": dial tcp 10.169.0.1:19999: connect: network is unreachable`,
			keep: []string{"network is unreachable", "getSignature"},
		},
		{
			name: "basic-auth credentials in a URL",
			in:   `token request failed: https://` + fakeUsername + `:` + fakePassword + `@www.privateinternetaccess.com/gtoken/generateToken: HTTP 401`,
			keep: []string{"www.privateinternetaccess.com", "HTTP 401"},
		},
		{
			name: "JSON error body echoing the token",
			in:   `HTTP 403: {"status":"ERROR","message":"invalid token","token":"` + fakeToken + `"}`,
			keep: []string{"HTTP 403", "invalid token"},
		},
		{
			name: "WireGuard config line",
			in:   "[Interface]\nPrivateKey = " + fakeWGKey + "\nAddress = 10.100.6.45/32\n",
			keep: []string{"[Interface]", "Address = 10.100.6.45/32"},
		},
		{
			name: "curl fallback message with username and password flags",
			in:   `curl failed: exit status 22 (username=` + fakeUsername + ` password=` + fakePassword + `)`,
			keep: []string{"curl failed", "exit status 22"},
		},
		{
			name: "bare username embedded in a sentence",
			in:   `account ` + fakeUsername + ` is rate limited, try again later`,
			keep: []string{"is rate limited, try again later"},
		},
		{
			name: "authorization header",
			in:   `request failed with Authorization: Basic cDAwMDAwMDA6U3VwM3JTM2NyZXRGYWtlUGFzc3cwcmQh`,
			keep: []string{"request failed"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.in, fakeUsername, fakePassword)
			for _, secret := range secretsThatMustNeverSurvive {
				if strings.Contains(got, secret) {
					t.Fatalf("secret %q survived redaction:\n in: %s\nout: %s", secret, tc.in, got)
				}
			}
			for _, want := range tc.keep {
				if !strings.Contains(got, want) {
					t.Errorf("redaction destroyed useful context %q:\nout: %s", want, got)
				}
			}
		})
	}
}

// The knowledge of the exact username/password must catch values no heuristic
// would, for example a password with an unusual shape in free text.
func TestRedact_KnownValuesAreScrubbedEverywhere(t *testing.T) {
	in := "auth failed for hunter2hunter2 using pw swordfish!"
	got := Redact(in, "hunter2hunter2", "swordfish!")
	if strings.Contains(got, "hunter2hunter2") || strings.Contains(got, "swordfish!") {
		t.Fatalf("known secret survived: %q", got)
	}
}

// Ordinary diagnostics must pass through untouched, otherwise alerts become
// useless.
func TestRedact_LeavesOrdinaryDiagnosticsAlone(t *testing.T) {
	cases := []string{
		"gluetun reports VPN status \"stopped\"",
		"regeneration failed after 6 attempts: config generation: client setup: dial tcp: lookup serverlist.piaservers.net: no such host",
		"docker restart \"media-gluetun\" failed: 404 Not Found: No such container",
		"open /gluetun/wireguard/registration_lifetime.json: permission denied",
		"endpoint 98.159.234.245:1337 unreachable",
	}
	for _, in := range cases {
		if got := Redact(in, fakeUsername, fakePassword); got != in {
			t.Errorf("ordinary text altered:\n in: %s\nout: %s", in, got)
		}
	}
}

func TestRedact_ShortKnownValuesAreIgnored(t *testing.T) {
	// A 3-character "secret" would shred normal text; it must be ignored.
	if got := Redact("the cat sat", "cat"); got != "the cat sat" {
		t.Fatalf("short known value altered text: %q", got)
	}
}

func TestRedact_EmptyInput(t *testing.T) {
	if got := Redact(""); got != "" {
		t.Fatalf("empty input changed: %q", got)
	}
}
