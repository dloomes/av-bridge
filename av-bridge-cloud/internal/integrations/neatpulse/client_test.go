package neatpulse

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// All names, serials and ids below are fake.
const fakeKey = "fake-pulse-key-0123456789"

const endpointsJSON = `{"endpoints":[
 {"id":"ep-bar-1","connected":true,"inByod":false,"inCallStatus":"ZOOM","model":"Neat Bar Pro","serial":"FAKE-BAR-0001","roomId":11,"roomName":"Test Room A","locationId":2,"locationName":"Test Site","regionId":1,"regionName":"Test Region"},
 {"id":"ep-pad-1","connected":false,"inCallStatus":"NONE","model":"Neat Pad","serial":"FAKE-PAD-0001","roomId":11,"roomName":"Test Room A"}
]}`

const endpointJSON = `{"id":"ep-bar-1","connected":true,"connectionTime":"2026-11-20T08:00:00Z","restartTime":"","model":"Neat Bar Pro",
 "serial":"FAKE-BAR-0001","firmwareVersion":"NFF1.20261001.0001","latestVersion":"NFF1.20261101.0002","upgradeStatus":"IDLE",
 "inCallStatus":"TEAMS","primaryMode":"MICROSOFT_TEAMS_ROOMS","pairingSerial":"FAKE-PAD-0001",
 "networks":[{"macAddress":"AABeAFMB","type":"NETWORK_TYPE_ETHERNET","linkUp":true}],"roomId":11,"roomName":"Test Room A"}`

const sensorJSON = `{"data":[{"id":"ep-bar-1","endpointData":{"shutterClosed":false,"data":[
 {"co2":510.5,"humidity":40.2,"illumination":220,"people":2,"temp":21.5,"voc":80,"vocIndex":100,"timestamp":"1763900000"},
 {"co2":null,"humidity":41,"illumination":null,"people":"4","temp":21.7,"voc":null,"vocIndex":null,"timestamp":"1763900060000"}
]}},{"id":"ep-other"}]}`

func newServer(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(fakeKey, WithBaseURL(srv.URL))
}

func TestListEndpointDetailsAndSensors(t *testing.T) {
	var auth []string
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v1/orgs/org-fake/endpoints":
			_, _ = w.Write([]byte(endpointsJSON))
		case "/v1/orgs/org-fake/endpoints/ep-bar-1":
			_, _ = w.Write([]byte(endpointJSON))
		case "/v1/orgs/org-fake/endpoints/sensor":
			_, _ = w.Write([]byte(sensorJSON))
		default:
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	eps, err := c.ListEndpoints(ctx, "org-fake")
	if err != nil || len(eps) != 2 || eps[0].Serial != "FAKE-BAR-0001" || eps[0].RoomName != "Test Room A" || !eps[0].Connected || eps[1].Model != "Neat Pad" {
		t.Fatalf("endpoints %+v %v", eps, err)
	}
	ep, err := c.Endpoint(ctx, "org-fake", "ep-bar-1")
	if err != nil {
		t.Fatal(err)
	}
	if ep.PairingSerial != "FAKE-PAD-0001" || ep.FirmwareVersion != "NFF1.20261001.0001" || ep.LatestVersion == "" ||
		ep.ConnectionTime.IsZero() || !ep.RestartTime.IsZero() || RoomType(ep.PrimaryMode) != "teams_room" {
		t.Fatalf("endpoint %+v", ep)
	}
	if got := ep.MAC(); got != "00:00:5e:00:53:01" {
		t.Fatalf("mac %q", got)
	}
	sens, err := c.SensorData(ctx, "org-fake")
	if err != nil || len(sens) != 2 {
		t.Fatalf("sensors %+v %v", sens, err)
	}
	s, ok := sens[0].Latest()
	if !ok || s.People == nil || *s.People != 4 || s.CO2 != nil || s.Temp == nil || *s.Temp != 21.7 || s.Timestamp.Year() != 2025 {
		t.Fatalf("latest %+v ok=%v", s, ok)
	}
	if sens[0].ShutterClosed == nil || *sens[0].ShutterClosed {
		t.Fatal("shutter")
	}
	if _, ok := sens[1].Latest(); ok {
		t.Fatal("endpoint without data has no latest sample")
	}
	if _, err := c.Endpoint(ctx, "org-fake", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("404 = %v", err)
	}
	for _, a := range auth {
		if a != "Bearer "+fakeKey {
			t.Fatalf("auth header %q", a)
		}
	}
}

