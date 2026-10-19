package portalapi

// Integration test for the Room usage and Device events reports. Runs only
// when TEST_DATABASE_URL points at a disposable Postgres 16 (see
// device_move_integration_test.go for the docker line).

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

func TestRoomUsageAndDeviceEventsReports(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping room report test")
	}
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, dsn, quiet); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	su, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer su.Close(ctx)
	as := func(user, pass string) string {
		u, _ := url.Parse(dsn)
		u.User = url.UserPassword(user, pass)
		return u.String()
	}
	st, err := db.New(ctx, as("app_admin", "app_admin_dev"), as("app_tenant", "app_tenant_dev"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	q := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := su.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := su.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	cust := q(`INSERT INTO customers (name) VALUES ('room-reports') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'rr-' || $1::text, 'rr', '\x00') RETURNING id::text`, cust)
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
	bld := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'HQ') RETURNING id::text`, cust, loc)
	room := func(name string) string {
		return q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, $3) RETURNING id::text`, cust, bld, name)
	}
	device := func(roomID, reported string) string {
		return q(`INSERT INTO devices (customer_id, collector_id, room_id, reported_id, name, protocol)
		          VALUES ($1, $2, $3, $4, $4, 'ping') RETURNING id::text`, cust, col, roomID, reported)
	}
	// reading at <hoursAgo> whole hours back, 15 minutes past.
	reading := func(dev, status string, hoursAgo int, metrics string) {
		exec(`INSERT INTO telemetry (customer_id, device_id, status, metrics, ts)
		      VALUES ($1, $2, $3, $4::jsonb, date_trunc('hour', now()) - make_interval(hours => $5) + interval '15 minutes')`,
			cust, dev, status, metrics, hoursAgo)
	}

	boardroom := room("Boardroom") // codec and display
	huddle := room("Huddle")       // display only
	room("Store")                  // nothing

	codec := device(boardroom, "codec")
	display := device(boardroom, "display")
	reading(codec, "online", 10, `{"active_calls": 1, "people_count": 3}`)
	reading(codec, "online", 9, `{"active_calls": 0, "people_count": 5}`)
	reading(codec, "online", 8, `{"active_calls": 0, "people_count": 0}`)
	reading(codec, "offline", 7, `{"active_calls": 2, "people_count": 9}`) // offline readings don't count
	reading(display, "online", 10, `{"power_status": "active"}`)
	reading(display, "online", 5, `{"power_status": "active"}`) // during a Room Readiness run
	exec(`INSERT INTO nightly_run (customer_id, room_id, phase, status, scheduled_at, started_at, completed_at)
	      VALUES ($1, $2, 'ready', 'succeeded', now(),
	              date_trunc('hour', now()) - interval '5 hours' + interval '10 minutes',
	              date_trunc('hour', now()) - interval '5 hours' + interval '40 minutes')`, cust, boardroom)

	tv := device(huddle, "tv")
	reading(tv, "online", 3, `{"power_status": "on"}`)
	reading(tv, "online", 2, `{"power_status": "on"}`)
	reading(tv, "online", 1, `{"power_status": "standby"}`)
	for i, typ := range []string{"alert:offline", "alert:offline", "alert:fault", "telnet_message", "telnet_message"} {
		exec(`INSERT INTO events (customer_id, device_id, event_type, payload, ts)
		      VALUES ($1, $2, $3, '{}', now() - make_interval(mins => $4))`, cust, tv, typ, 10+i)
	}

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	admin := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000d1", CustomerID: cust, Permissions: all}
	h := New(st, nil, nil, nil, nil, quiet)
	get := func(fn http.HandlerFunc, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), admin))
		rec := httptest.NewRecorder()
		fn(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body)
		}
		return rec
	}

	type usage struct {
		RoomName       string  `json:"room_name"`
		DeviceCount    int     `json:"device_count"`
		HasUsageSignal bool    `json:"has_usage_signal"`
		HoursInUse     int     `json:"hours_in_use"`
		HoursInCalls   int     `json:"hours_in_calls"`
		HoursOccupied  int     `json:"hours_occupied"`
		DaysInUse      int     `json:"days_in_use"`
		AvgHoursPerDay float64 `json:"avg_hours_per_day"`
		PeakPeople     *int    `json:"peak_people"`
		DisplayOnHours int     `json:"display_on_hours"`
	}
	var rows []usage
	_ = json.Unmarshal(get(h.RoomUsageReport, "/r?days=1").Body.Bytes(), &rows)
	if len(rows) != 3 || rows[0].RoomName != "Boardroom" || rows[1].RoomName != "Huddle" || rows[2].RoomName != "Store" {
		t.Fatalf("rows = %+v, want Boardroom, Huddle, Store", rows)
	}
	b := rows[0]
	if !b.HasUsageSignal || b.HoursInUse != 2 || b.HoursInCalls != 1 || b.HoursOccupied != 2 ||
		b.PeakPeople == nil || *b.PeakPeople != 5 || b.DisplayOnHours != 1 || b.DeviceCount != 2 || b.AvgHoursPerDay != 2 {
		t.Errorf("Boardroom = %+v; want 2 h in use (1 in calls, 2 occupied), peak 5, 1 display-on hour (the run's hour left out)", b)
	}
	if hu := rows[1]; hu.HasUsageSignal || hu.HoursInUse != 0 || hu.DisplayOnHours != 2 || hu.PeakPeople != nil {
		t.Errorf("Huddle = %+v; want no usage signal and 2 display-on hours", hu)
	}
	if s := rows[2]; s.HasUsageSignal || s.HoursInUse != 0 || s.DisplayOnHours != 0 || s.DeviceCount != 0 {
		t.Errorf("Store = %+v; want empty", s)
	}
	if csv := get(h.RoomUsageReport, "/r?days=1&format=csv").Body.String(); !strings.HasPrefix(csv, "room_id,room_name,building_name,device_count,has_usage_signal,hours_in_use") ||
		!strings.Contains(csv, ",Boardroom,HQ,2,true,2,1,2,1,2.00,5,1") {
		t.Errorf("usage CSV:\n%s", csv)
	}

	type events struct {
		RoomName     string `json:"room_name"`
		EventCount   int    `json:"event_count"`
		AlertCount   int    `json:"alert_count"`
		MessageCount int    `json:"message_count"`
	}
	var ev []events
	_ = json.Unmarshal(get(h.DeviceEventsReport, "/r?days=1").Body.Bytes(), &ev)
	if len(ev) != 3 || ev[0].RoomName != "Huddle" || ev[0].EventCount != 5 || ev[0].AlertCount != 3 || ev[0].MessageCount != 2 {
		t.Fatalf("device events = %+v; want Huddle first with 5 events (3 alerts, 2 messages)", ev)
	}
	if ev[1].EventCount != 0 || ev[2].EventCount != 0 {
		t.Errorf("rooms without events = %+v", ev[1:])
	}
}
