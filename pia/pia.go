package pia

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/benburkert/dns"
	"github.com/pkg/errors"
)

type PIAWgClient interface {
	GetToken() (string, error)
	AddKey(token, publickey string) (AddKeyResult, error)
	getMetadataServerForRegion() Server
}

type Region string
type ServerList map[Region][]Server

type PIAClient struct {
	region           string
	wireguardServers ServerList
	metadataServers  ServerList
	rawServerList    PIAServerList // cached so GetAvailableRegions never re-fetches
	username         string
	password         string
	verbose          bool
	portForwarding   bool
	caCert           []byte
	// curlPath is the path to an OpenSSL-compiled curl binary. If empty, only
	// the system curl is tried. Set via the CURL_PATH environment variable —
	// required on Windows where the inbox curl uses Schannel/LibreSSL and
	// PIA's WAF rejects it.
	curlPath string
}

type PIAServerList struct {
	Regions []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Country     string `json:"country"`
		AutoRegion  bool   `json:"auto_region"`
		DNS         string `json:"dns"`
		PortForward bool   `json:"port_forward"`
		Geo         bool   `json:"geo"`
		Servers     struct {
			Meta []Server `json:"meta"`
			Wg   []Server `json:"wg"`
		} `json:"servers"`
	} `json:"regions"`
}

type RegionInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Country     string `json:"country"`
	AutoRegion  bool   `json:"auto_region"`
	PortForward bool   `json:"port_forward"`
}

type AddKeyResult struct {
	Status     string   `json:"status"`
	ServerKey  string   `json:"server_key"`
	ServerPort int      `json:"server_port"`
	ServerIP   string   `json:"server_ip"`
	ServerVip  string   `json:"server_vip"`
	Gateway    string   `json:"gateway"`
	PeerIP     string   `json:"peer_ip"`
	PeerPubkey string   `json:"peer_pubkey"`
	DNSServers []string `json:"dns_servers"`
}

type Server struct {
	Cn string
	IP string
}

// NewPIAClient constructs a client, fetches the server list once, and resolves
// the requested region. All subsequent calls reuse the cached list.
// Set the CURL_PATH env var to point at an OpenSSL curl binary on Windows.
func NewPIAClient(username, password, region string, verbose bool, portForwarding bool) (*PIAClient, error) {
	piaClient := PIAClient{
		username:       username,
		password:       password,
		region:         region,
		verbose:        verbose,
		portForwarding: portForwarding,
		curlPath:       strings.TrimSpace(os.Getenv("CURL_PATH")),
	}

	serverList, err := piaClient.getServerList()
	if err != nil {
		return nil, err
	}
	piaClient.rawServerList = serverList

	piaClient.region, err = piaClient.resolveRegionID(region, serverList)
	if err != nil {
		return nil, err
	}

	piaClient.metadataServers, err = piaClient.generateMetadataServerList(serverList)
	if err != nil {
		return nil, err
	}

	piaClient.wireguardServers, err = piaClient.generateWireguardServerList(serverList)
	if err != nil {
		return nil, err
	}

	return &piaClient, nil
}

// LogLine logs msg via the standard logger if verbose is true.
// It is called by token_fetcher.go and any other internal code that needs
// conditional verbosity without scattering if-checks everywhere.
func (p *PIAClient) LogLine(verbose bool, msg string) {
	if verbose {
		log.Print(msg)
	}
}

