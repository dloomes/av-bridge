package portalapi

// Integration test for moving devices between collectors (device_move.go)
// and the config-change trigger from migration 0049. Runs only when
// TEST_DATABASE_URL points at a disposable Postgres 16:
//
//	docker run -d --name hatest -e POSTGRES_PASSWORD=pgtest -p 55436:5432 postgres:16
//	TEST_DATABASE_URL=postgres://postgres:pgtest@localhost:55436/postgres?sslmode=disable \
//	  go test ./internal/portalapi/ -run TestDeviceMove -v

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

func TestDeviceMove(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping device move integration test")
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

	cust := q(`INSERT INTO customers (name) VALUES ('move-test') RETURNING id::text`)
	collector := func(bridgeID string) string {
		return q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc)
		          VALUES ($1::uuid, $2 || '-' || $1::uuid::text, $2, '\x00') RETURNING id::text`, cust, bridgeID)
	}
	colA := collector("move-a")
	colB := collector("move-b")
	colC := collector("move-c")
	device := func(col, reported string) string {
		return q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, protocol)
		          VALUES ($1, $2, $3, $3, 'ping') RETURNING id::text`, cust, col, reported)
	}
	d1 := device(colA, "disp-1")
	d2 := device(colA, "disp-2")
	device(colB, "disp-2") // clashes with d2 on B

	// Pretend both bridges are caught up, so only what follows bumps them.
	exec(`UPDATE collectors SET config_version_pulled = config_version WHERE customer_id = $1`, cust)
	needsResync := func(col string) bool {
		t.Helper()
		v, err := st.CollectorNeedsResync(ctx, col)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	// A config-irrelevant update (what ingest does every push) doesn't
	// bump; an adapter-relevant one does.
	exec(`UPDATE devices SET latest_status = 'online', name = 'renamed' WHERE id = $1`, d1)
	if needsResync(colA) {
		t.Error("observation/cosmetic update should not ask the bridge to resync")
	}
	exec(`UPDATE devices SET address = '10.0.0.9' WHERE id = $1`, d1)
	if !needsResync(colA) {
		t.Error("address change should ask the bridge to resync")
	}
	exec(`UPDATE collectors SET config_version_pulled = config_version WHERE customer_id = $1`, cust)

	cmdID := q(`INSERT INTO commands (customer_id, collector_id, device_id, name)
	            VALUES ($1, $2, $3, 'power_on') RETURNING id::text`, cust, colA, d1)

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	r1 := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
	principal := map[string]portalauth.Principal{
		"admin":  {UserID: "00000000-0000-0000-0000-0000000000a1", CustomerID: cust, Permissions: all},
		"scoped": {UserID: "00000000-0000-0000-0000-0000000000a2", CustomerID: cust, Permissions: all, RegionScopeIDs: []string{r1}},
	}

	h := New(st, nil, nil, nil, nil, quiet)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /devices/move", h.MoveDevices)
	mux.HandleFunc("POST /collectors/{id}/replace", h.ReplaceCollector)
	call := func(who, path string, body any) (int, string) {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", path, bytes.NewReader(b))
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), principal[who]))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	collectorOf := func(dev string) string {
		return q(`SELECT collector_id::text FROM devices WHERE id = $1`, dev)
	}
	tombstoned := func(col, reported string) bool {
		return q(`SELECT EXISTS (SELECT 1 FROM collector_device_tombstones
		          WHERE collector_id = $1 AND reported_id = $2)::text`, col, reported) == "true"
	}

	// 1. Move d1 A → B.
	code, body := call("admin", "/devices/move", map[string]any{
		"device_ids": []string{d1}, "target_collector_id": colB,
	})
	if code != 200 {
		t.Fatalf("move d1: %d %s", code, body)
	}
	if collectorOf(d1) != colB {
		t.Error("d1 should now be on collector B")
	}
	if !tombstoned(colA, "disp-1") {
		t.Error("collector A should have a tombstone for disp-1")
	}
	if !needsResync(colA) || !needsResync(colB) {
		t.Error("both collectors should be asked to resync after a move")
	}
	if got := q(`SELECT collector_id::text FROM commands WHERE id = $1`, cmdID); got != colB {
		t.Error("pending command should follow the device to collector B")
	}
	if n := q(`SELECT count(*)::text FROM audit_log WHERE action = 'device.move' AND target_id = $1`, d1); n != "1" {
		t.Errorf("want one device.move audit row, got %s", n)
	}

	// 2. Moving d2 to B clashes on reported_id → 409, nothing moves.
	code, body = call("admin", "/devices/move", map[string]any{
		"device_ids": []string{d2}, "target_collector_id": colB,
	})
	if code != http.StatusConflict {
		t.Errorf("move d2 to B: want 409, got %d %s", code, body)
	}
	if collectorOf(d2) != colA {
		t.Error("d2 must stay on A after a conflicting move")
	}

	// 3. Unknown target / unknown device.
	if code, _ := call("admin", "/devices/move", map[string]any{
		"device_ids": []string{d2}, "target_collector_id": "00000000-0000-0000-0000-000000000000",
	}); code != http.StatusBadRequest {
		t.Errorf("unknown target: want 400, got %d", code)
	}
	if code, _ := call("admin", "/devices/move", map[string]any{
		"device_ids": []string{"00000000-0000-0000-0000-000000000000"}, "target_collector_id": colC,
	}); code != http.StatusNotFound {
		t.Errorf("unknown device: want 404, got %d", code)
	}

	// 4. Replace needs whole-estate scope.
	if code, _ := call("scoped", "/collectors/"+colA+"/replace", map[string]any{"target_collector_id": colC}); code != http.StatusForbidden {
		t.Errorf("scoped replace: want 403, got %d", code)
	}

	// 5. Replace A → C moves everything left on A.
	code, body = call("admin", "/collectors/"+colA+"/replace", map[string]any{"target_collector_id": colC})
	if code != 200 {
		t.Fatalf("replace A→C: %d %s", code, body)
	}
	var res moveResult
	_ = json.Unmarshal([]byte(body), &res)
	if res.Moved != 1 || collectorOf(d2) != colC {
		t.Errorf("replace should move d2 to C, got %+v", res)
	}
	if n := q(`SELECT count(*)::text FROM devices WHERE collector_id = $1 AND deleted_at IS NULL`, colA); n != "0" {
		t.Errorf("collector A should be empty after replace, has %s", n)
	}

	// 6. Moving d1 back to A lifts A's tombstone and tombstones B instead.
	code, body = call("admin", "/devices/move", map[string]any{
		"device_ids": []string{d1}, "target_collector_id": colA,
	})
	if code != 200 {
		t.Fatalf("move d1 back: %d %s", code, body)
	}
	if tombstoned(colA, "disp-1") || !tombstoned(colB, "disp-1") {
		t.Error("moving back should swap the tombstone from A to B")
	}

	// 7. Moving to the collector it's already on is a no-op.
	code, body = call("admin", "/devices/move", map[string]any{
		"device_ids": []string{d1}, "target_collector_id": colA,
	})
	_ = json.Unmarshal([]byte(body), &res)
	if code != 200 || res.Moved != 0 || res.Unchanged != 1 {
		t.Errorf("same-collector move: %d %+v", code, res)
	}
}
