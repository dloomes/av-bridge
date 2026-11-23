package ingest

// Integration test: Teams Rooms a Collector reads directly (mtr_windows) are
// alerted on by the integrations Alerter from their telemetry, and the
// Collector's own device_offline event never opens a second offline alert
// for them. Same TEST_DATABASE_URL harness as resilience_integration_test.go.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/integrations"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
)

func TestCollectorTeamsRoomAlerts(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping Collector Teams Room alert integration test")
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
	cipher, _ := secrets.NewAESGCMFromHexKey(strings.Repeat("cd", 32))

	q := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := su.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}

	const secret = "fake-mtr-secret"
	enc, _ := cipher.Encrypt([]byte(secret))
	cust := q(`INSERT INTO customers (name) VALUES ('mtr-alerts') RETURNING id::text`)
	bridgeID := "mtr-" + cust
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc, last_seen_at)
	          VALUES ($1, $2, 'fake collector', $3, now()) RETURNING id::text`, cust, bridgeID, enc)
	mtr := q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, type, protocol, platform, address)
	          VALUES ($1, $2, 'fake-mtr', 'Fake MTR', 'teams_room', 'mtr_windows', 'windows', '192.0.2.10') RETURNING id::text`, cust, col)
	disp := q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, type, protocol, address)
	           VALUES ($1, $2, 'fake-disp', 'Fake Display', 'display', 'ping', '192.0.2.11') RETURNING id::text`, cust, col)

	// A fixed daytime clock (outside the 01:00-05:00 hold) the test moves on.
	var mu sync.Mutex
	clock := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	advance := func(d time.Duration) {
		mu.Lock()
		clock = clock.Add(d)
		mu.Unlock()
	}
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	ing := NewHandler(st, cipher, nil, nil, quiet).
		SetTeamsRoomAlerter(integrations.NewAlerter(st.AdminPool(), nil, now, quiet))

	push := func(tel []map[string]any, events []map[string]any) {
		t.Helper()
		ts := time.Now().UTC().Format(time.RFC3339Nano)
		for _, m := range tel {
			m["timestamp"] = ts
		}
		for _, e := range events {
			e["timestamp"] = ts
		}
		b, _ := json.Marshal(map[string]any{"collector_id": bridgeID, "telemetry": tel, "events": events})
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(b)
		req := httptest.NewRequest("POST", "/ingest", bytes.NewReader(b))
		req.Header.Set("X-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		rec := httptest.NewRecorder()
		ing.ServeHTTP(rec, req)
		ing.WaitTeamsRoomAlerts() // evaluation runs after the reply
		if rec.Code != 200 {
			t.Fatalf("ingest: %d %s", rec.Code, rec.Body.String())
		}
	}
	room := func(status string, metrics map[string]any) map[string]any {
		return map[string]any{"device_id": "fake-mtr", "device_type": "teams_room", "protocol": "mtr_windows",
			"status": status, "metrics": metrics}
	}
	peripherals := []map[string]any{
		{"name": "Fake Camera", "model": "Camera", "connected": true},
		{"name": "Fake Speaker", "model": "Speaker", "connected": true},
	}
	signedOut := map[string]any{"teams_signed_in": false, "teams_health": "healthy", "call_state": "idle",
		"peripherals": peripherals, "peripherals_missing": []string{}, "peripherals_linked": true}
	healthy := map[string]any{"teams_signed_in": true, "teams_health": "healthy", "call_state": "idle",
		"peripherals": peripherals, "peripherals_missing": []string{}, "peripherals_linked": true}
	count := func(dev, key, status string) string {
		return q(`SELECT count(*)::text FROM alerts WHERE device_id = $1 AND alert_key = $2 AND status = $3`, dev, key, status)
	}

	// Signed out: nothing until it has held for 10 minutes, then one alert.
	push([]map[string]any{room("online", signedOut)}, nil)
	advance(5 * time.Minute)
	push([]map[string]any{room("online", signedOut)}, nil)
	if n := count(mtr, "teams_signed_out", "open"); n != "0" {
		t.Fatalf("signed-out alert opened after 5 min: %s", n)
	}
	advance(6 * time.Minute)
	push([]map[string]any{room("online", signedOut)}, nil)
	advance(time.Minute)
	push([]map[string]any{room("online", signedOut)}, nil)
	if n := count(mtr, "teams_signed_out", "open"); n != "1" {
		t.Fatalf("open teams_signed_out alerts after 12 min = %s, want 1", n)
	}

	// Healthy again: resolved.
	push([]map[string]any{room("online", healthy)}, nil)
	if n := count(mtr, "teams_signed_out", "open"); n != "0" {
		t.Errorf("teams_signed_out still open after sign-in")
	}
	if n := count(mtr, "teams_signed_out", "resolved"); n != "1" {
		t.Errorf("resolved teams_signed_out alerts = %s, want 1", n)
	}

	// Offline: the Collector's device_offline event opens nothing for the
	// Teams Room (the Alerter owns it), but still does for the display.
	offlineEvent := func(id string) map[string]any {
		return map[string]any{"device_id": id, "event_type": "alert:device_offline",
			"payload": map[string]any{"severity": "critical", "message": "Device offline"}}
	}
	push([]map[string]any{room("offline", map[string]any{"error": "WinRM sign-in rejected"})},
		[]map[string]any{offlineEvent("fake-mtr"), offlineEvent("fake-disp")})
	if n := count(mtr, "device_offline", "open"); n != "0" {
		t.Errorf("Collector offline event opened an alert for the Teams Room: %s", n)
	}
	if n := count(disp, "device_offline", "open"); n != "1" {
		t.Errorf("display offline alerts = %s, want 1 (old path must still work)", n)
	}
	advance(6 * time.Minute)
	push([]map[string]any{room("offline", map[string]any{"error": "WinRM sign-in rejected"})},
		[]map[string]any{offlineEvent("fake-mtr")})
	if n := count(mtr, "device_offline", "open"); n != "1" {
		t.Errorf("Teams Room offline alerts after 6 min = %s, want exactly 1", n)
	}

	// Back online: the Collector's recovery event is left to the Alerter too,
	// which resolves its own alert.
	push([]map[string]any{room("online", healthy)},
		[]map[string]any{{"device_id": "fake-mtr", "event_type": "alert:device_recovered", "payload": map[string]any{}}})
	if n := count(mtr, "device_offline", "open"); n != "0" {
		t.Errorf("Teams Room offline alert still open after recovery")
	}
	if n := q(`SELECT count(*)::text FROM integration_alert_state WHERE device_id = $1`, mtr); n != "0" {
		t.Errorf("alert state rows left after recovery: %s", n)
	}
}