// GetToken returns a valid PIA auth token, trying three strategies in order:
//
//  1. In-process token cache (avoids any network call for repeated calls within
//     the same 23-hour window — important because the daemon calls GetToken
//     twice per refresh cycle: once for AddKey and once for PF).
//
//  2. Curl-based fetch against PIA's public API endpoint. This path handles
//     Windows correctly by using an OpenSSL curl if CURL_PATH is set (PIA's
//     WAF rejects Schannel/LibreSSL TLS fingerprints).
//
//  3. Fallback: regional metadata server via Go's native TLS with PIA's CA cert
//     pinned. Works reliably on Linux/macOS where Go's TLS is accepted.
func (p *PIAClient) GetToken() (string, error) {
	// 1. Cache hit — no network call needed.
	if token, _, err := readCachedToken(time.Now()); err == nil {
		p.LogLine(p.verbose, "PIA: using cached token")
		return token, nil
	}

	// 2. Curl path (public endpoint; handles Windows WAF / OpenSSL requirement).
	token, curlErr := p.fetchTokenViaCurl(context.Background())
	if curlErr == nil {
		_ = writeCachedToken(token, 23*time.Hour)
		return token, nil
	}

	// Rate-limiting is fatal — a fallback attempt would only make it worse.
	if curlErr == ErrTooManyAttempts {
		return "", curlErr
	}

	p.LogLine(p.verbose, fmt.Sprintf("curl token fetch failed (%v); falling back to metadata server", curlErr))

	// 3. Metadata server fallback (regional server, CA-cert-pinned TLS).
	token, err := p.getTokenFromMetadataServer()
	if err != nil {
		return "", fmt.Errorf("all token methods failed — curl: %v; metadata server: %v", curlErr, err)
	}

	_ = writeCachedToken(token, 23*time.Hour)
	return token, nil
}

// getTokenFromMetadataServer authenticates against the regional PIA metadata
// server using Basic Auth over CA-cert-pinned TLS. This is the original
// token endpoint and works reliably on Linux/macOS.
func (p *PIAClient) getTokenFromMetadataServer() (string, error) {
	server := p.getMetadataServerForRegion()
	u := fmt.Sprintf("https://%v/authv3/generateToken", server.Cn)

	resp, err := p.executePIARequest(server, u, "")
	if err != nil {
		return "", errors.Wrap(err, "metadata server token request failed")
	}

	var tokenResp struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", errors.Wrap(err, "error decoding token response")
	}
	if tokenResp.Token == "" {
		return "", errors.New("metadata server returned an empty token — check your credentials")
	}

	p.LogLine(p.verbose, "Got token via metadata server")
	return tokenResp.Token, nil
}

// AddKey registers a WireGuard public key with PIA and returns peer config metadata.
func (p *PIAClient) AddKey(token, publickey string) (AddKeyResult, error) {
	var addKeyResp AddKeyResult
	server := p.getWireguardServerForRegion()

	u := fmt.Sprintf("https://%v:1337/addKey?pt=%v&pubkey=%v",
		server.Cn,
		url.QueryEscape(token),
		url.QueryEscape(publickey),
	)

	resp, err := p.executePIARequest(server, u, token)
	if err != nil {
		return addKeyResp, errors.Wrap(err, "Error Executing Request")
	}

	if err := json.NewDecoder(resp.Body).Decode(&addKeyResp); err != nil {
		return addKeyResp, errors.Wrap(err, "Error Decoding Add Key Response")
	}

	return addKeyResp, nil
}

func (p *PIAClient) getWireguardServerForRegion() Server {
	if p.verbose {
		log.Print("Getting WireGuard Server For Region: ", p.region)
	}
	return p.wireguardServers[Region(p.region)][0]
}

func (p *PIAClient) getMetadataServerForRegion() Server {
	if p.verbose {
		log.Print("Getting Metadata Server For Region: ", p.region)
	}
	return p.metadataServers[Region(p.region)][0]
}

// getServerList fetches the live PIA server list and strips the trailing
// base64 blob that PIA appends after the JSON object.
func (p *PIAClient) getServerList() (PIAServerList, error) {
	var serverList PIAServerList

	resp, err := http.Get("https://serverlist.piaservers.net/vpninfo/servers/v6")
	if err != nil {
		return PIAServerList{}, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return PIAServerList{}, err
	}

	// Strip the base64 blob appended after the closing brace.
	respString := string(respBytes)
	lastBracketInd := strings.LastIndex(respString, "}")
	if lastBracketInd < 0 {
		return PIAServerList{}, errors.New("getServerList: response contained no JSON object")
	}
	safeJSON := respString[:lastBracketInd+1]

	if err := json.Unmarshal([]byte(safeJSON), &serverList); err != nil {
		return PIAServerList{}, err
	}

	return serverList, nil
}

