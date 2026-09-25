package db

// Integration test for the physical-scope RLS policies (migration 0047).
// Runs only when TEST_DATABASE_URL points at a disposable Postgres 16 whose
// user can create roles, e.g.
//
//	docker run -d --name rls -e POSTGRES_PASSWORD=pgtest -p 55432:5432 postgres:16
//	TEST_DATABASE_URL=postgres://postgres:pgtest@localhost:55432/postgres?sslmode=disable \
//	  go test ./internal/db/ -run TestPhysicalScopeRLS -v
//
// It applies every embedded migration, seeds one tenant with a two-region
// hierarchy, then checks what a caller with each shape of scope can see
// through the app_tenant pool — the same path portal handlers use.

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
)

func withUser(t *testing.T, dsn, user, pass string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	u.User = url.UserPassword(user, pass)
	return u.String()
}

// scopeFixture holds the ids of the seeded hierarchy, keyed by name.
type scopeFixture struct {
	customer string
	ids      map[string]string
}

// Seeded hierarchy (names are what the assertions compare):
//
//	bu1
//	└── r1 (in bu1)
//	    └── l1
//	        ├── b1: room1a, room1b
//	        └── b2: room2a
//	r2 (no business unit)
//	├── l2
//	│   ├── b3: room3a
//	│   └── b4: room4a
//	└── l3
//	    └── b5: room5a
//
// One device per room (dev1a … dev5a), one telemetry row per device, plus
// an unplaced device (devX) that only unscoped callers may see.
func seedScopeFixture(t *testing.T, ctx context.Context, conn *pgx.Conn) scopeFixture {
	t.Helper()
	f := scopeFixture{ids: map[string]string{}}
	q := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := conn.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("seed %q: %v", sql, err)
		}
		return id
	}
	f.customer = q(`INSERT INTO customers (name) VALUES ('rls-test') RETURNING id::text`)
	c := f.customer
	f.ids["bu1"] = q(`INSERT INTO business_units (customer_id, name) VALUES ($1, 'bu1') RETURNING id::text`, c)
	f.ids["r1"] = q(`INSERT INTO regions (customer_id, name, business_unit_id) VALUES ($1, 'r1', $2) RETURNING id::text`, c, f.ids["bu1"])
	f.ids["r2"] = q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'r2') RETURNING id::text`, c)
	for _, l := range [][2]string{{"l1", "r1"}, {"l2", "r2"}, {"l3", "r2"}} {
		f.ids[l[0]] = q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, $3) RETURNING id::text`, c, f.ids[l[1]], l[0])
	}
	for _, b := range [][2]string{{"b1", "l1"}, {"b2", "l1"}, {"b3", "l2"}, {"b4", "l2"}, {"b5", "l3"}} {
		f.ids[b[0]] = q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, $3) RETURNING id::text`, c, f.ids[b[1]], b[0])
	}
	for _, r := range [][2]string{{"room1a", "b1"}, {"room1b", "b1"}, {"room2a", "b2"}, {"room3a", "b3"}, {"room4a", "b4"}, {"room5a", "b5"}} {
		f.ids[r[0]] = q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, $3) RETURNING id::text`, c, f.ids[r[1]], r[0])
	}
	col := q(`INSERT INTO collectors (customer_id, name, hmac_secret_enc) VALUES ($1, 'col', '\x00'::bytea) RETURNING id::text`, c)
	for _, d := range [][2]string{{"dev1a", "room1a"}, {"dev1b", "room1b"}, {"dev2a", "room2a"}, {"dev3a", "room3a"}, {"dev4a", "room4a"}, {"dev5a", "room5a"}, {"devX", ""}} {
		var room any
		if d[1] != "" {
			room = f.ids[d[1]]
		}
		f.ids[d[0]] = q(`INSERT INTO devices (customer_id, collector_id, room_id, name, reported_id) VALUES ($1, $2, $3, $4, $4) RETURNING id::text`, c, col, room, d[0])
		q(`INSERT INTO telemetry (customer_id, device_id, ts) VALUES ($1, $2, now()) RETURNING device_id::text`, c, f.ids[d[0]])
	}
	return f
}

