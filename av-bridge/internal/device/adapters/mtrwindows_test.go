package adapters

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// Fixed "now" for the parse tests; event times are relative to it.
var mtrNow = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

func mtrAgo(d time.Duration) string {
	return mtrNow.Add(-d).Format("2006-01-02T15:04:05.0000000Z")
}

// mtrMsg builds an event description the way MTR writes them: a JSON object.
func mtrMsg(fields map[string]any) string {
	b, _ := json.Marshal(fields)
	return string(b)
}

func mtrHeartbeatMsg(state, signIn string) string {
	f := map[string]any{
		"Description":     "Heartbeat is " + strings.ToLower(state) + ".",
		"ResourceState":   state,
		"OperationName":   "Heartbeat",
		"OperationResult": "Pass",
		"OS":              "Windows 11",
		"OSVersion":       "10.0.22631.4460",
		"Alias":           "test-room@example.invalid",
		"DisplayName":     "Test Room 01",
		"AppVersion":      "5.2.115.0",
	}
	if signIn != "" {
		f["SignInState"] = signIn
	}
	return mtrMsg(f)
}

func mtrHardwareMsg(state, desc string) string {
	return mtrMsg(map[string]any{
		"Description":     desc,
		"ResourceState":   state,
		"OperationName":   "HardwareCheckEngine",
		"OperationResult": map[string]string{"Healthy": "Pass", "Unhealthy": "Fail"}[state],
		"AppVersion":      "5.2.115.0",
	})
}

const mtrAllHealthy = "Conference Microphone status : Healthy. Conference Speaker status : Healthy. " +
	"Default Speaker status : Healthy. Camera status : Healthy. Front of Room Display status : Healthy."

// mtrOutputDoc is a script output document.
type mtrOutputDoc map[string]any

func (d mtrOutputDoc) json() []byte {
	b, _ := json.Marshal(map[string]any(d))
	return b
}

func mtrBaseDoc() mtrOutputDoc {
	return mtrOutputDoc{
		"schema":    1,
		"event_log": true,
		"events": []map[string]any{
			{"id": 2000, "time": mtrAgo(1 * time.Minute), "message": mtrHeartbeatMsg("Healthy", "SignedIn")},
			{"id": 2000, "time": mtrAgo(3 * time.Minute), "message": mtrHardwareMsg("Healthy", mtrAllHealthy)},
		},
		"errors": []map[string]any{},
		"mic":    map[string]any{"in_use": false, "apps": []string{}},
		"camera": map[string]any{"in_use": false, "apps": []string{}},
		"udp":    map[string]any{"teams_processes": 2, "media_sockets": 0},
		"pc": map[string]any{
			"os_caption": "Microsoft Windows 11 IoT Enterprise", "os_version": "10.0.22631",
			"uptime_s": 86400, "manufacturer": "Example Corp", "model": "Example MTR PC 1",
			"serial": "TESTSERIAL0001",
		},
		"section_errors": map[string]string{},
	}
}