func (p *PIAClient) generateWireguardServerList(list PIAServerList) (ServerList, error) {
	servers := ServerList{}

	for _, r := range list.Regions {
		if p.portForwarding && !r.PortForward {
			continue
		}
		for _, server := range r.Servers.Wg {
			servers[Region(r.ID)] = append(servers[Region(r.ID)], Server{
				Cn: server.Cn,
				IP: server.IP,
			})
		}
	}

	if len(servers) == 0 {
		if p.portForwarding {
			return nil, errors.New("No Servers Found With Port Forwarding Enabled")
		}
		return nil, errors.New("No Servers Found")
	}

	return servers, nil
}

func (p *PIAClient) generateMetadataServerList(list PIAServerList) (ServerList, error) {
	servers := ServerList{}

	for _, r := range list.Regions {
		if p.portForwarding && !r.PortForward {
			continue
		}
		for _, server := range r.Servers.Meta {
			servers[Region(r.ID)] = append(servers[Region(r.ID)], Server{
				Cn: server.Cn,
				IP: server.IP,
			})
		}
	}

	if len(servers) == 0 {
		if p.portForwarding {
			return nil, errors.New("No Metadata Servers Found With Port Forwarding Enabled")
		}
		return nil, errors.New("No Metadata Servers Found")
	}

	return servers, nil
}

func (p *PIAClient) executePIARequest(server Server, rawURL, token string) (*http.Response, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	// Use Basic Auth only for the token endpoint (token == "").
	// For subsequent calls the token is passed as a query parameter.
	if token == "" {
		req.SetBasicAuth(p.username, p.password)
	}

	if err := p.downloadPIACertificate(); err != nil {
		return nil, errors.Wrap(err, "Error Downloading CA Certificate")
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(p.caCert)

	// Build a custom DNS resolver that maps the server's CN directly to its
	// IP address, bypassing public DNS (which may not resolve PIA's internal CNs).
	zone := &dns.Zone{
		Origin: "",
		TTL:    5 * time.Minute,
		RRs: dns.RRSet{
			server.Cn: {
				dns.TypeA: []dns.Record{
					&dns.A{A: net.ParseIP(server.IP)},
				},
			},
		},
	}
	mux := new(dns.ResolveMux)
	mux.Handle(dns.TypeANY, zone.Origin, zone)
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: (&dns.Client{
			Resolver: mux,
		}).Dial,
	}

	dialer := &net.Dialer{Resolver: resolver}
	dialContext := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, addr)
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: caCertPool},
			DialContext:     dialContext,
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	// Buffer the body so callers can re-read it and we can inspect it here.
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %v: %s", resp.StatusCode, string(body))
	}

	return resp, nil
}

// downloadPIACertificate lazily fetches PIA's CA certificate from their
// official GitHub repo. Subsequent calls are no-ops (cached on the struct).
func (p *PIAClient) downloadPIACertificate() error {
	if len(p.caCert) > 0 {
		return nil
	}

	resp, err := http.Get("https://raw.githubusercontent.com/pia-foss/desktop/master/daemon/res/ca/rsa_4096.crt")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	p.caCert, err = io.ReadAll(resp.Body)
	return err
}

func (p *PIAClient) resolveRegionID(input string, list PIAServerList) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", errors.New("Region Cannot Be Empty")
	}

	needle := strings.ToLower(trimmed)

	for _, r := range list.Regions {
		if p.portForwarding && !r.PortForward {
			continue
		}
		if strings.ToLower(r.ID) == needle {
			return r.ID, nil
		}
		if strings.ToLower(r.Name) == needle {
			return r.ID, nil
		}
	}

	for _, r := range list.Regions {
		if p.portForwarding && !r.PortForward {
			continue
		}
		for _, server := range append(r.Servers.Meta, r.Servers.Wg...) {
			if strings.ToLower(server.Cn) == needle {
				return r.ID, nil
			}
		}
	}

	return "", errors.New("Unknown Region: " + input)
}

// GetAvailableRegions returns region metadata using the already-cached server
// list — no additional network call is made.
func (p *PIAClient) GetAvailableRegions() ([]RegionInfo, error) {
	regions := make([]RegionInfo, 0, len(p.rawServerList.Regions))
	for _, r := range p.rawServerList.Regions {
		regions = append(regions, RegionInfo{
			ID:          r.ID,
			Name:        r.Name,
			Country:     r.Country,
			AutoRegion:  r.AutoRegion,
			PortForward: r.PortForward,
		})
	}
	return regions, nil
}
