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
	username         string
	password         string
	verbose          bool
	portForwarding   bool
	caCert           []byte
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

// NewPIAClient
func NewPIAClient(username, password, region string, verbose bool, portForwarding bool) (*PIAClient, error) {
	piaClient := PIAClient{
		username:       username,
		password:       password,
		region:         region,
		verbose:        verbose,
		portForwarding: portForwarding,
	}

	// Get list of servers
	serverList, err := piaClient.getServerList()
	if err != nil {
		return nil, err
	}

	piaClient.region, err = piaClient.resolveRegionID(region, serverList)
	if err != nil {
		return nil, err
	}

	// Set servers
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

// GetToken
func (p *PIAClient) GetToken() (string, error) {
	server := p.getMetadataServerForRegion()

	url := fmt.Sprintf("https://%v/authv3/generateToken", server.Cn)

	// Send Request
	resp, err := p.executePIARequest(server, url, "")
	if err != nil {
		return "", errors.Wrap(err, "error executing request")
	}

	// Parse Response
	var tokenResp struct {
		Token string `json:"token"`
	}

	err = json.NewDecoder(resp.Body).Decode(&tokenResp)
	if err != nil {
		return "", errors.Wrap(err, "error decoding token response")
	}

	if p.verbose {
		log.Print("Got token: ", tokenResp.Token)
	}

	return tokenResp.Token, nil
}

// AddKey
func (p *PIAClient) AddKey(token, publickey string) (AddKeyResult, error) {
	var addKeyResp AddKeyResult
	server := p.getWireguardServerForRegion()

	// Build HTTP Request
	url := fmt.Sprintf("https://%v:6421/addKey?pt=%v&pubkey=%v", server.Cn, url.QueryEscape(token), url.QueryEscape(publickey))

	// Send Request
	resp, err := p.executePIARequest(server, url, token)
	if err != nil {
		return addKeyResp, errors.Wrap(err, "Error Executing Request")
	}

	// Parse Response
	err = json.NewDecoder(resp.Body).Decode(&addKeyResp)
	if err != nil {
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

// getSeverList returns a list of servers from the PIA API
func (p *PIAClient) getServerList() (PIAServerList, error) {
	var serverList PIAServerList

	resp, err := http.Get("https://serverlist.piaservers.net/vpninfo/servers/v6")
	if err != nil {
		return PIAServerList{}, err
	}

	// Strip the base64 garbage
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return PIAServerList{}, err
	}
	respString := string(respBytes)
	lastBracketInd := strings.LastIndex(respString, "}")
	safeJSON := respString[:lastBracketInd+1]

	// Parse the JSON
	err = json.Unmarshal([]byte(safeJSON), &serverList)
	if err != nil {
		return PIAServerList{}, err
	}

	// Return list of servers
	return serverList, nil
}

// generateWireguardServerList
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

// generateMetadataServerList
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

func (p *PIAClient) executePIARequest(server Server, url, token string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	// Set header to JSON
	req.Header.Set("Content-Type", "application/json")

	// Set basic auth
	if token == "" {
		req.SetBasicAuth(p.username, p.password)
	}

	// Add certificate to shared pool
	err = p.downloadPIACertificate()
	if err != nil {
		return nil, errors.Wrap(err, "Error Downloading CA Certificate")
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(p.caCert)

	// Create DNS resolver for PIA addresses
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

	// Set custom DNS server
	dialer := &net.Dialer{
		Resolver: resolver,
	}

	dialContext := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, addr)
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: caCertPool,
			},
			DialContext: dialContext,
		},
	}

	// Execute the request
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	// Log the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	// Return error if status code is not 200
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Status Code %v, Response Body: %s", resp.StatusCode, string(body))
	}

	return resp, nil
}

// downloadPIACertificate downloads the PIA certificate
func (p *PIAClient) downloadPIACertificate() error {
	// caCert already loaded
	if len(p.caCert) > 0 {
		return nil
	}

	// Download certificate
	resp, err := http.Get("https://raw.githubusercontent.com/pia-foss/desktop/master/daemon/res/ca/rsa_4096.crt")
	if err != nil {
		return err
	}

	// Parse certificate
	p.caCert, err = io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	return nil
}

func (p *PIAClient) resolveRegionID(input string, list PIAServerList) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", errors.New("Region Cannot Be Empty")
	}

	needle := strings.ToLower(trimmed)

	for _, r := range list.Regions {
		if strings.ToLower(r.ID) == needle {
			return r.ID, nil
		}
		if strings.ToLower(r.Name) == needle {
			return r.ID, nil
		}
	}

	return "", errors.New("Unknown Region: " + input)
}

func (p *PIAClient) GetAvailableRegions() ([]RegionInfo, error) {
	serverList, err := p.getServerList()
	if err != nil {
		return nil, err
	}

	regions := make([]RegionInfo, 0, len(serverList.Regions))
	for _, r := range serverList.Regions {
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