func mtrWith(d mtrOutputDoc, kv ...any) mtrOutputDoc {
	out := mtrOutputDoc{}
	for k, v := range d {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func TestMTRReadings(t *testing.T) {
	inUse := map[string]any{"in_use": true, "apps": []string{"MSTeams_8wekyb3d8bbwe"}}
	idle := map[string]any{"in_use": false, "apps": []string{}}

	tests := []struct {
		name   string
		doc    mtrOutputDoc
		want   map[string]any // keys that must have these values
		absent []string       // keys that must be missing
	}{
		{
			name: "heartbeat healthy and signed in",
			doc:  mtrBaseDoc(),
			want: map[string]any{
				"teams_health": "healthy", "teams_signed_in": true, "teams_app_version": "5.2.115.0",
				"last_heartbeat": "2026-11-23T09:59:00Z",
				"call_state":     "idle", "active_calls": 0,
				"call_signal_mic": false, "call_signal_udp": false, "udp_media_sockets": 0,
				"peripherals_linked": true, "peripherals_missing": []string{},
				"software_version": "10.0.22631", "os_name": "Microsoft Windows 11 IoT Enterprise",
				"uptime_s": int64(86400), "manufacturer": "Example Corp", "model": "Example MTR PC 1",
				"serial_number": "TESTSERIAL0001",
			},
			absent: []string{"last_error"},
		},
		{
			name: "heartbeat unhealthy with a peripheral down",
			doc: mtrWith(mtrBaseDoc(),
				"events", []map[string]any{
					{"id": 2000, "time": mtrAgo(1 * time.Minute), "message": mtrHeartbeatMsg("Unhealthy", "SignedIn")},
					{"id": 2000, "time": mtrAgo(10 * time.Minute), "message": mtrHardwareMsg("Healthy", mtrAllHealthy)},
				},
				"errors", []map[string]any{
					{"id": 3001, "time": mtrAgo(2 * time.Minute), "message": mtrHardwareMsg("Unhealthy",
						"Conference Microphone status : Unhealthy. Conference Microphone Name : Example Mic 1.")},
					{"id": 2001, "time": mtrAgo(5 * time.Minute), "message": mtrHardwareMsg("Unhealthy", "An older error.")},
				}),
			want: map[string]any{
				"teams_health":        "unhealthy",
				"teams_signed_in":     true,
				"peripherals_linked":  false,
				"peripherals_missing": []string{"Conference Microphone"},
				"last_error":          "Conference Microphone status : Unhealthy. Conference Microphone Name : Example Mic 1.",
			},
		},
		{
			name: "in call by mic only",
			doc:  mtrWith(mtrBaseDoc(), "mic", inUse, "camera", idle),
			want: map[string]any{
				"call_state": "in_call", "active_calls": 1,
				"call_signal_mic": true, "call_signal_udp": false, "udp_media_sockets": 0,
			},
		},
		{
			// Idle Teams can hold media ports: an idle mic/camera wins.
			name: "UDP open but mic and camera idle is idle",
			doc:  mtrWith(mtrBaseDoc(), "udp", map[string]any{"teams_processes": 3, "media_sockets": 6}),
			want: map[string]any{
				"call_state": "idle", "active_calls": 0,
				"call_signal_mic": false, "call_signal_udp": true, "udp_media_sockets": 6,
			},
		},
		{
			// UDP alone doesn't decide: idle Teams can hold media ports.
			name: "unknown when mic and camera can't be read, even with UDP open",
			doc: mtrWith(mtrWith(mtrWith(mtrBaseDoc(), "udp", map[string]any{"teams_processes": 3, "media_sockets": 6}),
				"mic", nil), "camera", nil),
			want: map[string]any{
				"call_state": "unknown", "active_calls": 0,
				"call_signal_udp": true, "udp_media_sockets": 6,
			},
			absent: []string{"call_signal_mic"},
		},
		{
			name: "both signals false",
			doc:  mtrBaseDoc(),
			want: map[string]any{
				"call_state": "idle", "active_calls": 0,
				"call_signal_mic": false, "call_signal_udp": false,
			},
		},
		{
			name: "event log missing",
			doc: mtrWith(mtrBaseDoc(), "event_log", false, "events", nil, "errors", nil,
				"section_errors", map[string]string{"event_log": `There is not an event log on the localhost computer that matches "Skype Room System".`}),
			want: map[string]any{
				"call_state": "idle", "call_signal_mic": false, "call_signal_udp": false,
				"software_version": "10.0.22631", "serial_number": "TESTSERIAL0001",
			},
			absent: []string{"teams_health", "teams_signed_in", "teams_app_version", "last_heartbeat",
				"last_error", "peripherals", "peripherals_missing", "peripherals_linked"},
		},
		{
			name: "registry unreadable",
			doc: mtrWith(mtrBaseDoc(), "mic", nil, "camera", nil,
				"section_errors", map[string]string{"mic": "Requested registry access is not allowed.", "camera": "Requested registry access is not allowed."}),
			want: map[string]any{
				"call_state": "unknown", "active_calls": 0, "call_signal_udp": false,
				"teams_health": "healthy",
			},
			absent: []string{"call_signal_mic"},
		},
		{
			name: "registry unreadable and UDP in use",
			doc: mtrWith(mtrBaseDoc(), "mic", nil, "camera", nil,
				"udp", map[string]any{"teams_processes": 2, "media_sockets": 4}),
			want:   map[string]any{"call_state": "unknown", "active_calls": 0, "call_signal_udp": true, "udp_media_sockets": 4},
			absent: []string{"call_signal_mic"},
		},
		{
			name:   "no call signal readable",
			doc:    mtrWith(mtrBaseDoc(), "mic", nil, "camera", nil, "udp", nil),
			want:   map[string]any{"call_state": "unknown", "active_calls": 0},
			absent: []string{"call_signal_mic", "call_signal_udp", "udp_media_sockets"},
		},
		{
			name: "camera in use counts as the mic signal",
			doc:  mtrWith(mtrBaseDoc(), "camera", inUse),
			want: map[string]any{"call_state": "in_call", "call_signal_mic": true},
		},
		{
			name: "signed out, error older than 15 minutes ignored",
			doc: mtrWith(mtrBaseDoc(),
				"events", []map[string]any{
					{"id": 2000, "time": mtrAgo(1 * time.Minute), "message": mtrHeartbeatMsg("Healthy", "SignedOut")},
				},
				"errors", []map[string]any{
					{"id": 2001, "time": mtrAgo(20 * time.Minute), "message": mtrHardwareMsg("Unhealthy", "Stale error.")},
				}),
			want:   map[string]any{"teams_signed_in": false},
			absent: []string{"last_error", "peripherals"},
		},
		{
			name: "heartbeat without sign-in field",
			doc: mtrWith(mtrBaseDoc(), "events", []map[string]any{
				{"id": 2000, "time": mtrAgo(1 * time.Minute), "message": mtrHeartbeatMsg("Healthy", "")},
			}),
			want:   map[string]any{"teams_health": "healthy"},
			absent: []string{"teams_signed_in"},
		},
		{
			name: "everything unreadable",
			doc: mtrOutputDoc{"schema": 1, "event_log": nil, "events": nil, "errors": nil,
				"mic": nil, "camera": nil, "udp": nil, "pc": nil},
			want: map[string]any{"call_state": "unknown", "active_calls": 0},
			absent: []string{"teams_health", "software_version", "serial_number", "uptime_s",
				"call_signal_mic", "call_signal_udp"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := mtrReadings(tc.doc.json(), mtrNow)
			if err != nil {
				t.Fatal(err)
			}
			for k, want := range tc.want {
				v, ok := got[k]
				if !ok {
					t.Errorf("%s missing; readings %v", k, got)
					continue
				}
				if !reflect.DeepEqual(v, want) {
					t.Errorf("%s = %#v, want %#v", k, v, want)
				}
			}
			for _, k := range tc.absent {
				if v, ok := got[k]; ok {
					t.Errorf("%s = %#v, want absent", k, v)
				}
			}
		})
	}
}

func TestMTRReadingsPeripherals(t *testing.T) {
	got, _, err := mtrReadings(mtrWith(mtrBaseDoc(), "errors", []map[string]any{
		{"id": 3001, "time": mtrAgo(2 * time.Minute), "message": mtrHardwareMsg("Unhealthy", "Camera status : Unhealthy.")},
	}).json(), mtrNow)
	if err != nil {
		t.Fatal(err)
	}
	ps, _ := got["peripherals"].([]map[string]any)
	byName := map[string]map[string]any{}
	for _, p := range ps {
		byName[p["name"].(string)] = p
	}
	for _, name := range []string{"Camera", "Conference Microphone", "Conference Speaker", "Default Speaker", "Front of Room Display"} {
		if byName[name] == nil {
			t.Fatalf("peripheral %q missing: %v", name, ps)
		}
	}
	cam := byName["Camera"]
	if cam["connected"] != false || cam["model"] != "Camera" {
		t.Errorf("camera = %v, want disconnected, model Camera", cam)
	}
	// Unhealthy now; last_seen still shows when it was last healthy.
	if cam["last_seen"] != "2026-11-23T09:57:00Z" {
		t.Errorf("camera last_seen = %v", cam["last_seen"])
	}
	mic := byName["Conference Microphone"]
	if mic["connected"] != true || mic["model"] != "Microphone" || mic["last_seen"] != "2026-11-23T09:57:00Z" {
		t.Errorf("mic = %v", mic)
	}
	if byName["Front of Room Display"]["model"] != "Display" || byName["Default Speaker"]["model"] != "Speaker" {
		t.Errorf("kinds wrong: %v", ps)
	}
	if !reflect.DeepEqual(got["peripherals_missing"], []string{"Camera"}) || got["peripherals_linked"] != false {
		t.Errorf("missing = %v linked = %v", got["peripherals_missing"], got["peripherals_linked"])
	}
}

func TestMTRReadingsUnreadableOutput(t *testing.T) {
	if _, _, err := mtrReadings([]byte("powershell : blocked by policy"), mtrNow); err == nil {
		t.Fatal("want an error for output with no JSON")
	}
	// A BOM and a stray line before the object are tolerated.
	raw := append([]byte("\xef\xbb\xbfWARNING: something\r\n"), mtrBaseDoc().json()...)
	got, _, err := mtrReadings(raw, mtrNow)
	if err != nil || got["teams_health"] != "healthy" {
		t.Fatalf("got %v, %v", got, err)
	}
}

// ── Command line ──────────────────────────────────────────────────────────

func TestMTRCommandLine(t *testing.T) {
	line, err := mtrCommandLine(mtrScript)
	if err != nil {
		t.Fatal(err)
	}
	if len(line) > mtrMaxCommandLine {
		t.Fatalf("command line %d chars, over %d", len(line), mtrMaxCommandLine)
	}
	if !strings.HasPrefix(line, `powershell.exe -NoLogo -NoProfile -NonInteractive -Command "`) || !strings.HasSuffix(line, `"`) {
		t.Fatalf("unexpected wrapper: %.120s", line)
	}
	if strings.Count(line, `"`) != 2 {
		t.Error("the bootstrap must not contain double quotes (cmd.exe quoting)")
	}
	m := regexp.MustCompile(`FromBase64String\('([A-Za-z0-9+/=]+)'\)`).FindStringSubmatch(line)
	if m == nil {
		t.Fatal("no base64 payload")
	}
	gz, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	back, _ := io.ReadAll(zr)
	if string(back) != mtrScript {
		t.Error("payload does not decode back to the script")
	}
	t.Logf("script %d bytes, command line %d chars", len(mtrScript), len(line))
}

// The script must only read. A cheap guard against a write creeping in.
func TestMTRScriptIsReadOnly(t *testing.T) {
	code := regexp.MustCompile(`(?m)#.*$`).ReplaceAllString(mtrScript, "")
	bad := regexp.MustCompile(`(?i)\b(Set|New|Remove|Clear|Rename|Move|Copy|Restart|Stop|Start|Enable|Disable|Register|Unregister|Invoke|Add|Install|Uninstall|Write-EventLog|Limit)-[A-Za-z]+|Out-File|Export-|reg(\.exe)?\s+(add|delete)|wevtutil|shutdown|\.Set[A-Z]\w*\(|\.Delete\w*\(|\.Create\w*\(|Invoke-CimMethod`)
	for _, m := range bad.FindAllString(code, -1) {
		if strings.EqualFold(m, "New-Object") {
			continue
		}
		t.Errorf("script contains %q; it must stay read-only", m)
	}
}

// ── Adapter ───────────────────────────────────────────────────────────────

func mtrConfig(addr string, tags map[string]string) config.DeviceConfig {
	return config.DeviceConfig{
		ID: "mtr-test-01", Name: "Test Teams Room", Type: "teams_room", Protocol: "mtr_windows",
		Address: addr, Username: "Admin", Password: "not-a-real-password", Tags: tags,
	}
}

func TestMTRFactory(t *testing.T) {
	d, err := New(mtrConfig("192.0.2.10", nil), Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.(*MTRWindowsAdapter); !ok {
		t.Fatalf("New returned %T", d)
	}
}

func TestMTRPollWithFakeRunner(t *testing.T) {
	a := NewMTRWindowsAdapter(mtrConfig("192.0.2.10", nil))
	var gotCmd string
	a.run = func(_ context.Context, cmd string) (string, string, int, error) {
		gotCmd = cmd
		return string(mtrWith(mtrBaseDoc(), "udp", map[string]any{"teams_processes": 2, "media_sockets": 5}).json()), "", 0, nil
	}
	tel, err := a.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotCmd, "powershell.exe ") {
		t.Errorf("ran %.40q", gotCmd)
	}
	if tel.Status != device.StatusOnline || tel.Error != "" {
		t.Fatalf("status %s error %q", tel.Status, tel.Error)
	}
	if tel.Metrics["call_state"] != "idle" || tel.Metrics["serial_number"] != "TESTSERIAL0001" {
		t.Errorf("metrics %v", tel.Metrics)
	}
	if _, ok := tel.Metrics["response_ms"]; !ok {
		t.Error("no response_ms")
	}
}

func TestMTRPollScriptOutputUnreadable(t *testing.T) {
	a := NewMTRWindowsAdapter(mtrConfig("192.0.2.10", nil))
	a.run = func(context.Context, string) (string, string, int, error) {
		return "", "#< CLIXML something went wrong", 1, nil
	}
	tel, _ := a.Poll(context.Background())
	if tel.Status != device.StatusOnline || tel.Error == "" || tel.Metrics["call_state"] != "unknown" {
		t.Fatalf("status %s error %q metrics %v", tel.Status, tel.Error, tel.Metrics)
	}
}

func TestMTRPollErrors(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantErr string
	}{
		{"auth sentinel", errWinRMAuth, "WinRM sign-in rejected"},
		{"auth flattened by the library", fmt.Errorf("unknown error %v", errWinRMAuth), "WinRM sign-in rejected"},
		{"refused", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}, "WinRM unreachable"},
		{"timeout", context.DeadlineExceeded, "WinRM timed out"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := NewMTRWindowsAdapter(mtrConfig("192.0.2.10", nil))
			a.run = func(context.Context, string) (string, string, int, error) { return "", "", 1, tc.err }
			tel, err := a.Poll(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if tel.Status != device.StatusOffline || !strings.HasPrefix(tel.Error, tc.wantErr) {
				t.Errorf("status %s error %q, want offline %q", tel.Status, tel.Error, tc.wantErr)
			}
			if a.Status() != device.StatusOffline {
				t.Error("adapter status not offline")
			}
		})
	}
}

