package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dloomes/av-bridge/internal/config"
)

// fakeBravia answers the Sony JSON-RPC calls a poll makes. With v17 false
// it rejects getSystemInformation 1.7 as an older display does.
func fakeBravia(t *testing.T, v17 bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sonyRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		reply := func(result any) { _ = json.NewEncoder(w).Encode(map[string]any{"result": []any{result}, "id": req.ID}) }
		switch req.Method {
		case "getSystemInformation":
			info := map[string]any{"model": "FW-55BZ35L", "serial": "7001234", "generation": "5.7.0", "macAddr": "04:5d:4b:00:00:01", "name": "BRAVIA"}
			if req.Version == "1.7" {
				if !v17 {
					_ = json.NewEncoder(w).Encode(map[string]any{"error": []any{14, "unsupported version"}, "id": req.ID})
					return
				}
				info["fwVersion"] = "PKG6.5603.0175"
				info["androidOs"] = "12"
				info["mode"] = "Pro"
			}
			reply(info)
		case "getPowerStatus":
			reply(map[string]any{"status": "standby"})
		case "getPowerSavingMode":
			reply(map[string]any{"mode": "off"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": []any{}, "id": req.ID})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSonyBraviaFirmware(t *testing.T) {
	for _, c := range []struct {
		name     string
		v17      bool
		firmware any
	}{
		{"display with getSystemInformation 1.7", true, "PKG6.5603.0175"},
		{"older display, 1.0 only", false, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := fakeBravia(t, c.v17)
			a := NewSonyBraviaAdapter(config.DeviceConfig{ID: "bravia", Protocol: "sony_bravia", Address: srv.URL,
				Tags: map[string]string{"psk": "1234"}})
			ctx := context.Background()
			if err := a.Connect(ctx); err != nil {
				t.Fatal(err)
			}
			tel, err := a.Poll(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if tel.Metrics["firmware_version"] != c.firmware {
				t.Errorf("firmware_version = %v, want %v", tel.Metrics["firmware_version"], c.firmware)
			}
			if tel.Metrics["model"] != "FW-55BZ35L" || tel.Metrics["serial_number"] != "7001234" {
				t.Errorf("model/serial: %v", tel.Metrics)
			}
			if c.v17 && (tel.Metrics["pro_mode"] != "Pro" || tel.Metrics["android_os"] != "12") {
				t.Errorf("pro_mode/android_os: %v", tel.Metrics)
			}
		})
	}
}
