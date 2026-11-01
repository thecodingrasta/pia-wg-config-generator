package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestDockerEndpoint(t *testing.T) {
	cases := []struct {
		name, host, socket string
		wantBase           string
		wantErr            string
	}{
		{name: "default is the unix socket", host: "", socket: "", wantBase: "http://docker"},
		{name: "explicit socket path", host: "", socket: "/run/custom.sock", wantBase: "http://docker"},
		{name: "unix scheme", host: "unix:///var/run/docker.sock", wantBase: "http://docker"},
		{name: "tcp to a proxy", host: "tcp://pia-wg-refresh-socket-proxy:2375", wantBase: "http://pia-wg-refresh-socket-proxy:2375"},
		{name: "http scheme", host: "http://proxy:2375/", wantBase: "http://proxy:2375"},
		{name: "whitespace trimmed", host: "  tcp://proxy:2375  ", wantBase: "http://proxy:2375"},
		{name: "https is refused, not downgraded", host: "https://proxy:2376", wantErr: "unsupported docker host"},
		{name: "ssh is refused", host: "ssh://user@host", wantErr: "unsupported docker host"},
		{name: "bare host is refused", host: "proxy:2375", wantErr: "unsupported docker host"},
		{name: "empty tcp address", host: "tcp://", wantErr: "invalid docker host"},
		{name: "path in tcp address", host: "tcp://proxy:2375/containers", wantErr: "invalid docker host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, base, err := dockerEndpoint(tc.host, tc.socket)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if client == nil || base != tc.wantBase {
				t.Fatalf("base = %q, want %q", base, tc.wantBase)
			}
		})
	}
}

// The restart call over TCP must be exactly what the least-privilege proxy
// allows: POST /containers/<name>/restart, and nothing else.
func TestRestartViaTCPSendsOnlyTheRestartCall(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	host := "tcp://" + strings.TrimPrefix(srv.URL, "http://")
	if err := restartDockerContainerVia(host, "/ignored.sock", "media-gluetun", false); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0] != "POST /containers/media-gluetun/restart?t=10" {
		t.Fatalf("unexpected Docker API traffic: %v", requests)
	}
}

// A proxy that refuses the call (403) must surface a clear error, not success.
func TestRestartViaTCPReportsProxyDenial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	err := restartDockerContainerVia("tcp://"+strings.TrimPrefix(srv.URL, "http://"), "", "media-gluetun", false)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected a 403 error, got %v", err)
	}
}

// --docker-socket keeps working when --docker-host is not given.
func TestRestartWithoutHostStillUsesTheSocketPath(t *testing.T) {
	err := restartDockerContainerVia("", "/nonexistent/docker.sock", "media-gluetun", false)
	if err == nil {
		t.Fatal("expected a dial error for a nonexistent socket")
	}
	if !strings.Contains(err.Error(), "docker restart") {
		t.Fatalf("unexpected error shape: %v", err)
	}
}