// The real WinRM path against a server that refuses the sign-in.
func TestMTRWinRMSignInRejected(t *testing.T) {
	for _, offer := range []string{"Negotiate", `Basic realm="WSMAN"`} {
		t.Run(offer, func(t *testing.T) {
			var sawBasic bool
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
					sawBasic = true
				}
				w.Header().Set("WWW-Authenticate", offer)
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer srv.Close()
			a := NewMTRWindowsAdapter(mtrConfig(srv.Listener.Addr().String(), map[string]string{"tls_skip_verify": "true"}))
			tel, _ := a.Poll(context.Background())
			if tel.Status != device.StatusOffline || !strings.HasPrefix(tel.Error, "WinRM sign-in rejected — retrying at ") {
				t.Fatalf("status %s error %q", tel.Status, tel.Error)
			}
			if sawBasic {
				t.Error("the password was sent with Basic auth")
			}
			if err := a.Connect(context.Background()); err == nil || !strings.Contains(err.Error(), "WinRM sign-in rejected") {
				t.Errorf("Connect error %v", err)
			}
		})
	}
}

func TestMTRWinRMUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listening now: connection refused
	a := NewMTRWindowsAdapter(mtrConfig(addr, nil))
	tel, _ := a.Poll(context.Background())
	if tel.Status != device.StatusOffline || !strings.HasPrefix(tel.Error, "WinRM unreachable") {
		t.Fatalf("status %s error %q", tel.Status, tel.Error)
	}
}

