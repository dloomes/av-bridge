package commands

// Integration test for pending-command expiry in the sweeper. Runs only
// when TEST_DATABASE_URL points at a disposable Postgres 16:
//
//	docker run -d --name hatest -e POSTGRES_PASSWORD=pgtest -p 55436:5432 postgres:16
//	TEST_DATABASE_URL=postgres://postgres:pgtest@localhost:55436/postgres?sslmode=disable \
//	  go test ./internal/commands/ -run TestSweeperExpiresPending -v

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSweeperExpiresPending(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping sweeper integration test")
	}
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, dsn, quiet); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	q := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := pool.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	cust := q(`INSERT INTO customers (name) VALUES ('sweeper-test') RETURNING id::text`)
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
	          VALUES ($1::uuid, 'sweep-col-' || $1::uuid::text, 'sweep', '\x00') RETURNING id::text`, cust)
	dev := q(`INSERT INTO devices (customer_id, collector_id, reported_id) VALUES ($1, $2, 'd') RETURNING id::text`, cust, col)
	cmd := func(age time.Duration, status string) string {
		return q(`INSERT INTO commands (customer_id, collector_id, device_id, name, status, submitted_at)
		          VALUES ($1, $2, $3, 'power_off', $4, now() - make_interval(secs => $5))
		          RETURNING id::text`, cust, col, dev, status, int(age.Seconds()))
	}
	stale := cmd(20*time.Minute, "pending")
	fresh := cmd(time.Minute, "pending")

	// A waiter on cmd_done must be woken by the expiry.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `LISTEN `+ChannelDone); err != nil {
		t.Fatal(err)
	}

	NewSweeper(pool, time.Minute, 5*time.Minute, 3, 10*time.Minute, quiet).Sweep(ctx)

	if s := q(`SELECT status || ':' || COALESCE(error,'') FROM commands WHERE id = $1`, stale); s != "failed:expired" {
		t.Errorf("stale pending command: %s, want failed:expired", s)
	}
	if s := q(`SELECT status FROM commands WHERE id = $1`, fresh); s != "pending" {
		t.Errorf("fresh pending command: %s, want pending", s)
	}
	wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// The sweeper is cross-tenant, so leftovers from earlier runs on the
	// same database may notify too — wait for this test's command.
	for {
		n, err := conn.WaitForNotification(wctx)
		if err != nil {
			t.Errorf("want cmd_done notify for %s: %v", stale, err)
			break
		}
		if n.Payload == stale {
			break
		}
	}

	// Disabled expiry leaves old pending commands alone.
	old := cmd(time.Hour, "pending")
	NewSweeper(pool, time.Minute, 5*time.Minute, 3, 0, quiet).Sweep(ctx)
	if s := q(`SELECT status FROM commands WHERE id = $1`, old); s != "pending" {
		t.Errorf("expiry disabled: %s, want pending", s)
	}
}
