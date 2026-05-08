package pia

import "testing"

func TestResolveRegionID_ByID_And_ByName(t *testing.T) {
	list := PIAServerList{
		Regions: []struct {
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
		}{
			{
				ID:          "uk_southampton",
				Name:        "UK Southampton",
				PortForward: true,
			},
		},
	}

	c := &PIAClient{}

	got, err := c.resolveRegionID("uk_southampton", list)
	if err != nil || got != "uk_southampton" {
		t.Fatalf("expected uk_southampton, got %q err=%v", got, err)
	}

	got, err = c.resolveRegionID("UK Southampton", list)
	if err != nil || got != "uk_southampton" {
		t.Fatalf("expected uk_southampton, got %q err=%v", got, err)
	}
}

func TestResolveRegionID_ByServerCommonName(t *testing.T) {
	list := PIAServerList{
		Regions: []struct {
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
		}{
			{
				ID:          "nl_netherlands-so",
				Name:        "NL Netherlands Streaming Optimized",
				PortForward: true,
				Servers: struct {
					Meta []Server `json:"meta"`
					Wg   []Server `json:"wg"`
				}{
					Meta: []Server{{Cn: "amsterdam405", IP: "154.47.21.141"}},
					Wg:   []Server{{Cn: "amsterdam404", IP: "154.47.21.134"}},
				},
			},
		},
	}

	c := &PIAClient{}
	got, err := c.resolveRegionID("amsterdam404", list)
	if err != nil || got != "nl_netherlands-so" {
		t.Fatalf("expected nl_netherlands-so, got %q err=%v", got, err)
	}
}

func TestResolveRegionID_Unknown(t *testing.T) {
	list := PIAServerList{Regions: []struct {
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
	}{}}

	c := &PIAClient{}
	_, err := c.resolveRegionID("does_not_exist", list)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestGenerateWireguardServerList_PortForwardFilter(t *testing.T) {
	list := PIAServerList{
		Regions: []struct {
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
		}{
			{
				ID:          "r1",
				Name:        "Region 1",
				PortForward: true,
				Servers: struct {
					Meta []Server `json:"meta"`
					Wg   []Server `json:"wg"`
				}{
					Wg: []Server{{Cn: "wg1", IP: "1.1.1.1"}},
				},
			},
			{
				ID:          "r2",
				Name:        "Region 2",
				PortForward: false,
				Servers: struct {
					Meta []Server `json:"meta"`
					Wg   []Server `json:"wg"`
				}{
					Wg: []Server{{Cn: "wg2", IP: "2.2.2.2"}},
				},
			},
		},
	}

	c := &PIAClient{portForwarding: true}
	servers, err := c.generateWireguardServerList(list)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := servers[Region("r2")]; ok {
		t.Fatalf("expected r2 excluded when portForwarding=true")
	}
	if _, ok := servers[Region("r1")]; !ok {
		t.Fatalf("expected r1 included")
	}
}