func TestMTRWinRMSelfSignedRefusedWithoutSkipVerify(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	a := NewMTRWindowsAdapter(mtrConfig(srv.Listener.Addr().String(), nil))
	tel, _ := a.Poll(context.Background())
	if tel.Status != device.StatusOffline || !strings.Contains(tel.Error, "certificate") {
		t.Fatalf("status %s error %q, want a certificate error", tel.Status, tel.Error)
	}
}

func TestMTRMissingCredentials(t *testing.T) {
	cfg := mtrConfig("192.0.2.10", nil)
	cfg.Password = ""
	a := NewMTRWindowsAdapter(cfg)
	if err := a.Connect(context.Background()); err == nil || !strings.Contains(err.Error(), "mtr_windows settings: username and password") {
		t.Fatalf("Connect error %v", err)
	}
}

func TestMTREndpoint(t *testing.T) {
	tests := []struct {
		addr  string
		tags  map[string]string
		host  string
		port  int
		https bool
	}{
		{"192.0.2.10", nil, "192.0.2.10", 5986, true},
		{"mtr-test-01.example.invalid", nil, "mtr-test-01.example.invalid", 5986, true},
		{"192.0.2.10:15986", nil, "192.0.2.10", 15986, true},
		{"https://192.0.2.10:5986/", nil, "192.0.2.10", 5986, true},
		{"192.0.2.10", map[string]string{"port": "6000"}, "192.0.2.10", 6000, true},
		{"192.0.2.10", map[string]string{"winrm_https": "false"}, "192.0.2.10", 5985, false},
		{"192.0.2.10:5999", map[string]string{"port": "6001"}, "192.0.2.10", 6001, true},
		{"[fe80::1]", nil, "fe80::1", 5986, true},
		{"[fe80::1]:5999", nil, "fe80::1", 5999, true},
		{"https://[2001:db8::10]/", nil, "2001:db8::10", 5986, true},
	}
	for _, tc := range tests {
		host, port, https, err := mtrEndpoint(mtrConfig(tc.addr, tc.tags))
		if err != nil || host != tc.host || port != tc.port || https != tc.https {
			t.Errorf("%s %v: got %s %d %v %v", tc.addr, tc.tags, host, port, https, err)
		}
	}
	if _, _, _, err := mtrEndpoint(mtrConfig("192.0.2.10", map[string]string{"port": "nope"})); err == nil {
		t.Error("want an error for a bad port tag")
	}
}

