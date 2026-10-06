package portalapi

// Integration test for the portal side of collector self-update: the
// collector list's update fields, PATCH update_window, and the Update
// endpoints. Runs only when TEST_DATABASE_URL points at a disposable
// Postgres 16 (see device_move_integration_test.go for the docker line).

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

	"github.com/dloomes/av-bridge-cloud/internal/collectorupdate"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

func TestCollectorUpdateEndpoints(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping collector update endpoint test")
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

	var cust, ready, blocked, current string
	_ = su.QueryRow(ctx, `INSERT INTO customers (name) VALUES ('upd-endpoints') RETURNING id::text`).Scan(&cust)
	ins := func(name, version string, capable bool, blocker string) string {
		var id string
		if err := su.QueryRow(ctx, `
			INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc,
			                        bridge_version, bridge_platform, update_capable, update_blocker)
			VALUES ($1::uuid, $2 || '-' || $1::uuid::text, $2, '\x00', $3, 'linux/amd64', $4, NULLIF($5, ''))
			RETURNING id::text`, cust, name, version, capable, blocker).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ready = ins("ready", "v1.0.19", true, "")
	blocked = ins("blocked", "v1.0.19", false, "runs in a container; update the container image instead")
	current = ins("current", "v1.0.20", true, "")

	release := &collectorupdate.Release{Version: "v1.0.20", Files: map[string]collectorupdate.File{
		"av-bridge-linux-amd64": {SHA256: "aa", Signature: "sig"},
	}}
	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	admin := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000b1", CustomerID: cust, Permissions: all}

	h := New(st, nil, nil, nil, nil, quiet).SetCollectorUpdates(collectorupdate.NewService(st, release, quiet))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /collectors", h.ListCollectors)
	mux.HandleFunc("PATCH /collectors/{id}", h.UpdateCollector)
	mux.HandleFunc("POST /collectors/{id}/update", h.RequestCollectorUpdate)
	mux.HandleFunc("POST /collectors/update-all", h.RequestAllCollectorUpdates)
	call := func(method, path string, body any) (int, []byte) {
		t.Helper()
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rdr)
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), admin))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}

	type row struct {
		ID              string `json:"id"`
		LatestVersion   string `json:"latest_version"`
		UpdateAvailable bool   `json:"update_available"`
		UpdateBlocker   string `json:"update_blocker"`
		UpdateState     string `json:"update_state"`
		UpdateWindow    string `json:"update_window"`
		TimeZone        string `json:"time_zone"`
	}
	list := func() map[string]row {
		t.Helper()
		code, body := call("GET", "/collectors", nil)
		if code != 200 {
			t.Fatalf("list: %d %s", code, body)
		}
		var rows []row
		_ = json.Unmarshal(body, &rows)
		out := map[string]row{}
		for _, r := range rows {
			out[r.ID] = r
		}
		return out
	}

	rows := list()
	if r := rows[ready]; !r.UpdateAvailable || r.LatestVersion != "v1.0.20" || r.UpdateState != "idle" || r.TimeZone != "Europe/London" {
		t.Errorf("ready collector: %+v", r)
	}
	if r := rows[blocked]; r.UpdateAvailable || r.UpdateBlocker == "" {
		t.Errorf("blocked collector should explain why: %+v", r)
	}
	if r := rows[current]; r.UpdateAvailable || r.UpdateBlocker != "up to date" {
		t.Errorf("current collector: %+v", r)
	}

	// Schedule.
	if code, body := call("PATCH", "/collectors/"+ready, map[string]any{"update_window": "02:30"}); code != http.StatusNoContent && code != 200 {
		t.Fatalf("set window: %d %s", code, body)
	}
	if w := list()[ready].UpdateWindow; w != "02:30" {
		t.Errorf("window = %q, want 02:30", w)
	}
	if code, _ := call("PATCH", "/collectors/"+ready, map[string]any{"update_window": "7pm"}); code != http.StatusBadRequest {
		t.Errorf("bad window: want 400, got %d", code)
	}
	call("PATCH", "/collectors/"+ready, map[string]any{"update_window": ""})
	if w := list()[ready].UpdateWindow; w != "" {
		t.Errorf("window should clear, got %q", w)
	}

	// Update buttons.
	if code, body := call("POST", "/collectors/"+blocked+"/update", nil); code != http.StatusConflict {
		t.Errorf("blocked update: want 409, got %d %s", code, body)
	}
	if code, body := call("POST", "/collectors/update-all", nil); code != http.StatusAccepted || !bytes.Contains(body, []byte(`"requested":1`)) {
		t.Errorf("update all: %d %s, want exactly the ready collector requested", code, body)
	}
	if s := list()[ready].UpdateState; s != "requested" {
		t.Errorf("ready collector state %q, want requested", s)
	}
	if code, _ := call("POST", "/collectors/"+ready+"/update", nil); code != http.StatusConflict {
		t.Errorf("already requested: want 409, got %d", code)
	}
}
