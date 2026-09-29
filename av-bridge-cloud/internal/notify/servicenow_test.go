package notify

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServiceNow is a minimal Table API: create, get-by-correlation and
// patch on /api/now/table/incident, with Basic auth.
type fakeServiceNow struct {
	srv *httptest.Server

	mu        sync.Mutex
	failNext  int // return 503 for the next N requests
	incidents map[string]map[string]any // sys_id → fields
	byCorr    map[string]string         // correlation_id → sys_id
	requests  []*http.Request
	bodies    []map[string]any
	seq       int
}

func newFakeServiceNow(t *testing.T) *fakeServiceNow {
	t.Helper()
	f := &fakeServiceNow{incidents: map[string]map[string]any{}, byCorr: map[string]string{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServiceNow) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
	}
	f.requests = append(f.requests, r)
	f.bodies = append(f.bodies, body)
	w.Header().Set("Content-Type", "application/json")
	if u, p, ok := r.BasicAuth(); !ok || u != "marcus" || p != "s3cret" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"User Not Authenticated","detail":"Required to provide Auth information"},"status":"failure"}`)
		return
	}
	if f.failNext > 0 {
		f.failNext--
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	const base = "/api/now/table/incident"
	switch {
	case r.Method == http.MethodPost && r.URL.Path == base:
		f.seq++
		id := "sys" + string(rune('0'+f.seq))
		num := "INC000100" + string(rune('0'+f.seq))
		body["number"] = num
		f.incidents[id] = body
		if c, _ := body["correlation_id"].(string); c != "" {
			f.byCorr[c] = id
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]string{"sys_id": id, "number": num}})
	case r.Method == http.MethodGet && r.URL.Path == base:
		corr := strings.TrimPrefix(r.URL.Query().Get("sysparm_query"), "correlation_id=")
		res := []map[string]string{}
		if id, ok := f.byCorr[corr]; ok {
			res = append(res, map[string]string{"sys_id": id, "number": f.incidents[id]["number"].(string)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": res})
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, base+"/"):
		id := strings.TrimPrefix(r.URL.Path, base+"/")
		inc, ok := f.incidents[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"No Record found"}}`)
			return
		}
		for k, v := range body {
			inc[k] = v
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]string{"sys_id": id, "state": "6"}})
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (f *fakeServiceNow) last() (*http.Request, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1], f.bodies[len(f.bodies)-1]
}

// identity "cipher" for tests: password_enc is just the hex of the password.
func testSN(f *fakeServiceNow) *ServiceNow {
	s := NewServiceNow(func(b []byte) ([]byte, error) { return b, nil })
	s.httpc = f.srv.Client()
	return s
}

func testChannel(f *fakeServiceNow, extra map[string]any) Channel {
	cfg := map[string]any{"username": "marcus", "password_enc": hex.EncodeToString([]byte("s3cret"))}
	for k, v := range extra {
		cfg[k] = v
	}
	return Channel{ID: "ch1", Type: "servicenow", Target: f.srv.URL, Config: cfg}
}

func testEvent() AlertEvent {
	return AlertEvent{
		AlertID: "11111111-2222-3333-4444-555555555555", CustomerID: "c1",
		DeviceID: "d1", DeviceName: "Court 3 Display", AlertKey: "device_offline",
		Severity: "critical", Message: "Device has stopped responding",
		OpenedAt: time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC),
	}
}

func TestServiceNowOpen(t *testing.T) {
	f := newFakeServiceNow(t)
	s := testSN(f)
	ch := testChannel(f, map[string]any{"assignment_group": "AV Support", "caller": "marcus.integration", "category": "hardware"})
	inc, err := s.Open(context.Background(), ch, testEvent())
	if err != nil {
		t.Fatal(err)
	}
	if inc.SysID != "sys1" || inc.Number != "INC0001001" {
		t.Errorf("incident = %+v", inc)
	}
	r, body := f.last()
	if r.Method != http.MethodPost || r.URL.Query().Get("sysparm_input_display_value") != "true" {
		t.Errorf("request %s %s", r.Method, r.URL)
	}
	want := map[string]string{
		"short_description": "[M.A.R.C.U.S.] Court 3 Display: Device has stopped responding",
		"urgency":           "1", "impact": "2",
		"correlation_id":    "11111111-2222-3333-4444-555555555555",
		"assignment_group":  "AV Support", "caller_id": "marcus.integration", "category": "hardware",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %v, want %q", k, body[k], v)
		}
	}
	if d, _ := body["description"].(string); !strings.Contains(d, "Device: Court 3 Display") || !strings.Contains(d, "resolved automatically") {
		t.Errorf("description = %q", d)
	}
}

