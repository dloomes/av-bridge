package portalapi

// Integration test: a device's readings keep their mode (Snapshot = "poll")
// through create, read and update. Runs only when TEST_DATABASE_URL points
// at a disposable Postgres 16 (see device_move_integration_test.go for the
// docker line).

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

func TestDeviceSubscriptionModeRoundTrip(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping device subscription test")
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
	cust := q(`INSERT INTO customers (name) VALUES ('tesira-subs') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'ts-' || $1::text, 'ts', '\x00') RETURNING id::text`, cust)

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	admin := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000d3", CustomerID: cust, Permissions: all}
	h := New(st, nil, nil, nil, nil, quiet)
	call := func(method, path, id, body string, fn func(*httptest.ResponseRecorder, *httptest.ResponseRecorder)) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if id != "" {
			req.SetPathValue("id", id)
		}
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), admin))
		rec := httptest.NewRecorder()
		switch method {
		case "POST":
			h.CreateDevice(rec, req)
		case "PATCH":
			h.UpdateDevice(rec, req)
		default:
			h.GetDevice(rec, req)
		}
		if rec.Code >= 300 {
			t.Fatalf("%s %s = %d %s", method, path, rec.Code, rec.Body)
		}
		return rec
	}
	modes := func(id string) map[string]string {
		t.Helper()
		var got struct {
			Subscriptions []Subscription `json:"subscriptions"`
		}
		if err := json.Unmarshal(call("GET", "/api/v1/devices/"+id, id, "", nil).Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, s := range got.Subscriptions {
			out[s.Label] = s.Mode
		}
		return out
	}

	created := call("POST", "/api/v1/devices", "", `{
		"collector_id": "`+col+`", "reported_id": "dsp-1", "name": "DSP", "type": "audio", "protocol": "tesira",
		"address": "10.0.4.30",
		"subscriptions": [
			{"tag": "MicMeter1", "attribute": "level", "channel": 1, "label": "mic1_level", "mode": "poll"},
			{"tag": "Zone1", "attribute": "mute", "channel": 1, "label": "zone1_mute"}
		]}`, nil)
	var c struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(created.Body.Bytes(), &c)
	if m := modes(c.ID); m["mic1_level"] != "poll" || m["zone1_mute"] != "" {
		t.Errorf("after create, modes = %v; want mic1_level poll, zone1_mute live", m)
	}

	call("PATCH", "/api/v1/devices/"+c.ID, c.ID, `{"subscriptions": [
		{"tag": "Zone1", "attribute": "mute", "channel": 1, "label": "zone1_mute", "mode": "poll"}]}`, nil)
	if m := modes(c.ID); m["zone1_mute"] != "poll" {
		t.Errorf("after update, modes = %v; want zone1_mute poll", m)
	}

	// The bridge config is built from the stored JSON, which must carry it.
	if stored := q(`SELECT subscriptions::text FROM devices WHERE id = $1`, c.ID); !strings.Contains(stored, `"mode": "poll"`) && !strings.Contains(stored, `"mode":"poll"`) {
		t.Errorf("stored subscriptions = %s; want mode poll", stored)
	}
}
