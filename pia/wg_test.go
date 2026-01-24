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

	// If the test explicitly wants to use AddKeyRes, return it mostly as-is.
	// This allows tests like ServerPort==0 to remain 0.
	if p.UseAddKeyRes {
		res := p.AddKeyRes
		if res.ServerKey == "" {
			res.ServerKey = publickey
		}
		return res, nil
	}

	// Otherwise, provide sane defaults.
	return AddKeyResult{
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
		"AllowedIPs = 0.0.0.0/0",
		"Endpoint = 1.2.3.4:1337",
		"PersistentKeepalive = 25",
	}

	for _, s := range wantContains {
		if !strings.Contains(got, s) {
			t.Fatalf("expected config to contain %q, got:\n%s", s, got)
		}
	}

	if strings.Contains(got, "ServerCommonName =") {
		t.Fatalf("did not expect ServerCommonName line, got:\n%s", got)
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

	if !strings.Contains(got, "ServerCommonName = mock-server") {
		t.Fatalf("expected ServerCommonName, got:\n%s", got)
	}
}

func TestPIAWgGenerator_GenerateWithMetadata_ReturnsKey(t *testing.T) {
	fake := &piaClientFake{
		UseAddKeyRes: true,
		AddKeyRes: AddKeyResult{
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

func TestPIAWgGenerator_GenerateWithMetadata_GetTokenError(t *testing.T) {
	fake := &piaClientFake{
		TokenErr: errors.New("nope"),
	}
	gen := NewPIAWgGenerator(fake, PIAWgGeneratorConfig{
		PrivateKey: "k",
		PublicKey:  "p",
	})

	_, err := gen.GenerateWithMetadata()
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestPIAWgGenerator_GenerateWithMetadata_AddKeyError(t *testing.T) {
	fake := &piaClientFake{
		AddKeyErr: errors.New("bad addkey"),
	}
	gen := NewPIAWgGenerator(fake, PIAWgGeneratorConfig{
		PrivateKey: "k",
		PublicKey:  "p",
	})

	_, err := gen.GenerateWithMetadata()
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestPIAWgGenerator_GenerateWithMetadata_ServerPortZeroErrors(t *testing.T) {
	fake := &piaClientFake{
		UseAddKeyRes: true,
		AddKeyRes: AddKeyResult{
			ServerIP:   "1.2.3.4",
			ServerPort: 0,
			DNSServers: []string{"1.1.1.1"},
			PeerIP:     "4.5.6.7",
			ServerKey:  "serverkey",
		},
	}

	gen := NewPIAWgGenerator(fake, PIAWgGeneratorConfig{
		PrivateKey: "k",
		PublicKey:  "p",
	})

	_, err := gen.GenerateWithMetadata()
	if err == nil {
		t.Fatalf("expected error when ServerPort is 0")
	}
}
