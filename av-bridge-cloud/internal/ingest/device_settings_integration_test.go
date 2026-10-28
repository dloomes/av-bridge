package ingest

// Integration test: telemetry doesn't undo a portal edit of a device's
// tags or name. Runs only when TEST_DATABASE_URL points at a disposable
// Postgres 16 (see portalapi/device_move_integration_test.go for the
// docker line).

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

func TestTelemetryKeepsPortalEdits(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping device settings test")
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
	cust := q(`INSERT INTO customers (name) VALUES ('device-settings') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'ds-' || $1::text, 'ds', '\x00') RETURNING id::text`, cust)
	dev := q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, type, protocol, tags)
	          VALUES ($1, $2, 'pdu', 'Rack PDU', 'power', 'aten_pdu', '{"outlet_count": "8"}') RETURNING id::text`, cust, col)

	push := func(stamp, name string, tags map[string]string) {
		t.Helper()
		if err := st.WithTenant(ctx, cust, func(tx pgx.Tx) error {
			_, err := upsertDeviceFromTelemetry(ctx, tx, cust, col, telemetryDTO{
				DeviceID: "pdu", DeviceName: name, DeviceType: "power", Protocol: "aten_pdu",
				Status: "online", Timestamp: time.Now(), Tags: tags, ConfigStamp: stamp,
			})
			return err
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	state := func() string {
		return q(`SELECT COALESCE(tags->>'outlet_1_name', '-') || '|' || COALESCE(tags->>'model', '-') || '|' || name FROM devices WHERE id = $1`, dev)
	}
	old := map[string]string{"outlet_count": "8"}

	// Never edited: telemetry's tags (with what the adapter discovered) are
	// taken, from any Collector, but never its name.
	push("", "pdu-old-name", map[string]string{"outlet_count": "8", "model": "PE6108G"})
	if got := state(); got != "-|PE6108G|Rack PDU" {
		t.Errorf("before any edit = %q", got)
	}

	// The portal edits the tags: it stamps the device, which also bumps
	// the Collector's config version so it re-pulls straight away.
	before := q(`SELECT config_version::text FROM collectors WHERE id = $1`, col)
	q(`UPDATE devices SET tags = '{"outlet_count": "8", "outlet_1_name": "Codec"}',
	          config_updated_at = '2026-10-23 10:00:00.123456+00' WHERE id = $1 RETURNING id::text`, dev)
	if after := q(`SELECT config_version::text FROM collectors WHERE id = $1`, col); after == before {
		t.Errorf("a tag edit should bump the Collector's config version (still %s)", after)
	}

	// Telemetry made with the old settings, however late it arrives, and
	// telemetry from a Collector too old to send a stamp, keep the
	// portal's tags.
	push("", "pdu-old-name", old)
	push("2026-10-23T09:00:00.000000Z", "pdu-old-name", old)
	if got := state(); got != "Codec|-|Rack PDU" {
		t.Errorf("old telemetry after the edit = %q; want the portal's tags kept", got)
	}

	// Telemetry made with the new settings (the stamp the Collector pulled)
	// is followed again, including tags it discovers.
	push("2026-10-23T10:00:00.123456Z", "pdu-old-name",
		map[string]string{"outlet_count": "8", "outlet_1_name": "Codec", "model": "PE6108G"})
	if got := state(); got != "Codec|PE6108G|Rack PDU" {
		t.Errorf("telemetry with the current stamp = %q", got)
	}
}

// An event from a device with no telemetry in the same push goes through
// upsertDeviceMinimal, for a known device and a new one. It once passed
// one argument too few, failing every push that carried such an event.
func TestEventOnlyDeviceUpsert(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping event-only upsert test")
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
	var cust, col, known string
	if err := su.QueryRow(ctx, `INSERT INTO customers (name) VALUES ('event-only') RETURNING id::text`).Scan(&cust); err != nil {
		t.Fatal(err)
	}
	if err := su.QueryRow(ctx, `INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'eo-' || $1::text, 'eo', '\x00') RETURNING id::text`, cust).Scan(&col); err != nil {
		t.Fatal(err)
	}
	if err := su.QueryRow(ctx, `INSERT INTO devices (customer_id, collector_id, reported_id, name, type, protocol, tags, config_updated_at)
	          VALUES ($1, $2, 'codec', 'Codec', 'conferencing', 'cisco_roomos', '{"a":"1"}', now()) RETURNING id::text`, cust, col).Scan(&known); err != nil {
		t.Fatal(err)
	}
	for _, reported := range []string{"codec", "new-device"} {
		var id string
		if err := st.WithTenant(ctx, cust, func(tx pgx.Tx) error {
			var err error
			id, err = upsertDeviceMinimal(ctx, tx, cust, col, reported, "Name", "conferencing")
			return err
		}); err != nil {
			t.Fatalf("%s: %v", reported, err)
		}
		if id == "" || (reported == "codec" && id != known) {
			t.Errorf("%s: id %q (known %s)", reported, id, known)
		}
	}
	var tags string
	if err := su.QueryRow(ctx, `SELECT tags::text FROM devices WHERE id = $1`, known).Scan(&tags); err != nil {
		t.Fatal(err)
	}
	if tags != `{"a": "1"}` {
		t.Errorf("event upsert changed tags: %s", tags)
	}
}

// last_online_at moves only with online telemetry: a failed poll (offline
// telemetry) moves last_seen_at but not when the device last answered.
func TestLastOnlineAt(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping last_online_at test")
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
	var cust, col string
	if err := su.QueryRow(ctx, `INSERT INTO customers (name) VALUES ('last-online') RETURNING id::text`).Scan(&cust); err != nil {
		t.Fatal(err)
	}
	if err := su.QueryRow(ctx, `INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'lo-' || $1::text, 'lo', '\x00') RETURNING id::text`, cust).Scan(&col); err != nil {
		t.Fatal(err)
	}
	push := func(status string, at time.Time) {
		t.Helper()
		if err := st.WithTenant(ctx, cust, func(tx pgx.Tx) error {
			_, err := upsertDeviceFromTelemetry(ctx, tx, cust, col, telemetryDTO{
				DeviceID: "disp", DeviceName: "Display", DeviceType: "display", Protocol: "sony_bravia",
				Status: status, Timestamp: at,
			})
			return err
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	times := func() (online, seen *time.Time) {
		t.Helper()
		if err := su.QueryRow(ctx, `SELECT last_online_at, last_seen_at FROM devices WHERE collector_id = $1 AND reported_id = 'disp'`, col).Scan(&online, &seen); err != nil {
			t.Fatal(err)
		}
		return
	}
	t0 := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Microsecond)

	push("offline", t0) // first seen offline: never answered
	if on, _ := times(); on != nil {
		t.Errorf("offline first report set last_online_at %v", on)
	}
	push("online", t0.Add(time.Minute))
	push("offline", t0.Add(2*time.Minute))
	on, seen := times()
	if on == nil || !on.Equal(t0.Add(time.Minute)) || seen == nil || !seen.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("after online then offline: last_online_at %v, last_seen_at %v", on, seen)
	}
	push("online", t0.Add(30*time.Second)) // a late, older reading doesn't move it back
	if on, _ := times(); on == nil || !on.Equal(t0.Add(time.Minute)) {
		t.Errorf("older online reading moved last_online_at to %v", on)
	}
}
