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
			// Verbose no-fault indicator entry — the "all clear" response.
			name: "verbose-none",
			raw: `[{
				"id":"INDICATOR_NONE_IN_DEVICE",
				"name":"No fault in device",
				"faults":[],
				"serialNumber":"01842216"
			}]`,
			wantCount: 0,
			wantNil:   true,
		},
		{
			// Verbose with one fault under an indicator entry.
			name: "verbose-single-fault",
			raw: `[{
				"id":"INDICATOR_MINOR_IN_DEVICE",
				"name":"Minor Fault in Device",
				"faults":[{"id":"FAULT_FAN_MALFUNCTION","name":"Cooling fan malfunction"}],
				"serialNumber":"01842216"
			}]`,
			wantCount: 1,
			wantNil:   false,
		},
		{
			// Verbose with multiple faults across two indicator entries.
			name: "verbose-multi-device",
			raw: `[
			  {"id":"IND1","name":"Minor","faults":[{"id":"F1","name":"Foo"}],"serialNumber":"111"},
			  {"id":"IND2","name":"Major","faults":[{"id":"F2","name":"Bar"},{"id":"F3","name":"Baz"}],"serialNumber":"222"}
			]`,
			wantCount: 3,
			wantNil:   false,
		},
		{
			// Non-verbose positional: [indicator_num, description, [[fault_id, desc]], serial]
			name:      "non-verbose-single-fault",
			raw:       `[[2, "Minor Fault in Device", [[89, "Cooling fan malfunction"]], "02196874"]]`,
			wantCount: 1,
			wantNil:   false,
		},
		{
			name:      "non-verbose-multiple-faults",
			raw:       `[[3, "Major Fault", [[89, "Foo"],[101, "Bar"],[102, "Baz"]], "02196874"]]`,
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

func TestParseTesiraDeviceInfo(t *testing.T) {
	cases := []struct {
		name         string
		raw          string
		wantModel    string
		wantFirmware string
		wantSerial   string
		wantIP       string
	}{
		{name: "empty", raw: "", wantModel: "", wantFirmware: "", wantSerial: "", wantIP: ""},
		{name: "not-json", raw: "not-json", wantModel: "", wantFirmware: "", wantSerial: "", wantIP: ""},
		{
			// Canonical shape per DEVICE service reference.
			name:         "canonical",
			raw:          `{"model":"TESIRAFORTE-CI","revision":"A","serial":"05008305","firmware":"3.19.1.7","IP":"192.168.1.100"}`,
			wantModel:    "TESIRAFORTE-CI",
			wantFirmware: "3.19.1.7",
			wantSerial:   "05008305",
			wantIP:       "192.168.1.100",
		},
		{
			// Alternate field names some firmwares emit.
			name:         "alternate-keys",
			raw:          `{"Model":"TESIRAFORTE-VT","softwareVersion":"4.1.0","serialNumber":"12345","ipAddress":"10.0.0.5"}`,
			wantModel:    "TESIRAFORTE-VT",
			wantFirmware: "4.1.0",
			wantSerial:   "12345",
			wantIP:       "10.0.0.5",
		},
		{
			name:         "partial",
			raw:          `{"model":"TESIRAFORTE-CI","firmware":"3.19.1.7"}`,
			wantModel:    "TESIRAFORTE-CI",
			wantFirmware: "3.19.1.7",
			wantSerial:   "",
			wantIP:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, f, s, ip := parseTesiraDeviceInfo(tc.raw)
			if m != tc.wantModel {
				t.Errorf("model: got %q want %q", m, tc.wantModel)
			}
			if f != tc.wantFirmware {
				t.Errorf("firmware: got %q want %q", f, tc.wantFirmware)
			}
			if s != tc.wantSerial {
				t.Errorf("serial: got %q want %q", s, tc.wantSerial)
			}
			if ip != tc.wantIP {
				t.Errorf("ip: got %q want %q", ip, tc.wantIP)
			}
		})
	}
}