func TestFormatMAC(t *testing.T) {
	cases := map[string]string{
		"AAECAwQF":          "00:01:02:03:04:05",
		"AA:BB:CC:00:11:22": "aa:bb:cc:00:11:22",
		"":                  "",
		"not-a-mac":         "not-a-mac",
	}
	for in, want := range cases {
		if got := FormatMAC(in); got != want {
			t.Errorf("FormatMAC(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestErrorsNeverCarryTheKey(t *testing.T) {
	for _, c := range []struct {
		status int
		want   error
	}{{401, ErrUnauthorized}, {403, ErrUnauthorized}, {404, ErrNotFound}} {
		cl := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(`{"message":"echo ` + r.Header.Get("Authorization") + `","status":1}`))
		})
		_, err := cl.ListEndpoints(context.Background(), "org-fake")
		if !errors.Is(err, c.want) || strings.Contains(err.Error(), fakeKey) {
			t.Fatalf("%d: %v", c.status, err)
		}
	}
	// A 5xx error echoing the header still must not leak the key: the body
	// is Pulse's, so check the message we build.
	cl := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"message":"internal","status":500}`))
	})
	if _, err := cl.ListEndpoints(context.Background(), "org-fake"); err == nil || strings.Contains(err.Error(), fakeKey) || !strings.Contains(err.Error(), "internal") {
		t.Fatalf("500: %v", err)
	}
}

func TestRateLimitHonoursRetryAfter(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(429)
	})
	_, err := c.SensorData(context.Background(), "org-fake")
	var rl *RateLimitError
	if !errors.As(err, &rl) || !errors.Is(err, ErrRateLimited) || rl.RetryAfter != 42*time.Second {
		t.Fatalf("%v", err)
	}
	if d := parseRetryAfter(time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)); d < 80*time.Second || d > 91*time.Second {
		t.Fatalf("http-date retry after %v", d)
	}
	// Capped at 5 minutes, however long Pulse asks for.
	if d := parseRetryAfter("3600"); d != MaxRetryAfter {
		t.Fatalf("3600s retry after = %v, want %v", d, MaxRetryAfter)
	}
	if d := parseRetryAfter(time.Now().Add(2 * time.Hour).UTC().Format(http.TimeFormat)); d != MaxRetryAfter {
		t.Fatalf("far http-date retry after = %v, want %v", d, MaxRetryAfter)
	}
}

func TestTimeout(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.ListEndpoints(ctx, "org-fake"); err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

func TestMappings(t *testing.T) {
	for in, want := range map[string]string{"NONE": "", "": "", "ZOOM": "zoom", "TEAMS": "teams", "MEET_CONTENT": "meet", "OPEN_BYOD": "byod", "OTHER": "other", "NEW": "other"} {
		if got := CallPlatform(in); got != want {
			t.Errorf("CallPlatform(%q) = %q", in, got)
		}
	}
	for in, want := range map[string]string{"ZOOM_ROOMS": "zoom_room", "zoom": "zoom_room", "MICROSOFT_TEAMS_ROOMS": "teams_room", "Teams": "teams_room", "": "", "GOOGLE_MEET": ""} {
		if got := RoomType(in); got != want {
			t.Errorf("RoomType(%q) = %q", in, got)
		}
	}
	if !IsCompanionModel("Neat Pad") || IsCompanionModel("Neat Bar 2") {
		t.Fatal("companion model")
	}
}