// After one refused sign-in, no further sign-in is attempted (Connect or
// Poll) for 30 minutes, so the PC's local account isn't locked out.
func TestMTRAuthHoldOff(t *testing.T) {
	a := NewMTRWindowsAdapter(mtrConfig("192.0.2.10", nil))
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.Local)
	a.now = func() time.Time { return now }
	attempts := 0
	authFails := true
	a.run = func(context.Context, string) (string, string, int, error) {
		attempts++
		if authFails {
			return "", "", 1, errWinRMAuth
		}
		return string(mtrBaseDoc().json()), "", 0, nil
	}
	wantErr := "WinRM sign-in rejected — retrying at 10:30"

	tel, _ := a.Poll(context.Background())
	if tel.Status != device.StatusOffline || tel.Error != wantErr || attempts != 1 {
		t.Fatalf("first poll: status %s error %q attempts %d", tel.Status, tel.Error, attempts)
	}
	for i := 0; i < 5; i++ {
		now = now.Add(5 * time.Minute) // up to 10:25
		if err := a.Connect(context.Background()); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("Connect during hold-off: %v", err)
		}
		tel, _ = a.Poll(context.Background())
		if tel.Status != device.StatusOffline || tel.Error != wantErr {
			t.Fatalf("Poll during hold-off: status %s error %q", tel.Status, tel.Error)
		}
	}
	if attempts != 1 {
		t.Fatalf("%d sign-in attempts during the hold-off, want none after the first", attempts)
	}

	// Hold-off over: one more attempt; the password is now right.
	now = now.Add(5 * time.Minute) // 10:30
	authFails = false
	if err := a.Connect(context.Background()); err != nil {
		t.Fatalf("Connect after hold-off: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts %d, want 2", attempts)
	}
	tel, _ = a.Poll(context.Background())
	if tel.Status != device.StatusOnline || tel.Error != "" {
		t.Fatalf("poll after hold-off: status %s error %q", tel.Status, tel.Error)
	}

	// A restarted adapter (config edit) tries at once.
	b := NewMTRWindowsAdapter(mtrConfig("192.0.2.10", nil))
	b.run = a.run
	if err := b.Connect(context.Background()); err != nil {
		t.Fatalf("fresh adapter: %v", err)
	}
}

func TestMTRNoCommands(t *testing.T) {
	a := NewMTRWindowsAdapter(mtrConfig("192.0.2.10", nil))
	if _, err := a.SendCommand(context.Background(), device.CommandRequest{Name: "reboot"}); err == nil || !strings.Contains(err.Error(), "unsupported command") {
		t.Fatalf("SendCommand error %v", err)
	}
	caps := a.Capabilities()
	if len(caps.Commands) != 0 || caps.Power.On || caps.Power.Off {
		t.Errorf("capabilities %+v, want no commands or power", caps)
	}
}
