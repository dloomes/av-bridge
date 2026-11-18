package remote

// Integration test for tunnel presence. Runs only when TEST_DATABASE_URL
// points at a disposable Postgres 16.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

// presenceEnv gives each test its own scratch database (created from
// TEST_DATABASE_URL, migrated, dropped afterwards): ClearPresence and Sweep
// act on the whole remote_tunnel table, so sharing the suite's database
// would let them delete rows other packages' tests are using.
func presenceEnv(t *testing.T) (context.Context, *pgxpool.Pool, *pgx.Conn, string, func(name string) string) {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping presence integration test")
	}
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	dbName := fmt.Sprintf("remote_presence_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+dbName); err != nil {
		admin.Close(ctx)
		t.Fatalf("create scratch database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`)
		admin.Close(ctx)
	})
	with := func(user, pass string) string {
		u, _ := url.Parse(base)
		if user != "" {
			u.User = url.UserPassword(user, pass)
		}
		u.Path = "/" + dbName
		return u.String()
	}
	dsn := with("", "")
	if err := db.Migrate(ctx, dsn, quiet); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	su, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { su.Close(ctx) })
	st, err := db.New(ctx, with("app_admin", "app_admin_dev"), with("app_tenant", "app_tenant_dev"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	var cust string
	if err := su.QueryRow(ctx, `INSERT INTO customers (name) VALUES ('presence-test') RETURNING id::text`).Scan(&cust); err != nil {
		t.Fatal(err)
	}
	collector := func(name string) string {
		var id string
		if err := su.QueryRow(ctx, `INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
			VALUES ($1, $2, $3, '\x00') RETURNING id::text`, cust, name+"-"+cust, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	return ctx, st.AdminPool(), su, cust, collector
}

func TestPresenceRecordsAndClears(t *testing.T) {
	ctx, pool, _, _, collector := presenceEnv(t)
	id := collector("presence-a")
	tn := NewTunnels(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tn.SetPresence(pool)

	first := tn.recordConnect(ctx, id)
	if ok, err := TunnelConnected(ctx, pool, id); err != nil || !ok {
		t.Fatalf("after connect: ok=%v err=%v", ok, err)
	}
	// A reconnect writes a newer row; the older tunnel closing must not remove it.
	time.Sleep(5 * time.Millisecond)
	second := tn.recordConnect(ctx, id)
	tn.recordClose(ctx, id, first)
	if ok, _ := TunnelConnected(ctx, pool, id); !ok {
		t.Fatal("older close removed the newer row")
	}
	tn.recordClose(ctx, id, second)
	if ok, _ := TunnelConnected(ctx, pool, id); ok {
		t.Fatal("row remains after close")
	}
}

func TestPresenceClearedOnStartup(t *testing.T) {
	ctx, pool, su, _, collector := presenceEnv(t)
	id := collector("presence-b")
	if _, err := su.Exec(ctx, `INSERT INTO remote_tunnel (collector_id) VALUES ($1)`, id); err != nil {
		t.Fatal(err)
	}
	if ok, _ := TunnelConnected(ctx, pool, id); !ok {
		t.Fatal("seeded row not seen")
	}
	if err := ClearPresence(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if ok, _ := TunnelConnected(ctx, pool, id); ok {
		t.Fatal("still connected after ClearPresence")
	}
}

func TestSweepDropsOffboardingTunnels(t *testing.T) {
	ctx, pool, su, cust, collector := presenceEnv(t)
	id := collector("presence-c")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tn := NewTunnels(nil, log)
	tn.SetPresence(pool)

	c1, c2 := net.Pipe()
	srv, err := yamux.Server(c2, yamux.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	cli, err := yamux.Client(c1, yamux.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	tn.register(id, cli)
	svc := NewService(pool, tn, "access.test", log)

	if err := svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if !tn.Connected(id) {
		t.Fatal("tunnel of an active customer was dropped")
	}
	if _, err := su.Exec(ctx, `UPDATE customers SET offboarding_started_at = now() WHERE id = $1`, cust); err != nil {
		t.Fatal(err)
	}
	if err := svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if tn.Connected(id) || !cli.IsClosed() {
		t.Fatal("tunnel of an offboarding customer was not dropped")
	}
}

// register puts a session in the map as Handle does.
func (t *Tunnels) register(id string, s *yamux.Session) {
	t.mu.Lock()
	t.sessions[id] = s
	t.mu.Unlock()
}

func TestSweepReconcilesPresence(t *testing.T) {
	ctx, pool, su, _, collector := presenceEnv(t)
	stale := collector("presence-stale")
	live := collector("presence-live")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tn := NewTunnels(nil, log)
	tn.SetPresence(pool)

	// A row with no live session (an old worker exited without closing it).
	if _, err := su.Exec(ctx, `INSERT INTO remote_tunnel (collector_id) VALUES ($1)`, stale); err != nil {
		t.Fatal(err)
	}
	// A live session with no row.
	c1, c2 := net.Pipe()
	srv, err := yamux.Server(c2, yamux.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	cli, err := yamux.Client(c1, yamux.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	tn.register(live, cli)

	if err := NewService(pool, tn, "access.test", log).Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, _ := TunnelConnected(ctx, pool, stale); ok {
		t.Fatal("row without a live session survived Sweep")
	}
	if ok, _ := TunnelConnected(ctx, pool, live); !ok {
		t.Fatal("live session has no row after Sweep")
	}
}
