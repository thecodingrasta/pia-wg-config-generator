package pia

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type piaClientFake struct {
	Token        string
	TokenErr     error
	AddKeyRes    AddKeyResult
	UseAddKeyRes bool
	AddKeyErr    error
	MetaServer   Server
}

func (p *piaClientFake) getMetadataServerForRegion() Server {
	if p.MetaServer.Cn == "" {
		return Server{Cn: "mock-server", IP: "0.0.0.0"}
	}
	return p.MetaServer
}

func (p *piaClientFake) GetToken() (string, error) {
	if p.TokenErr != nil {
		return "", p.TokenErr
	}
	if p.Token == "" {
		return "mock-token", nil
	}
	return p.Token, nil
}

func (p *piaClientFake) AddKey(token, publickey string) (AddKeyResult, error) {
	if p.AddKeyErr != nil {
		return AddKeyResult{}, p.AddKeyErr
	}

	// If the test explicitly sets UseAddKeyRes, return it as-is so tests can
	// control edge-case fields (e.g. ServerPort==0).
	if p.UseAddKeyRes {
		res := p.AddKeyRes
		if res.ServerKey == "" {
			res.ServerKey = publickey
		}
		return res, nil
	}

	// Sane defaults for happy-path tests.
	return AddKeyResult{
		Status:     "OK",
		ServerIP:   "1.2.3.4",
		ServerPort: 1337,
		DNSServers: []string{"1.1.1.1"},
		PeerIP:     "4.5.6.7",
		ServerKey:  publickey,
	}, nil
}

func (p *piaClientFake) LogLine(enabled bool, msg string) {
	if !enabled {
		return
	}
	fmt.Println(time.Now().Format("2006/01/02 15:04:05"), msg)
}

// ---------- Basic generation ----------

func TestPIAWgGenerator_Generate_Basic(t *testing.T) {
	fake := &piaClientFake{}
	gen := NewPIAWgGenerator(fake, PIAWgGeneratorConfig{
		PrivateKey: "test_privatekey",
		PublicKey:  "test_publickey",
	})

	got, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate() returned error: %v", err)
	}

	wantContains := []string{
		"PrivateKey = test_privatekey",
		"Address = 4.5.6.7",
		"DNS = 1.1.1.1",
		"PublicKey = test_publickey",
		// Default mode is IPv6ModeOn → dual-stack
		"AllowedIPs = 0.0.0.0/0, ::/0",
		"Endpoint = 1.2.3.4:1337",
		"PersistentKeepalive = 25",
	}

	for _, s := range wantContains {
		if !strings.Contains(got, s) {
			t.Fatalf("expected config to contain %q, got:\n%s", s, got)
		}
	}

	// ServerCommonName must not appear as a bare key (only as a comment or not at all).
	if strings.Contains(got, "\nServerCommonName =") {
		t.Fatalf("ServerCommonName must not appear as a bare WireGuard key:\n%s", got)
	}
	// No PostUp/PostDown in default mode.
	if strings.Contains(got, "PostUp") || strings.Contains(got, "PostDown") {
		t.Fatalf("did not expect PostUp/PostDown in default IPv6ModeOn, got:\n%s", got)
	}
}

func TestPIAWgGenerator_Generate_WithServerCommonName(t *testing.T) {
	fake := &piaClientFake{
		MetaServer: Server{Cn: "mock-server", IP: "0.0.0.0"},
	}
	gen := NewPIAWgGenerator(fake, PIAWgGeneratorConfig{
		ServerName: true,
		PrivateKey: "test_privatekey",
		PublicKey:  "test_publickey",
	})

	got, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate() returned error: %v", err)
	}

	// Must appear as a comment, not a bare key.
	if !strings.Contains(got, "# ServerCommonName = mock-server") {
		t.Fatalf("expected '# ServerCommonName = mock-server' comment, got:\n%s", got)
	}
	if strings.Contains(got, "\nServerCommonName = mock-server") {
		t.Fatalf("ServerCommonName must be a comment, not a bare WireGuard key:\n%s", got)
	}
}

// ---------- IPv6 mode ----------

func TestPIAWgGenerator_IPv6Mode_On_ProducesDualStack(t *testing.T) {
	gen := NewPIAWgGenerator(&piaClientFake{}, PIAWgGeneratorConfig{
		PrivateKey: "pk",
		PublicKey:  "pub",
		IPv6Mode:   IPv6ModeOn,
	})

	got, err := gen.Generate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(got, "AllowedIPs = 0.0.0.0/0, ::/0") {
		t.Fatalf("IPv6ModeOn: expected dual-stack AllowedIPs, got:\n%s", got)
	}
	if strings.Contains(got, "PostUp") || strings.Contains(got, "PostDown") {
		t.Fatalf("IPv6ModeOn: did not expect PostUp/PostDown, got:\n%s", got)
	}
}

func TestPIAWgGenerator_IPv6Mode_Off_ProducesIPv4Only(t *testing.T) {
	gen := NewPIAWgGenerator(&piaClientFake{}, PIAWgGeneratorConfig{
		PrivateKey: "pk",
		PublicKey:  "pub",
		IPv6Mode:   IPv6ModeOff,
	})

	got, err := gen.Generate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(got, "AllowedIPs = 0.0.0.0/0") {
		t.Fatalf("IPv6ModeOff: expected IPv4-only AllowedIPs, got:\n%s", got)
	}
	if strings.Contains(got, "::/0") {
		t.Fatalf("IPv6ModeOff: must not contain ::/0, got:\n%s", got)
	}
	if strings.Contains(got, "PostUp") || strings.Contains(got, "PostDown") {
		t.Fatalf("IPv6ModeOff: did not expect PostUp/PostDown, got:\n%s", got)
	}
}

