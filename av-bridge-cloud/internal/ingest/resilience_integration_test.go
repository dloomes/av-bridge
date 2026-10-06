package ingest

// Integration test for the ingest upsert's resilience guards (migration
// 0049): tombstoned devices aren't recreated on their old collector, and
// replayed (older) telemetry doesn't roll latest_status back. Runs only
// when TEST_DATABASE_URL points at a disposable Postgres 16:
//
//	docker run -d --name hatest -e POSTGRES_PASSWORD=pgtest -p 55436:5432 postgres:16
//	TEST_DATABASE_URL=postgres://postgres:pgtest@localhost:55436/postgres?sslmode=disable \
//	  go test ./internal/ingest/ -run TestIngestResilience -v

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/jackc/pgx/v5"
)

func TestIngestResilience(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping ingest resilience integration test")
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

	cust := q(`INSERT INTO customers (name) VALUES ('ingest-resilience') RETURNING id::text`)
	colOld := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	             VALUES ($1::uuid, 'ir-old-' || $1::uuid::text, 'old', '\x00') RETURNING id::text`, cust)
	colNew := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	             VALUES ($1::uuid, 'ir-new-' || $1::uuid::text, 'new', '\x00') RETURNING id::text`, cust)

	upsert := func(col string, tel telemetryDTO) string {
		t.Helper()
		var id string
		if err := st.WithTenant(ctx, cust, func(tx pgx.Tx) error {
			var e error
			id, e = upsertDeviceFromTelemetry(ctx, tx, cust, col, tel)
			return e
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		return id
	}
	count := func(col, reported string) string {
		return q(`SELECT count(*)::text FROM devices WHERE collector_id = $1 AND reported_id = $2`, col, reported)
	}

	now := time.Now().UTC()

	// 1. A brand-new reported_id still auto-creates (legacy path intact).
	if id := upsert(colOld, telemetryDTO{DeviceID: "cam-1", Status: "online", Timestamp: now}); id == "" {
		t.Fatal("unknown device without a tombstone should be created")
	}

	// 2. Move cam-1 to the new collector the way device_move does, then
	//    the old collector reports it again: must not resurrect it there.
	if _, err := su.Exec(ctx, `UPDATE devices SET collector_id = $1 WHERE collector_id = $2 AND reported_id = 'cam-1'`, colNew, colOld); err != nil {
		t.Fatal(err)
	}
	if _, err := su.Exec(ctx, `INSERT INTO collector_device_tombstones (customer_id, collector_id, reported_id) VALUES ($1, $2, 'cam-1')`, cust, colOld); err != nil {
		t.Fatal(err)
	}
	if id := upsert(colOld, telemetryDTO{DeviceID: "cam-1", Status: "online", Timestamp: now}); id != "" {
		t.Error("tombstoned device was recreated on its old collector")
	}
	if n := count(colOld, "cam-1"); n != "0" {
		t.Errorf("old collector has %s cam-1 rows, want 0", n)
	}

	// 3. A device that does exist on a collector is updated even if a
	//    stale tombstone for the same pair lingers.
	if _, err := su.Exec(ctx, `INSERT INTO collector_device_tombstones (customer_id, collector_id, reported_id) VALUES ($1, $2, 'cam-1')`, cust, colNew); err != nil {
		t.Fatal(err)
	}
	if id := upsert(colNew, telemetryDTO{DeviceID: "cam-1", Status: "online", Timestamp: now}); id == "" {
		t.Error("existing device must still accept telemetry")
	}

	// 4. Out-of-order replay: newer "offline" lands, then an older
	//    "online" replays from a spool. Latest state stays offline.
	upsert(colNew, telemetryDTO{DeviceID: "cam-1", Status: "offline", Timestamp: now.Add(time.Minute)})
	upsert(colNew, telemetryDTO{DeviceID: "cam-1", Status: "online", Timestamp: now.Add(-time.Hour)})
	if s := q(`SELECT latest_status FROM devices WHERE collector_id = $1 AND reported_id = 'cam-1'`, colNew); s != "offline" {
		t.Errorf("latest_status = %q after an older replay, want offline", s)
	}
	if !replayedTooLate(now.Add(-time.Hour)) || replayedTooLate(now) || replayedTooLate(time.Time{}) {
		t.Error("replayedTooLate cutoff wrong")
	}
}
