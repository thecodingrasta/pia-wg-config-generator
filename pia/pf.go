package pia

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pkg/errors"
)

type PFSignatureResponse struct {
	Status    string `json:"status"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type PFPayload struct {
	Token     string    `json:"token"`
	Port      int       `json:"port"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

type PFClient struct {
	httpClient *http.Client
	verbose    bool
}

func NewPFClient(verbose bool) *PFClient {
	// PIA docs use curl -k; we mirror that here.
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // Gateway uses internal cert, aligning with the PIA docs
		},
	}

	return &PFClient{
		httpClient: &http.Client{Transport: transport, Timeout: 15 * time.Second},
		verbose:    verbose,
	}
}

func (p *PFClient) GetSignature(gateway string, token string) (PFSignatureResponse, PFPayload, error) {
	var sig PFSignatureResponse
	var payload PFPayload

	gw := normalizeGateway(gateway)
	if gw == "" {
		return sig, payload, errors.New("gateway is empty")
	}

	u := fmt.Sprintf("https://%s/getSignature?token=%s", gw, url.QueryEscape(token))
	resp, err := p.httpClient.Get(u)
	if err != nil {
		return sig, payload, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return sig, payload, fmt.Errorf("getSignature failed: %s", resp.Status)
	}

	if err := json.NewDecoder(resp.Body).Decode(&sig); err != nil {
		return sig, payload, err
	}
	if sig.Status != "OK" {
		return sig, payload, fmt.Errorf("getSignature returned status: %s", sig.Status)
	}

	decoded, err := base64.StdEncoding.DecodeString(sig.Payload)
	if err != nil {
		return sig, payload, errors.Wrap(err, "failed to decode payload")
	}

	if err := json.Unmarshal(decoded, &payload); err != nil {
		return sig, payload, errors.Wrap(err, "failed to parse decoded payload")
	}

	return sig, payload, nil
}

func (p *PFClient) BindPort(gateway string, sig PFSignatureResponse) error {
	gw := normalizeGateway(gateway)
	if gw == "" {
		return errors.New("gateway is empty")
	}

	form := url.Values{}
	form.Set("payload", sig.Payload)
	form.Set("signature", sig.Signature)

	u := fmt.Sprintf("https://%s/bindPort?%s", gw, form.Encode())
	resp, err := p.httpClient.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bindPort failed: %s", resp.Status)
	}

	return resp.Body.Close()
}

func normalizeGateway(gateway string) string {
	gw := strings.TrimSpace(gateway)
	if gw == "" {
		return ""
	}

	// retain normal 'host:port' values.
	if _, _, err := net.SplitHostPort(gw); err == nil {
		return gw
	}

	// Otherwise default to PIA PF port.
	return gw + ":19999"
}
