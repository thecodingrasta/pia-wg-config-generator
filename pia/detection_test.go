package pia

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// controlServer is a configurable stand-in for gluetun's control server.
type controlServer struct {
	status      string // value of /v1/vpn/status
	publicIP    string // value of public_ip
	ipHTTPCode  int    // 0 = 200
	ipBody      string // raw override for the public-IP body
	statusCalls int32
	ipCalls     int32
}

func (c *controlServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/vpn/status":
			atomic.AddInt32(&c.statusCalls, 1)
			_, _ = w.Write([]byte(`{"status":"` + c.status + `"}`))
		case "/v1/publicip/ip":
			atomic.AddInt32(&c.ipCalls, 1)
			if c.ipHTTPCode != 0 {
				w.WriteHeader(c.ipHTTPCode)
				return
			}
			if c.ipBody != "" {
				_, _ = w.Write([]byte(c.ipBody))
				return
			}
			_, _ = w.Write([]byte(`{"public_ip":"` + c.publicIP + `","country":"United Kingdom"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func check(t *testing.T, c *controlServer) TunnelState {
	t.Helper()
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	return CheckTunnelViaControlServer(context.Background(), srv.URL, time.Second)
}

// THE REGRESSION that motivated the second signal. Measured against real
// gluetun v3.41.3 after a silent peer eviction: status "running" in 57 of 60
// one-second samples while the public IP was empty in 60 of 60.
func TestCheckTunnel_SilentEvictionRunningButNoPublicIPIsUnhealthy(t *testing.T) {
	st := check(t, &controlServer{status: "running", publicIP: ""})
	if st.Healthy {
		t.Fatalf("running + empty public IP must be unhealthy, got %+v", st)
	}
	if st.Status != "running" || !st.PublicIPKnown || st.PublicIPPresent {
		t.Fatalf("state not populated as expected: %+v", st)
	}
	if st.Err == nil || !strings.Contains(st.Err.Error(), "no public IP") {
		t.Fatalf("error should explain the cause, got %v", st.Err)
	}
}

func TestCheckTunnel_RunningWithPublicIPIsHealthy(t *testing.T) {
	st := check(t, &controlServer{status: "running", publicIP: "98.159.234.246"})
	if !st.Healthy || !st.PublicIPKnown || !st.PublicIPPresent || st.Err != nil {
		t.Fatalf("expected healthy with public IP, got %+v", st)
	}
}

func TestCheckTunnel_NotRunningIsUnhealthyWithoutAskingForIP(t *testing.T) {
	c := &controlServer{status: "stopped", publicIP: "98.159.234.246"}
	st := check(t, c)
	if st.Healthy {
		t.Fatalf("stopped must be unhealthy, got %+v", st)
	}
	if atomic.LoadInt32(&c.ipCalls) != 0 {
		t.Fatalf("public IP should not be queried when status already says down")
	}
}

// When the public-IP endpoint cannot be read (older gluetun, auth required),
// the daemon must NOT invent a failure: it falls back to status-only, otherwise
// a gluetun upgrade could trigger endless re-registration.
func TestCheckTunnel_PublicIPUnavailableFallsBackToStatusOnly(t *testing.T) {
	for name, c := range map[string]*controlServer{
		"404":        {status: "running", ipHTTPCode: http.StatusNotFound},
		"401 (auth)": {status: "running", ipHTTPCode: http.StatusUnauthorized},
		"500":        {status: "running", ipHTTPCode: http.StatusInternalServerError},
		"malformed":  {status: "running", ipBody: "not json"},
	} {
		t.Run(name, func(t *testing.T) {
			st := check(t, c)
			if !st.Healthy {
				t.Fatalf("unreadable public IP must not make a running tunnel unhealthy: %+v", st)
			}
			if st.PublicIPKnown {
				t.Fatalf("PublicIPKnown must be false when the endpoint is unreadable: %+v", st)
			}
		})
	}
}

func TestExpectedExpiry(t *testing.T) {
	birth := time.Date(2026, 10, 29, 20, 17, 49, 0, time.UTC)
	fallback := 12 * time.Hour

	t.Run("unknown birth yields nothing", func(t *testing.T) {
		exp, src := LifetimeRecord{}.ExpectedExpiry(fallback, 0)
		if !exp.IsZero() || src != "" {
			t.Fatalf("got %v %q", exp, src)
		}
	})

	t.Run("fallback before any measurement", func(t *testing.T) {
		exp, src := LifetimeRecord{CurrentRegistrationUTC: birth}.ExpectedExpiry(fallback, 0)
		if src != LifetimeFallback || !exp.Equal(birth.Add(12*time.Hour)) {
			t.Fatalf("got %v %q", exp, src)
		}
	})

	t.Run("measured lifetime wins and is the shortest", func(t *testing.T) {
		rec := LifetimeRecord{CurrentRegistrationUTC: birth, ObservedLifetimesSeconds: []int64{42300, 39000}}
		exp, src := rec.ExpectedExpiry(fallback, 0)
		if src != LifetimeMeasured || !exp.Equal(birth.Add(39000*time.Second)) {
			t.Fatalf("got %v %q", exp, src)
		}
	})

	t.Run("user max age caps the fallback", func(t *testing.T) {
		exp, src := LifetimeRecord{CurrentRegistrationUTC: birth}.ExpectedExpiry(fallback, 5*time.Hour)
		if src != LifetimeFallback || !exp.Equal(birth.Add(5*time.Hour)) {
			t.Fatalf("got %v %q", exp, src)
		}
	})
}