func TestServiceNowOpenReusesCorrelatedIncident(t *testing.T) {
	f := newFakeServiceNow(t)
	s := testSN(f)
	ch := testChannel(f, nil)
	first, err := s.Open(context.Background(), ch, testEvent())
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Open(context.Background(), ch, testEvent())
	if err != nil {
		t.Fatal(err)
	}
	if again != first || len(f.incidents) != 1 {
		t.Errorf("retry created a second incident: %+v vs %+v (%d incidents)", again, first, len(f.incidents))
	}
}

func TestServiceNowResolve(t *testing.T) {
	f := newFakeServiceNow(t)
	s := testSN(f)
	ch := testChannel(f, nil)
	inc, _ := s.Open(context.Background(), ch, testEvent())
	if err := s.Resolve(context.Background(), ch, inc.SysID, "The device recovered."); err != nil {
		t.Fatal(err)
	}
	r, body := f.last()
	if r.Method != http.MethodPatch || body["state"] != "6" || body["close_code"] != "Solution provided" || body["close_notes"] != "The device recovered." {
		t.Errorf("%s %v", r.Method, body)
	}
	if r.URL.Query().Has("sysparm_input_display_value") {
		t.Error("numeric state should be sent as a value, not a display value")
	}

	ch.Config["resolved_state"] = "Resolved"
	ch.Config["close_code"] = "Resolved by caller"
	if err := s.Resolve(context.Background(), ch, inc.SysID, "x"); err != nil {
		t.Fatal(err)
	}
	r, body = f.last()
	if body["state"] != "Resolved" || body["close_code"] != "Resolved by caller" || r.URL.Query().Get("sysparm_input_display_value") != "true" {
		t.Errorf("custom resolve: %v %s", body, r.URL.RawQuery)
	}
}

func TestServiceNowErrors(t *testing.T) {
	f := newFakeServiceNow(t)
	s := testSN(f)
	bad := testChannel(f, map[string]any{"password_enc": hex.EncodeToString([]byte("wrong"))})
	_, err := s.Open(context.Background(), bad, AlertEvent{Severity: "warning"})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "User Not Authenticated") {
		t.Errorf("bad credentials: %v", err)
	}
	for _, ch := range []Channel{
		{Target: f.srv.URL, Config: map[string]any{"username": "marcus"}},
		{Target: f.srv.URL, Config: map[string]any{"password_enc": "00"}},
		{Target: "", Config: map[string]any{"username": "u", "password_enc": "00"}},
	} {
		if _, err := s.Open(context.Background(), ch, AlertEvent{}); err == nil {
			t.Errorf("channel %+v: expected an error", ch)
		}
	}
	if err := s.Resolve(context.Background(), testChannel(f, nil), "nope", "x"); err == nil || !strings.Contains(err.Error(), "No Record found") {
		t.Errorf("resolve unknown: %v", err)
	}
}

func TestNormaliseServiceNowURL(t *testing.T) {
	for in, want := range map[string]string{
		"acme":                                 "https://acme.service-now.com",
		"acme.service-now.com":                 "https://acme.service-now.com",
		"https://acme.service-now.com/":        "https://acme.service-now.com",
		"https://acme.service-now.com/now/nav": "https://acme.service-now.com",
		" https://snow.example.gov.uk ":        "https://snow.example.gov.uk",
	} {
		if got, err := NormaliseServiceNowURL(in); err != nil || got != want {
			t.Errorf("%q → %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "http://acme.service-now.com", "https://"} {
		if _, err := NormaliseServiceNowURL(in); err == nil {
			t.Errorf("%q: expected an error", in)
		}
	}
}
