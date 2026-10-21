package retention

// Integration test for the retention rules. Runs only when TEST_DATABASE_URL
// points at a disposable Postgres 16 (see
// portalapi/device_move_integration_test.go for the docker line).

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

func TestRetentionRules(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping retention test")
	}
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, dsn, quiet); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	q := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := conn.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}

	cust := q(`INSERT INTO customers (name) VALUES ('retention') RETURNING id::text`)
	short := q(`INSERT INTO customers (name) VALUES ('retention-short') RETURNING id::text`)
	q(`INSERT INTO nightly_schedule (customer_id, retention_days) VALUES ($1, 30) RETURNING customer_id::text`, short)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'rt-' || $1::text, 'rt', '\x00') RETURNING id::text`, cust)
	colS := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	           VALUES ($1::uuid, 'rt-' || $1::text, 'rt', '\x00') RETURNING id::text`, short)
	dev := q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, protocol) VALUES ($1, $2, 'd', 'D', 'ping') RETURNING id::text`, cust, col)
	reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'R') RETURNING id::text`, cust)
	loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'L') RETURNING id::text`, cust, reg)
	bld := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'B') RETURNING id::text`, cust, loc)
	room := q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'Rm') RETURNING id::text`, cust, bld)
	regS := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'R') RETURNING id::text`, short)
	locS := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'L') RETURNING id::text`, short, regS)
	bldS := q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, 'B') RETURNING id::text`, short, locS)
	roomS := q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, 'Rm') RETURNING id::text`, short, bldS)
	_ = colS

	// Each record: (table, id, should it go?).
	type rec struct {
		table, id string
		gone      bool
	}
	var recs []rec
	add := func(table string, gone bool, sql string, args ...any) {
		recs = append(recs, rec{table, q(sql, args...), gone})
	}
	audit := `INSERT INTO audit_log (customer_id, actor, action, target_kind, ts) VALUES ($1, 'a', 'x.y', 'device', now() - $2::interval) RETURNING id::text`
	add("audit_log", true, audit, cust, "25 months")
	add("audit_log", false, audit, cust, "23 months")

	alert := `INSERT INTO alerts (customer_id, device_id, alert_key, severity, status, opened_at, resolved_at)
	          VALUES ($1, $2, 'offline', 'warning', $3, now() - interval '2 years', now() - $4::interval) RETURNING id::text`
	add("alerts", true, alert, cust, dev, "resolved", "13 months")
	add("alerts", false, alert, cust, dev, "resolved", "11 months")
	add("alerts", false, `INSERT INTO alerts (customer_id, device_id, alert_key, severity, status, opened_at)
	                      VALUES ($1, $2, 'stuck', 'critical', 'open', now() - interval '2 years') RETURNING id::text`, cust, dev)

	event := `INSERT INTO events (customer_id, device_id, event_type, ts) VALUES ($1, $2, 'telnet_message', now() - $3::interval) RETURNING id::text`
	add("events", true, event, cust, dev, "91 days")
	add("events", false, event, cust, dev, "89 days")

	cmd := `INSERT INTO commands (customer_id, collector_id, device_id, name, status, submitted_at) VALUES ($1, $2, $3, 'power_on', $4, now() - $5::interval) RETURNING id::text`
	add("commands", true, cmd, cust, col, dev, "succeeded", "91 days")
	add("commands", false, cmd, cust, col, dev, "succeeded", "89 days")
	// Never finished: kept. In progress and never claimed, so the commands
	// sweeper (whose tests share this database) leaves it alone too.
	add("commands", false, cmd, cust, col, dev, "in_progress", "200 days")

	run := `INSERT INTO nightly_run (customer_id, room_id, phase, status, scheduled_at) VALUES ($1, $2, 'ready', $3, now() - $4::interval) RETURNING id::text`
	add("nightly_run", true, run, cust, room, "succeeded", "91 days")  // default 90
	add("nightly_run", false, run, cust, room, "succeeded", "60 days") // within 90
	add("nightly_run", true, run, short, roomS, "failed", "31 days")   // customer keeps 30
	add("nightly_run", false, run, cust, room, "in_progress", "200 days")

	sess := `INSERT INTO remote_session (customer_id, device_id, actor, host_label, scheme, port, launch_code_hash, expires_at, ended_at)
	         VALUES ($1, $2, 'a', $3, 'http', 80, '\x00', now(), now() - $4::interval) RETURNING id::text`
	add("remote_session", true, sess, cust, dev, "s-old-"+cust, "91 days")
	add("remote_session", false, sess, cust, dev, "s-new-"+cust, "1 day")

	// Apply as app_admin, the role the cloud runs retention as, so a
	// missing grant fails here rather than in production.
	u, _ := url.Parse(dsn)
	u.User = url.UserPassword("app_admin", "app_admin_dev")
	admin, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, `DELETE FROM audit_log WHERE false`); err == nil {
		t.Error("app_admin can delete from audit_log directly; it should stay append-only")
	}

	clog := `INSERT INTO collector_logs (customer_id, collector_id, ts, level, msg, inserted_at)
	         VALUES ($1, $2, now(), 'INFO', 'x', now() - $3::interval) RETURNING id::text`
	add("collector_logs", true, clog, cust, col, "31 days")
	add("collector_logs", false, clog, cust, col, "29 days")

	deadline := time.Now().Add(time.Minute)
	for _, r := range Rules {
		if _, err := Apply(ctx, admin, r, deadline); err != nil {
			t.Fatalf("%s: %v", r.Name, err)
		}
	}
	for _, r := range recs {
		n := q(`SELECT count(*)::text FROM `+r.table+` WHERE id::text = $1`, r.id)
		if r.gone && n != "0" {
			t.Errorf("%s %s: kept, want deleted", r.table, r.id)
		}
		if !r.gone && n != "1" {
			t.Errorf("%s %s: deleted, want kept", r.table, r.id)
		}
	}
}
