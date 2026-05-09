package pia

import (
	"strings"
	"testing"
)

func TestSanitizeBody_EmptyIsExplicit(t *testing.T) {
	if got := sanitizeBody(nil); got != "<empty>" {
		t.Fatalf("expected <empty>, got %q", got)
	}
}

func TestSanitizeBody_CollapsesMultilineOutput(t *testing.T) {
	got := sanitizeBody([]byte("body\n---stderr---\ncurl: failed"))
	if strings.Contains(got, "\n") {
		t.Fatalf("expected newlines collapsed, got %q", got)
	}
	if !strings.Contains(got, "stderr") || !strings.Contains(got, "curl: failed") {
		t.Fatalf("expected stderr details preserved, got %q", got)
	}
}
