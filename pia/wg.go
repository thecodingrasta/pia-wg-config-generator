package pia

import (
	"bytes"
	"log"
	"strings"
	"text/template"

	"github.com/pkg/errors"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// IPv6Mode controls how IPv6 traffic is handled in the generated WireGuard config.
type IPv6Mode int

const (
	// IPv6ModeOn routes all traffic including IPv6 through the VPN (default).
	IPv6ModeOn IPv6Mode = iota
	// IPv6ModeOff produces an IPv4-only config (AllowedIPs = 0.0.0.0/0).
	IPv6ModeOff
	// IPv6ModeKill routes IPv6 through the VPN and adds ip6tables PostUp/PostDown
	// rules as a killswitch — blocking any IPv6 traffic not going through the tunnel.
	IPv6ModeKill
)

type PIAWgGenerator struct {
	pia        PIAWgClient
	verbose    bool
	serverName bool
	privatekey string
	publickey  string
	ipv6Mode   IPv6Mode
}

type PIAWgGeneratorConfig struct {
	Verbose    bool
	ServerName bool
	PrivateKey string
	PublicKey  string
	IPv6Mode   IPv6Mode
}

type templateConfig struct {
	Address             string
	AllowedIPs          string
	DNS                 string
	Endpoint            string
	EndpointPort        int
	PrivateKey          string
	PublicKey           string
	PersistentKeepalive string
	ServerCommonName    string
	PostUp              string
	PostDown            string
}

type GenerateResult struct {
	Config string
	Key    AddKeyResult
}

type GenerateResult struct {
	Config string
	Key    AddKeyResult
}

func NewPIAWgGenerator(pia PIAWgClient, config PIAWgGeneratorConfig) *PIAWgGenerator {
	return &PIAWgGenerator{
		pia:        pia,
		verbose:    config.Verbose,
		serverName: config.ServerName,
		privatekey: config.PrivateKey,
		publickey:  config.PublicKey,
		ipv6Mode:   config.IPv6Mode,
	}
}

// Generate retains backwards compatibility (string only).
func (p *PIAWgGenerator) Generate() (string, error) {
	result, err := p.GenerateWithMetadata()
	if err != nil {
		return "", err
	}
	return result.Config, nil
}

// GenerateWithMetadata returns both the config and the AddKeyResult metadata (required for Port Forwarding).
func (p *PIAWgGenerator) GenerateWithMetadata() (GenerateResult, error) {
	var result GenerateResult

	if p.verbose {
		log.Println("Getting PIA token")
	}
	token, err := p.pia.GetToken()
	if err != nil {
		return result, errors.Wrap(err, "Error Getting PIA Token")
	}

	if p.verbose {
		log.Println("Generating WireGuard keys")
	}
	privatekey, publickey, err := p.generateKeys()
	if err != nil {
		return result, errors.Wrap(err, "Error Generating WireGuard Keys")
	}

	if p.verbose {
		log.Println("Registering WireGuard Public Key With PIA")
	}
	key, err := p.pia.AddKey(token, publickey)
	if err != nil {
		return result, errors.Wrap(err, "Error Adding WireGuard Public Key To PIA Account")
	}

	// The API may return a non-OK status with HTTP 200; treat it as an error.
	if key.Status != "" && key.Status != "OK" {
		return result, errors.Errorf("AddKey returned non-OK status: %s", key.Status)
	}

	if p.verbose {
		log.Println("Generating WireGuard config")
	}
	config, err := p.generateConfig(key, privatekey)
	if err != nil {
		return result, errors.Wrap(err, "Error Generating WireGuard Config")
	}

	result.Config = config
	result.Key = key
	return result, nil
}

func (p *PIAWgGenerator) generateKeys() (string, string, error) {
	if p.privatekey != "" && p.publickey != "" {
		return p.privatekey, p.publickey, nil
	}

	privateKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return "", "", errors.Wrap(err, "Failed To Generate Private Key")
	}
	if p.verbose {
		log.Println("Private Key Generated")
	}

	publicKey := privateKey.PublicKey()
	if p.verbose {
		log.Println("Public Key Generated")
	}

	return privateKey.String(), publicKey.String(), nil
}

func (p *PIAWgGenerator) generateConfig(key AddKeyResult, privatekey string) (string, error) {
	tpl, err := template.New("config").Parse(wireguardConfigTemplate)
	if err != nil {
		return "", errors.Wrap(err, "Error Parsing WireGuard Config Template")
	}

	var serverCommonName string
	if p.serverName {
		server := p.pia.getMetadataServerForRegion()
		serverCommonName = server.Cn
	}

	endpointPort := key.ServerPort
	if endpointPort <= 0 {
		return "", errors.New("Invalid Server Port Returned By API")
	}
	if strings.TrimSpace(key.ServerIP) == "" {
		return "", errors.New("Invalid Server IP Returned By API")
	}
	if len(key.DNSServers) == 0 || strings.TrimSpace(key.DNSServers[0]) == "" {
		return "", errors.New("No DNS Servers Returned By API")
	}

	// Determine AllowedIPs and optional firewall hooks based on IPv6 mode.
	var allowedIPs, postUp, postDown string
	switch p.ipv6Mode {
	case IPv6ModeOff:
		// IPv4-only tunnel — drop all IPv6.
		allowedIPs = "0.0.0.0/0"
	case IPv6ModeKill:
		// Route IPv6 through VPN and enforce a killswitch via ip6tables.
		allowedIPs = "0.0.0.0/0, ::/0"
		postUp = "ip6tables -I OUTPUT ! -o %i -j REJECT"
		postDown = "ip6tables -D OUTPUT ! -o %i -j REJECT"
	default: // IPv6ModeOn
		// Route all traffic including IPv6 through VPN.
		allowedIPs = "0.0.0.0/0, ::/0"
	}

	tc := templateConfig{
		PrivateKey:          privatekey,
		PublicKey:           key.ServerKey,
		Endpoint:            key.ServerIP,
		EndpointPort:        endpointPort,
		DNS:                 key.DNSServers[0],
		Address:             key.PeerIP,
		AllowedIPs:          allowedIPs,
		PersistentKeepalive: "25",
		ServerCommonName:    serverCommonName,
		PostUp:              postUp,
		PostDown:            postDown,
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, tc); err != nil {
		return "", errors.Wrap(err, "Error Executing WireGuard Config Template")
	}

	return buf.String(), nil
}

// wireguardConfigTemplate is a standard WireGuard INI config.
// ServerCommonName is written as a comment — it is not a valid WireGuard key
// and would cause wg / wg-quick to reject the config if written bare.
var wireguardConfigTemplate = `[Interface]
PrivateKey = {{.PrivateKey}}
Address = {{.Address}}
DNS = {{.DNS}}
{{- if .PostUp }}
PostUp = {{.PostUp}}
PostDown = {{.PostDown}}
{{- end }}
[Peer]
PublicKey = {{.PublicKey}}
AllowedIPs = {{.AllowedIPs}}
Endpoint = {{.Endpoint}}:{{.EndpointPort}}
PersistentKeepalive = {{.PersistentKeepalive}}
{{- if .ServerCommonName }}
# ServerCommonName = {{.ServerCommonName}}
{{- end }}`
