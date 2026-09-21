package adapters

import "testing"

func TestParseTesiraNetworkStatus(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantIP  string
		wantMAC string
	}{
		{
			name:    "empty",
			raw:     "",
			wantIP:  "",
			wantMAC: "",
		},
		{
			name:    "non-json",
			raw:     "not-json",
			wantIP:  "",
			wantMAC: "",
		},
		{
			// Shape observed on 3.x firmwares — MAC + addresses siblings under interface.
			name: "interfaces-addresses-siblings",
			raw: `{"interfaces":[{
				"interfaceName":"Control",
				"macAddress":"AA:BB:CC:DD:EE:FF",
				"addresses":[{"address":"192.168.1.100","netmask":"255.255.255.0"}]
			}]}`,
			wantIP:  "192.168.1.100",
			wantMAC: "AA:BB:CC:DD:EE:FF",
		},
		{
			// Shape observed on newer firmwares — MAC embedded in each address entry.
			name: "address-embedded-mac",
			raw: `{"interfaces":[{
				"interfaceName":"Control",
				"addresses":[{"address":"10.0.0.42","macAddress":"11:22:33:44:55:66"}]
			}]}`,
			wantIP:  "10.0.0.42",
			wantMAC: "11:22:33:44:55:66",
		},
		{
			// Gateway is a valid IP string too; make sure we don't accidentally
			// pick it up as the primary address.
			name: "gateway-first-but-address-elsewhere",
			raw: `{"interfaces":[{
				"gatewayAddress":"192.168.1.1",
				"addresses":[{"address":"192.168.1.100","macAddress":"AA:BB:CC:DD:EE:FF"}]
			}]}`,
			wantIP:  "192.168.1.100",
			wantMAC: "AA:BB:CC:DD:EE:FF",
		},
		{
			// Zero-value 0.0.0.0 should not be selected as the primary IP.
			name: "zero-ip-filtered",
			raw: `{"interfaces":[{
				"addresses":[{"address":"0.0.0.0"},{"address":"192.168.1.100"}]
			}]}`,
			wantIP:  "192.168.1.100",
			wantMAC: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip, mac := parseTesiraNetworkStatus(tc.raw)
			if ip != tc.wantIP {
				t.Errorf("ip: got %q want %q", ip, tc.wantIP)
			}
			if mac != tc.wantMAC {
				t.Errorf("mac: got %q want %q", mac, tc.wantMAC)
			}
		})
	}
}

func TestParseTesiraFaultList(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantCount int
		wantNil   bool
	}{
		{name: "empty-string", raw: "", wantCount: 0, wantNil: true},
		{name: "empty-array", raw: "[]", wantCount: 0, wantNil: true},
		{name: "not-json", raw: "garbage", wantCount: 0, wantNil: true},
		{name: "not-an-array", raw: `{"unexpected":true}`, wantCount: 0, wantNil: true},
		{
			name: "single-fault",
			raw: `[{
				"id":"Fault_NetworkLossRedundancy",
				"name":"Network Redundancy Lost",
				"indicator":"Minor"
			}]`,
			wantCount: 1,
			wantNil:   false,
		},
		{
			name: "multiple-faults",
			raw: `[
				{"id":"F1","name":"Foo","indicator":"Minor"},
				{"id":"F2","name":"Bar","indicator":"Major"},
				{"id":"F3","name":"Baz","indicator":"Critical"}
			]`,
			wantCount: 3,
			wantNil:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			count, faults := parseTesiraFaultList(tc.raw)
			if count != tc.wantCount {
				t.Errorf("count: got %d want %d", count, tc.wantCount)
			}
			if tc.wantNil && faults != nil {
				t.Errorf("faults: expected nil, got %v", faults)
			}
			if !tc.wantNil && faults == nil {
				t.Errorf("faults: expected non-nil, got nil")
			}
		})
	}
}
