package pia

import (
	"bytes"
	"log"
	"strings"
	"text/template"

	"github.com/pkg/errors"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type PIAWgGenerator struct {
	pia        PIAWgClient
	verbose    bool
	serverName bool
	privatekey string
	publickey  string
}

type PIAWgGeneratorConfig struct {
	Verbose    bool
	ServerName bool
	PrivateKey string
	PublicKey  string
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
	}
}

// Generate - retains backwards compatibility (string only).
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
		return result, errors.Wrap(err, "Error Getting Pia Token")
	}

	if p.verbose {
		log.Println("Generating WireGuard keys")
	}
	privatekey, publickey, err := p.generateKeys()
	if err != nil {
		return result, errors.Wrap(err, "Error Generating WireGuard Keys")
	}

	if p.verbose {
		log.Println("Registering Wire Guard Public Key With PIA")
	}
	key, err := p.pia.AddKey(token, publickey)
	if err != nil {
		return result, errors.Wrap(err, "Error Adding WireGuard Public Key To PIA Account")
	}

	if p.verbose {
		log.Println("Generating WireGuard config")
	}
	config, err := p.generateConfig(key, privatekey)
	if err != nil {
		return result, errors.Wrap(err, "Error Generating Wire Guard Config")
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

	tc := templateConfig{
		PrivateKey:          privatekey,
		PublicKey:           key.ServerKey,
		Endpoint:            key.ServerIP,
		EndpointPort:        endpointPort,
		DNS:                 key.DNSServers[0],
		Address:             key.PeerIP,
		AllowedIPs:          "0.0.0.0/0",
		PersistentKeepalive: "25",
		ServerCommonName:    serverCommonName,
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, tc); err != nil {
		return "", errors.Wrap(err, "Error Executing WireGuard Config Template")
	}

	return buf.String(), nil
}

var wireguardConfigTemplate = `[Interface]
PrivateKey = {{.PrivateKey}}
Address = {{.Address}}
DNS = {{.DNS}}
[Peer]
PublicKey = {{.PublicKey}}
AllowedIPs = {{.AllowedIPs}}
Endpoint = {{.Endpoint}}:{{.EndpointPort}}
PersistentKeepalive = {{.PersistentKeepalive}}
{{- if .ServerCommonName }}
ServerCommonName = {{.ServerCommonName}}
{{- end }}`
