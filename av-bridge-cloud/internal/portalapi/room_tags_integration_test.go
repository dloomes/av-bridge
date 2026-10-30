package portalapi

// Integration test for room tags (migration 0070): the tag list and its
// room counts, create / rename / delete, setting a room's tags, bulk
// add and remove, the CSV import, and that one customer can't tag
// another's rooms. Runs only when TEST_DATABASE_URL points at a disposable
// Postgres 16 (see device_move_integration_test.go for the docker line).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

func TestRoomTags(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping room tags test")
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

	place := func(cust, building string) string {
		reg := q(`INSERT INTO regions (customer_id, name) VALUES ($1, 'North') RETURNING id::text`, cust)
		loc := q(`INSERT INTO locations (customer_id, region_id, name) VALUES ($1, $2, 'Leeds') RETURNING id::text`, cust, reg)
		return q(`INSERT INTO buildings (customer_id, location_id, name) VALUES ($1, $2, $3) RETURNING id::text`, cust, loc, building)
	}
	cust := q(`INSERT INTO customers (name) VALUES ('room-tags') RETURNING id::text`)
	hq := place(cust, "Crown Court")
	room := func(cust, building, name string) string {
		return q(`INSERT INTO rooms (customer_id, building_id, name) VALUES ($1, $2, $3) RETURNING id::text`, cust, building, name)
	}
	court1 := room(cust, hq, "Court 1")
	court2 := room(cust, hq, "Court 2")
	meeting := room(cust, hq, "Meeting room")
	other := q(`INSERT INTO customers (name) VALUES ('room-tags-other') RETURNING id::text`)
	theirs := room(other, place(other, "Elsewhere"), "Their room")

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	admin := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000c1", CustomerID: cust, Permissions: all}

	h := New(st, nil, nil, nil, nil, quiet)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /room-tags", h.ListRoomTags)
	mux.HandleFunc("POST /room-tags", h.CreateRoomTag)
	mux.HandleFunc("POST /room-tags/import", h.ImportRoomTags)
	mux.HandleFunc("PATCH /room-tags/{id}", h.UpdateRoomTag)
	mux.HandleFunc("DELETE /room-tags/{id}", h.DeleteRoomTag)
	mux.HandleFunc("POST /room-tags/{id}/rooms", h.BulkRoomTag)
	mux.HandleFunc("PUT /rooms/{id}/tags", h.SetRoomTags)
	mux.HandleFunc("GET /rooms", h.ListRooms)
	send := func(req *http.Request) (int, []byte) {
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), admin))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	call := func(method, path string, body any) (int, []byte) {
		t.Helper()
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		return send(httptest.NewRequest(method, path, rdr))
	}
	create := func(name, colour string) string {
		t.Helper()
		code, body := call("POST", "/room-tags", map[string]string{"name": name, "colour": colour})
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, code, body)
		}
		var out struct{ ID string }
		_ = json.Unmarshal(body, &out)
		return out.ID
	}
	counts := func() map[string]int {
		t.Helper()
		_, body := call("GET", "/room-tags", nil)
		var rows []roomTagRow
		_ = json.Unmarshal(body, &rows)
		out := map[string]int{}
		for _, r := range rows {
			out[r.Name] = r.RoomCount
		}
		return out
	}

	courtroom := create("Courtroom", "blue")
	pilot := create("Pilot", "green")

	// Names are unique ignoring case, and checked.
	if code, _ := call("POST", "/room-tags", map[string]string{"name": "  courtroom "}); code != http.StatusConflict {
		t.Errorf("duplicate name: %d", code)
	}
	if code, _ := call("POST", "/room-tags", map[string]string{"name": "x", "colour": "chartreuse"}); code != http.StatusBadRequest {
		t.Errorf("unknown colour: %d", code)
	}

	// A room's tags are replaced as a set.
	if code, body := call("PUT", "/rooms/"+court1+"/tags", map[string]any{"tag_ids": []string{courtroom, pilot}}); code != 200 {
		t.Fatalf("set tags: %d %s", code, body)
	}
	if code, _ := call("PUT", "/rooms/"+court1+"/tags", map[string]any{"tag_ids": []string{courtroom}}); code != 200 {
		t.Fatalf("set tags again: %d", code)
	}
	// Not another customer's room, and not a tag that doesn't exist.
	if code, _ := call("PUT", "/rooms/"+theirs+"/tags", map[string]any{"tag_ids": []string{courtroom}}); code != http.StatusNotFound {
		t.Errorf("tagging another customer's room: %d", code)
	}
	if code, _ := call("PUT", "/rooms/"+court2+"/tags", map[string]any{"tag_ids": []string{"00000000-0000-0000-0000-000000000099"}}); code != http.StatusBadRequest {
		t.Errorf("unknown tag: %d", code)
	}

	// Bulk: add to two rooms (one already has it), and their room is ignored.
	code, body := call("POST", "/room-tags/"+courtroom+"/rooms", map[string]any{"add": []string{court1, court2, theirs}})
	if code != 200 || !strings.Contains(string(body), `"added":1`) {
		t.Errorf("bulk add: %d %s", code, body)
	}
	if n := q(`SELECT count(*)::text FROM room_tag_assignments WHERE room_id = $1`, theirs); n != "0" {
		t.Errorf("their room was tagged: %s", n)
	}
	if c := counts(); c["Courtroom"] != 2 || c["Pilot"] != 0 {
		t.Errorf("counts after bulk add: %v", c)
	}

	// The room list carries each room's tags.
	_, body = call("GET", "/rooms", nil)
	var rooms []struct {
		ID     string   `json:"id"`
		TagIDs []string `json:"tag_ids"`
	}
	_ = json.Unmarshal(body, &rooms)
	tagsOf := map[string][]string{}
	for _, r := range rooms {
		tagsOf[r.ID] = r.TagIDs
	}
	if len(tagsOf[court2]) != 1 || tagsOf[court2][0] != courtroom || tagsOf[meeting] == nil || len(tagsOf[meeting]) != 0 {
		t.Errorf("room list tags: court2 %v, meeting %v", tagsOf[court2], tagsOf[meeting])
	}

	// CSV import: adds tags, creating ones that don't exist; a bad row
	// changes nothing.
	upload := func(csv string) (int, roomTagImportResult) {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "tags.csv")
		_, _ = fw.Write([]byte(csv))
		_ = mw.Close()
		req := httptest.NewRequest("POST", "/room-tags/import", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		code, body := send(req)
		var res roomTagImportResult
		_ = json.Unmarshal(body, &res)
		return code, res
	}
	code, res := upload("Building,Room,Tags\nCrown Court,Meeting room,Video link; pilot\nCrown Court,Nowhere,Pilot\n")
	if code != http.StatusBadRequest || len(res.Errors) != 1 || res.Errors[0].Row != 3 {
		t.Errorf("import with a bad row: %d %+v", code, res)
	}
	if c := counts(); c["Pilot"] != 0 {
		t.Errorf("a failed import changed tags: %v", c)
	}
	code, res = upload(string(rune(0xFEFF)) + "building,room,tags\nCrown Court,Meeting room,Video link; pilot\nCrown Court,Court 2,Video link\n")
	if code != 200 || res.RoomsTagged != 2 || strings.Join(res.TagsCreated, ",") != "Video link" {
		t.Errorf("import: %d %+v", code, res)
	}
	if c := counts(); c["Video link"] != 2 || c["Pilot"] != 1 {
		t.Errorf("counts after import: %v", c)
	}

	// Rename, then delete: the tag leaves its rooms, and says how many.
	if code, _ := call("PATCH", "/room-tags/"+pilot, map[string]string{"name": "Pilot 2027", "colour": "amber"}); code != http.StatusNoContent {
		t.Errorf("rename: %d", code)
	}
	code, body = call("DELETE", "/room-tags/"+courtroom, nil)
	if code != 200 || !strings.Contains(string(body), `"rooms_untagged":2`) {
		t.Errorf("delete: %d %s", code, body)
	}
	c := counts()
	names := make([]string, 0, len(c))
	for n := range c {
		names = append(names, n)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "Pilot 2027,Video link" {
		t.Errorf("tags after delete: %v", names)
	}

	// Every change is audited.
	if n := q(`SELECT count(*)::text FROM audit_log WHERE customer_id = $1 AND action LIKE 'room%tag%'`, cust); n == "0" {
		t.Error("no audit entries")
	}
}
