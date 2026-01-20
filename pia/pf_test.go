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
			w.Write([]byte(`{"status":"OK"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	gateway := strings.TrimPrefix(ts.URL, "https://")
	client := NewPFClient(false)
	client.httpClient = ts.Client() // reuse server TLS settings

	sig, payload, err := client.GetSignature(gateway, "token")
	if err != nil {
		t.Fatalf("GetSignature error: %v", err)
	}
	if sig.Signature != "sig" {
		t.Fatalf("expected signature 'sig', got %q", sig.Signature)
	}
	if payload.Port != 35238 {
		t.Fatalf("expected port 35238, got %d", payload.Port)
	}
}

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
		t.Fatalf("expected error")
	}
}

func TestPFClient_GetSignature_StatusNotOK(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(PFSignatureResponse{
			Status: "NOPE",
		})
	}))
	defer ts.Close()

	gateway := strings.TrimPrefix(ts.URL, "https://")
	client := NewPFClient(false)
	client.httpClient = ts.Client()

	_, _, err := client.GetSignature(gateway, "token")
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestPFClient_GetSignature_InvalidBase64(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(PFSignatureResponse{
			Status:    "OK",
			Payload:   "###notbase64###",
			Signature: "sig",
		})
	}))
	defer ts.Close()

	gateway := strings.TrimPrefix(ts.URL, "https://")
	client := NewPFClient(false)
	client.httpClient = ts.Client()

	_, _, err := client.GetSignature(gateway, "token")
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestPFClient_BindPort_Non200(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()

	gateway := strings.TrimPrefix(ts.URL, "https://")
	client := NewPFClient(false)
	client.httpClient = ts.Client()

	err := client.BindPort(gateway, PFSignatureResponse{
		Payload:   "p",
		Signature: "s",
	})
	if err == nil {
		t.Fatalf("expected error")
	}
}