func TestPhysicalScopeRLS(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping RLS integration test")
	}
	ctx := context.Background()
	if err := Migrate(ctx, dsn, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	su, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer su.Close(ctx)
	f := seedScopeFixture(t, ctx, su)

	st, err := New(ctx, withUser(t, dsn, "app_admin", "app_admin_dev"), withUser(t, dsn, "app_tenant", "app_tenant_dev"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()

	id := func(names ...string) []string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = f.ids[n]
		}
		return out
	}
	names := func(tx pgx.Tx, sql string) []string {
		t.Helper()
		rows, err := tx.Query(ctx, sql)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		slices.Sort(out)
		return out
	}
	type want struct {
		bus, regions, locations, buildings, rooms, devices []string
	}
	all := want{
		bus:       []string{"bu1"},
		regions:   []string{"r1", "r2"},
		locations: []string{"l1", "l2", "l3"},
		buildings: []string{"b1", "b2", "b3", "b4", "b5"},
		rooms:     []string{"room1a", "room1b", "room2a", "room3a", "room4a", "room5a"},
		devices:   []string{"dev1a", "dev1b", "dev2a", "dev3a", "dev4a", "dev5a", "devX"},
	}
	cases := []struct {
		name  string
		scope Scope
		want  want
	}{
		{"unscoped sees the whole tenant", Scope{}, all},
		{"region: everything under it, nothing above", Scope{Regions: id("r2")}, want{
			regions: []string{"r2"}, locations: []string{"l2", "l3"},
			buildings: []string{"b3", "b4", "b5"}, rooms: []string{"room3a", "room4a", "room5a"},
			devices: []string{"dev3a", "dev4a", "dev5a"},
		}},
		{"location: its buildings, plus the path up", Scope{Locations: id("l2")}, want{
			regions: []string{"r2"}, locations: []string{"l2"},
			buildings: []string{"b3", "b4"}, rooms: []string{"room3a", "room4a"},
			devices: []string{"dev3a", "dev4a"},
		}},
		{"building: its rooms, plus the path up to the BU", Scope{Buildings: id("b1")}, want{
			bus: []string{"bu1"}, regions: []string{"r1"}, locations: []string{"l1"},
			buildings: []string{"b1"}, rooms: []string{"room1a", "room1b"},
			devices: []string{"dev1a", "dev1b"},
		}},
		{"single room: only that room, plus the path", Scope{Rooms: id("room3a")}, want{
			regions: []string{"r2"}, locations: []string{"l2"},
			buildings: []string{"b3"}, rooms: []string{"room3a"},
			devices: []string{"dev3a"},
		}},
		{"business unit: everything in its regions", Scope{BusinessUnits: id("bu1")}, want{
			bus: []string{"bu1"}, regions: []string{"r1"}, locations: []string{"l1"},
			buildings: []string{"b1", "b2"}, rooms: []string{"room1a", "room1b", "room2a"},
			devices: []string{"dev1a", "dev1b", "dev2a"},
		}},
		{"levels union: region r2 plus one room elsewhere", Scope{Regions: id("r2"), Rooms: id("room1a")}, want{
			bus: []string{"bu1"}, regions: []string{"r1", "r2"}, locations: []string{"l1", "l2", "l3"},
			buildings: []string{"b1", "b3", "b4", "b5"}, rooms: []string{"room1a", "room3a", "room4a", "room5a"},
			devices: []string{"dev1a", "dev3a", "dev4a", "dev5a"},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := st.WithTenantScope(ctx, f.customer, tc.scope, func(tx pgx.Tx) error {
				got := want{
					bus:       names(tx, `SELECT name FROM business_units`),
					regions:   names(tx, `SELECT name FROM regions`),
					locations: names(tx, `SELECT name FROM locations`),
					buildings: names(tx, `SELECT name FROM buildings`),
					rooms:     names(tx, `SELECT name FROM rooms`),
					devices:   names(tx, `SELECT name FROM devices`),
				}
				for _, c := range []struct {
					what      string
					got, want []string
				}{
					{"business units", got.bus, tc.want.bus},
					{"regions", got.regions, tc.want.regions},
					{"locations", got.locations, tc.want.locations},
					{"buildings", got.buildings, tc.want.buildings},
					{"rooms", got.rooms, tc.want.rooms},
					{"devices", got.devices, tc.want.devices},
				} {
					w := c.want
					if w == nil {
						w = []string{}
					}
					if !slices.Equal(c.got, w) {
						t.Errorf("%s: got %v, want %v", c.what, c.got, w)
					}
				}
				// Device-anchored data follows device visibility.
				tel := names(tx, `SELECT d.name FROM telemetry t JOIN devices d ON d.id = t.device_id`)
				if !slices.Equal(tel, got.devices) {
					t.Errorf("telemetry devices: got %v, want %v", tel, got.devices)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("tx: %v", err)
			}
		})
	}

	t.Run("scoped caller cannot modify a device outside scope", func(t *testing.T) {
		err := st.WithTenantScope(ctx, f.customer, Scope{Rooms: id("room3a")}, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE devices SET name = name WHERE id = $1`, f.ids["dev1a"])
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 0 {
				t.Errorf("updated %d out-of-scope device rows, want 0", tag.RowsAffected())
			}
			return nil
		})
		if err != nil {
			t.Fatalf("tx: %v", err)
		}
	})

	// A task from the previous release, still running mid-rollout, sets only
	// the legacy building/BU variables and no scope path. Its restricted
	// users must never see MORE than their scope. (Without the path they
	// see less — nothing, in fact — until the rollout finishes: fail closed.)
	t.Run("legacy session variables never widen access", func(t *testing.T) {
		err := st.WithTenant(ctx, f.customer, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.building_scope', $1, true)`, f.ids["b1"]); err != nil {
				return err
			}
			for _, d := range names(tx, `SELECT name FROM devices`) {
				if d != "dev1a" && d != "dev1b" {
					t.Errorf("legacy building scope exposed out-of-scope device %s", d)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("tx: %v", err)
		}
	})
}