func TestPIAWgGenerator_IPv6Mode_Kill_ProducesRulesAndDualStack(t *testing.T) {
	gen := NewPIAWgGenerator(&piaClientFake{}, PIAWgGeneratorConfig{
		PrivateKey: "pk",
		PublicKey:  "pub",
		IPv6Mode:   IPv6ModeKill,
	})

	got, err := gen.Generate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(got, "AllowedIPs = 0.0.0.0/0, ::/0") {
		t.Fatalf("IPv6ModeKill: expected dual-stack AllowedIPs, got:\n%s", got)
	}
	if !strings.Contains(got, "PostUp") {
		t.Fatalf("IPv6ModeKill: expected PostUp rule, got:\n%s", got)
	}
	if !strings.Contains(got, "PostDown") {
		t.Fatalf("IPv6ModeKill: expected PostDown rule, got:\n%s", got)
	}
	if !strings.Contains(got, "ip6tables") {
		t.Fatalf("IPv6ModeKill: expected ip6tables in PostUp/PostDown, got:\n%s", got)
	}
	// PostUp/PostDown belong inside [Interface], before [Peer].
	interfaceIdx := strings.Index(got, "[Interface]")
	peerIdx := strings.Index(got, "[Peer]")
	postUpIdx := strings.Index(got, "PostUp")
	if interfaceIdx < 0 || peerIdx < 0 || postUpIdx < 0 {
		t.Fatalf("IPv6ModeKill: missing expected sections, got:\n%s", got)
	}
	if !(interfaceIdx < postUpIdx && postUpIdx < peerIdx) {
		t.Fatalf("IPv6ModeKill: PostUp must appear inside [Interface], before [Peer], got:\n%s", got)
	}
}

// ---------- Metadata ----------

func TestPIAWgGenerator_GenerateWithMetadata_ReturnsKey(t *testing.T) {
	fake := &piaClientFake{
		UseAddKeyRes: true,
		AddKeyRes: AddKeyResult{
			Status:     "OK",
			ServerIP:   "9.9.9.9",
			ServerPort: 51820,
			DNSServers: []string{"1.1.1.1"},
			PeerIP:     "10.0.0.2",
			ServerKey:  "serverkey",
		},
	}

	gen := NewPIAWgGenerator(fake, PIAWgGeneratorConfig{
		PrivateKey: "test_privatekey",
		PublicKey:  "test_publickey",
	})

	res, err := gen.GenerateWithMetadata()
	if err != nil {
		t.Fatalf("GenerateWithMetadata() returned error: %v", err)
	}

	if res.Config == "" {
		t.Fatalf("expected non-empty Config")
	}
	if res.Key.ServerIP != "9.9.9.9" || res.Key.ServerPort != 51820 {
		t.Fatalf("expected Key metadata to be returned, got: %+v", res.Key)
	}
}

func TestPIAWgGenerator_GenerateWithMetadata_NonOKStatusErrors(t *testing.T) {
	fake := &piaClientFake{
		UseAddKeyRes: true,
		AddKeyRes: AddKeyResult{
			Status:     "ERROR",
			ServerIP:   "1.2.3.4",
			ServerPort: 1337,
			DNSServers: []string{"1.1.1.1"},
			PeerIP:     "4.5.6.7",
		},
	}
	gen := NewPIAWgGenerator(fake, PIAWgGeneratorConfig{PrivateKey: "k", PublicKey: "p"})

	_, err := gen.GenerateWithMetadata()
	if err == nil {
		t.Fatalf("expected error when AddKey returns non-OK status")
	}
}

func TestPIAWgGenerator_GenerateWithMetadata_GetTokenError(t *testing.T) {
	fake := &piaClientFake{
		TokenErr: errors.New("nope"),
	}
	gen := NewPIAWgGenerator(fake, PIAWgGeneratorConfig{PrivateKey: "k", PublicKey: "p"})

	_, err := gen.GenerateWithMetadata()
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestPIAWgGenerator_GenerateWithMetadata_AddKeyError(t *testing.T) {
	fake := &piaClientFake{
		AddKeyErr: errors.New("bad addkey"),
	}
	gen := NewPIAWgGenerator(fake, PIAWgGeneratorConfig{PrivateKey: "k", PublicKey: "p"})

	_, err := gen.GenerateWithMetadata()
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestPIAWgGenerator_GenerateWithMetadata_ServerPortZeroErrors(t *testing.T) {
	fake := &piaClientFake{
		UseAddKeyRes: true,
		AddKeyRes: AddKeyResult{
			Status:     "OK",
			ServerIP:   "1.2.3.4",
			ServerPort: 0, // invalid
			DNSServers: []string{"1.1.1.1"},
			PeerIP:     "4.5.6.7",
			ServerKey:  "serverkey",
		},
	}

	gen := NewPIAWgGenerator(fake, PIAWgGeneratorConfig{PrivateKey: "k", PublicKey: "p"})

	_, err := gen.GenerateWithMetadata()
	if err == nil {
		t.Fatalf("expected error when ServerPort is 0")
	}
}
