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
