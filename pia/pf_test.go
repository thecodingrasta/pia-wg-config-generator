package pia

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Validate we can get a signature and decode the payload
func TestPFClient_GetSignature_OK(t *testing.T) {
	payloadObj := PFPayload{
		Token:     "t",
		Port:      35238,
		ExpiresAt: time.Now().Add(30 * time.Minute).UTC(),
		CreatedAt: time.Now().UTC(),
	}
	payloadBytes, _ := json.Marshal(payloadObj)
	payloadB64 := base64.StdEncoding.EncodeToString(payloadBytes)

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/getSignature"):
			_ = json.NewEncoder(w).Encode(PFSignatureResponse{
				Status:    "OK",
				Payload:   payloadB64,
				Signature: "sig",
			})
		case strings.HasPrefix(r.URL.Path, "/bindPort"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(struct {
				Status  string `json:"status"`
				Message string `json:"message"`
			}{"OK", "port successfully bound"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	gateway := strings.TrimPrefix(ts.URL, "https://")
	client := NewPFClient(false)
	client.httpClient = ts.Client() // reuse test server TLS settings

	sig, payload, err := client.GetSignature(gateway, "token")
	if err != nil {
		t.Fatalf("GetSignature Error: %v", err)
	}
	if sig.Signature != "sig" {
		t.Fatalf("Expected Signature 'sig', got %q", sig.Signature)
	}
	if payload.Port != 35238 {
		t.Fatalf("Expected Port 35238, got %d", payload.Port)
	}
}

// Validate we're catching bad get signature requests
func TestPFClient_GetSignature_Non200(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer ts.Close()

	gateway := strings.TrimPrefix(ts.URL, "https://")
	client := NewPFClient(false)
	client.httpClient = ts.Client()

	_, _, err := client.GetSignature(gateway, "token")
	if err == nil {
		t.Fatalf("Expected An Error")
	}
}

// Validate we can catch a bad status during a signature request
func TestPFClient_GetSignature_StatusNotOK(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(PFSignatureResponse{
			Status: "COMPUTER_SAYS_NO",
		})
	}))
	defer ts.Close()

	gateway := strings.TrimPrefix(ts.URL, "https://")
	client := NewPFClient(false)
	client.httpClient = ts.Client()

	_, _, err := client.GetSignature(gateway, "token")
	if err == nil {
		t.Fatalf("Expected An Error")
	}
}

// Validate we can catch dodgy base64 payloads
func TestPFClient_GetSignature_InvalidBase64(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(PFSignatureResponse{
			Status:    "OK",
			Payload:   "###totallyNotAbase64bitch###",
			Signature: "sig",
		})
	}))
	defer ts.Close()

	gateway := strings.TrimPrefix(ts.URL, "https://")
	client := NewPFClient(false)
	client.httpClient = ts.Client()

	_, _, err := client.GetSignature(gateway, "token")
	if err == nil {
		t.Fatalf("Expected An Error")
	}
}

// Validate HTTP non-200 is caught for BindPort
func TestPFClient_BindPort_Non200(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()

	gateway := strings.TrimPrefix(ts.URL, "https://")
	client := NewPFClient(false)
	client.httpClient = ts.Client()

	err := client.BindPort(gateway, PFSignatureResponse{Payload: "p", Signature: "s"})
	if err == nil {
		t.Fatalf("Expected An Error")
	}
}

// Validate that a non-OK JSON status body in BindPort is caught even when HTTP is 200
func TestPFClient_BindPort_StatusNotOK(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		}{"ERROR", "signature expired"})
	}))
	defer ts.Close()

	gateway := strings.TrimPrefix(ts.URL, "https://")
	client := NewPFClient(false)
	client.httpClient = ts.Client()

	err := client.BindPort(gateway, PFSignatureResponse{Payload: "p", Signature: "s"})
	if err == nil {
		t.Fatalf("Expected An Error when BindPort returns non-OK status in body")
	}
	if !strings.Contains(err.Error(), "ERROR") {
		t.Fatalf("Expected error message to contain 'ERROR', got: %v", err)
	}
}

// Validate the happy path: HTTP 200 + {"status":"OK"} succeeds
func TestPFClient_BindPort_OK(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		}{"OK", "port successfully bound"})
	}))
	defer ts.Close()

	gateway := strings.TrimPrefix(ts.URL, "https://")
	client := NewPFClient(false)
	client.httpClient = ts.Client()

	err := client.BindPort(gateway, PFSignatureResponse{Payload: "p", Signature: "s"})
	if err != nil {
		t.Fatalf("Expected no error on successful BindPort, got: %v", err)
	}
}

func TestNormalizeGateway(t *testing.T) {
	tests := map[string]string{
		"":                    "",
		"10.100.0.1":          "10.100.0.1:19999",
		" 10.100.0.1 ":        "10.100.0.1:19999",
		"10.100.0.1:19999":    "10.100.0.1:19999",
		"example.invalid:443": "example.invalid:443",
	}

	for input, want := range tests {
		if got := NormalizeGateway(input); got != want {
			t.Fatalf("NormalizeGateway(%q) = %q, want %q", input, got, want)
		}
	}
}
