package portalapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

// Room tags — the customer's own labels for rooms (migration 0070),
// managed on the Room tags page and applied to rooms, any number each.
// Listing is open to anyone who can see the dashboard (the filters need
// it); changes need hierarchy.crud, like the rest of the room structure.

// roomTagColours is the portal's chip palette (components/room-tag-chip.tsx).
var roomTagColours = map[string]bool{
	"slate": true, "red": true, "orange": true, "amber": true, "green": true,
	"teal": true, "blue": true, "indigo": true, "purple": true, "pink": true,
}

const maxRoomTagName = 60

type roomTagRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Colour      string `json:"colour"`
	Description string `json:"description,omitempty"`
	RoomCount   int    `json:"room_count"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type roomTagReq struct {
	Name        *string `json:"name,omitempty"`
	Colour      *string `json:"colour,omitempty"`
	Description *string `json:"description,omitempty"`
}

// check trims the name and reports what's wrong with the fields present.
func (req *roomTagReq) check() string {
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		req.Name = &n
		if n == "" {
			return "name cannot be empty"
		}
		if len([]rune(n)) > maxRoomTagName {
			return "name must be 60 characters or fewer"
		}
	}
	if req.Colour != nil && !roomTagColours[*req.Colour] {
		return "unknown colour"
	}
	return ""
}

// ListRoomTags — GET /api/v1/room-tags. Each tag with how many rooms have it.
func (h *Handler) ListRoomTags(w http.ResponseWriter, r *http.Request) {
	out := []roomTagRow{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT t.id::text, t.name, t.colour, COALESCE(t.description, ''),
			       count(a.room_id)::int, t.created_at, t.updated_at
			  FROM room_tags t
			  LEFT JOIN room_tag_assignments a ON a.tag_id = t.id
			 GROUP BY t.id
			 ORDER BY lower(t.name)`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				t                roomTagRow
				created, updated time.Time
			)
			if err := rows.Scan(&t.ID, &t.Name, &t.Colour, &t.Description, &t.RoomCount, &created, &updated); err != nil {
				return err
			}
			t.CreatedAt = created.UTC().Format(time.RFC3339)
			t.UpdatedAt = updated.UTC().Format(time.RFC3339)
			out = append(out, t)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateRoomTag — POST /api/v1/room-tags {name, colour?, description?}
func (h *Handler) CreateRoomTag(w http.ResponseWriter, r *http.Request) {
	var req roomTagReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == nil {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if msg := req.check(); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	colour := "slate"
	if req.Colour != nil {
		colour = *req.Colour
	}
	desc := ""
	if req.Description != nil {
		desc = strings.TrimSpace(*req.Description)
	}
	p, _ := portalauth.From(r.Context())
	var (
		id       string
		conflict bool
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		// Check the name first: a failed INSERT would abort the
		// transaction, and then the commit fails too.
		if taken, err := roomTagNameTaken(ctx, tx, *req.Name, ""); err != nil || taken {
			conflict = taken
			return err
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO room_tags (customer_id, name, colour, description)
			VALUES ($1, $2, $3, NULLIF($4, ''))
			RETURNING id::text`, p.CustomerID, *req.Name, colour, desc).Scan(&id)
		if isUniqueViolation(err) {
			conflict = true
			return nil
		}
		if err != nil {
			return err
		}
		after, err := audit.SnapshotByTable(ctx, tx, "room_tags", id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "room_tag.create", TargetKind: "room_tag", TargetID: id, After: after,
		}))
	})
	if !ok {
		return
	}
	if conflict {
		writeErr(w, http.StatusConflict, "a tag with that name already exists")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": *req.Name})
}

// UpdateRoomTag — PATCH /api/v1/room-tags/{id}. Only the fields present
// change; an empty description clears it.
func (h *Handler) UpdateRoomTag(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeErr(w, http.StatusNotFound, "tag not found")
		return
	}
	var req roomTagReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == nil && req.Colour == nil && req.Description == nil {
		writeErr(w, http.StatusBadRequest, "nothing to update")
		return
	}
	if msg := req.check(); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	p, _ := portalauth.From(r.Context())
	var notFound, conflict bool
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		before, err := audit.SnapshotByTable(ctx, tx, "room_tags", id)
		if err != nil {
			return err
		}
		if before == nil {
			notFound = true
			return nil
		}
		if req.Name != nil {
			if taken, err := roomTagNameTaken(ctx, tx, *req.Name, id); err != nil || taken {
				conflict = taken
				return err
			}
		}
		set := []string{}
		args := []any{id}
		add := func(col string, v any) {
			args = append(args, v)
			set = append(set, col+" = $"+strconv.Itoa(len(args)))
		}
		if req.Name != nil {
			add("name", *req.Name)
		}
		if req.Colour != nil {
			add("colour", *req.Colour)
		}
		if req.Description != nil {
			add("description", nullIfEmpty(strings.TrimSpace(*req.Description)))
		}
		if _, err := tx.Exec(ctx, "UPDATE room_tags SET "+strings.Join(set, ", ")+" WHERE id = $1", args...); err != nil {
			if isUniqueViolation(err) {
				conflict = true
				return nil
			}
			return err
		}
		after, err := audit.SnapshotByTable(ctx, tx, "room_tags", id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "room_tag.update", TargetKind: "room_tag", TargetID: id, Before: before, After: after,
		}))
	})
	if !ok {
		return
	}
	switch {
	case notFound:
		writeErr(w, http.StatusNotFound, "tag not found")
	case conflict:
		writeErr(w, http.StatusConflict, "a tag with that name already exists")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// DeleteRoomTag — DELETE /api/v1/room-tags/{id}. Removes it from its rooms
// too, and says how many.
func (h *Handler) DeleteRoomTag(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeErr(w, http.StatusNotFound, "tag not found")
		return
	}
	p, _ := portalauth.From(r.Context())
	var (
		notFound bool
		untagged int
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		before, err := audit.SnapshotByTable(ctx, tx, "room_tags", id)
		if err != nil {
			return err
		}
		if before == nil {
			notFound = true
			return nil
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM room_tag_assignments WHERE tag_id = $1`, id).Scan(&untagged); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM room_tags WHERE id = $1`, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "room_tag.delete", TargetKind: "room_tag", TargetID: id, Before: before,
			After: mustJSON(map[string]any{"rooms_untagged": untagged}),
		}))
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "tag not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "rooms_untagged": untagged})
}

// roomTagNameTaken reports whether another tag (not except) already has
// this name, ignoring case.
func roomTagNameTaken(ctx context.Context, tx pgx.Tx, name, except string) (bool, error) {
	var taken bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM room_tags
		                WHERE lower(name) = lower($1) AND id::text <> $2)`, name, except).Scan(&taken)
	return taken, err
}

// roomTagNames is a room's tags by name, sorted, for audit entries.
func roomTagNames(ctx context.Context, tx pgx.Tx, roomID string) ([]string, error) {
	names := []string{}
	rows, err := tx.Query(ctx, `
		SELECT t.name FROM room_tag_assignments a JOIN room_tags t ON t.id = a.tag_id
		 WHERE a.room_id = $1 ORDER BY lower(t.name)`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// SetRoomTags — PUT /api/v1/rooms/{id}/tags {tag_ids}. Replaces the room's
// tags with exactly these.
func (h *Handler) SetRoomTags(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	if !uuidRe.MatchString(roomID) {
		writeErr(w, http.StatusNotFound, "room not found")
		return
	}
	var req struct {
		TagIDs []string `json:"tag_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ids := uniqueStrings(req.TagIDs)
	for _, id := range ids {
		if !uuidRe.MatchString(id) {
			writeErr(w, http.StatusBadRequest, "unknown tag: "+id)
			return
		}
	}
	p, _ := portalauth.From(r.Context())
	var (
		notFound, unknownTag bool
		after                []string
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM rooms WHERE id = $1`, roomID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			notFound = true
			return nil
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM room_tags WHERE id = ANY($1::uuid[])`, ids).Scan(&n); err != nil {
			return err
		}
		if n != len(ids) {
			unknownTag = true
			return nil
		}
		before, err := roomTagNames(ctx, tx, roomID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM room_tag_assignments WHERE room_id = $1 AND NOT (tag_id = ANY($2::uuid[]))`, roomID, ids); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO room_tag_assignments (customer_id, room_id, tag_id)
			SELECT $1, $2, t.id FROM room_tags t WHERE t.id = ANY($3::uuid[])
			ON CONFLICT DO NOTHING`, p.CustomerID, roomID, ids); err != nil {
			return err
		}
		if after, err = roomTagNames(ctx, tx, roomID); err != nil {
			return err
		}
		if strings.Join(before, "\x00") == strings.Join(after, "\x00") {
			return nil // nothing changed: no audit entry
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "room.tags_update", TargetKind: "room", TargetID: roomID,
			Before: mustJSON(map[string]any{"tags": before}),
			After:  mustJSON(map[string]any{"tags": after}),
		}))
	})
	if !ok {
		return
	}
	switch {
	case notFound:
		writeErr(w, http.StatusNotFound, "room not found")
	case unknownTag:
		writeErr(w, http.StatusBadRequest, "unknown tag")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"room_id": roomID, "tags": after})
	}
}

// BulkRoomTag — POST /api/v1/room-tags/{id}/rooms {add, remove}: puts the
// tag on, or takes it off, many rooms at once. Rooms that already have it
// (or don't) are left as they are.
func (h *Handler) BulkRoomTag(w http.ResponseWriter, r *http.Request) {
	tagID := r.PathValue("id")
	if !uuidRe.MatchString(tagID) {
		writeErr(w, http.StatusNotFound, "tag not found")
		return
	}
	var req struct {
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	add, remove := uniqueStrings(req.Add), uniqueStrings(req.Remove)
	for _, id := range append(append([]string{}, add...), remove...) {
		if !uuidRe.MatchString(id) {
			writeErr(w, http.StatusBadRequest, "unknown room: "+id)
			return
		}
	}
	if len(add) == 0 && len(remove) == 0 {
		writeErr(w, http.StatusBadRequest, "no rooms given")
		return
	}
	p, _ := portalauth.From(r.Context())
	var (
		notFound       bool
		added, removed int64
	)
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var name string
		err := tx.QueryRow(ctx, `SELECT name FROM room_tags WHERE id = $1`, tagID).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		if err != nil {
			return err
		}
		if len(add) > 0 {
			// Only rooms this caller can see (RLS on rooms).
			tag, err := tx.Exec(ctx, `
				INSERT INTO room_tag_assignments (customer_id, room_id, tag_id)
				SELECT $1, r.id, $2 FROM rooms r WHERE r.id = ANY($3::uuid[])
				ON CONFLICT DO NOTHING`, p.CustomerID, tagID, add)
			if err != nil {
				return err
			}
			added = tag.RowsAffected()
		}
		if len(remove) > 0 {
			tag, err := tx.Exec(ctx,
				`DELETE FROM room_tag_assignments WHERE tag_id = $1 AND room_id = ANY($2::uuid[])`, tagID, remove)
			if err != nil {
				return err
			}
			removed = tag.RowsAffected()
		}
		if added == 0 && removed == 0 {
			return nil
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "room_tag.rooms_update", TargetKind: "room_tag", TargetID: tagID,
			After: mustJSON(map[string]any{
				"tag": name, "rooms_added": added, "rooms_removed": removed,
				"add": add, "remove": remove,
			}),
		}))
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "tag not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"added": added, "removed": removed})
}

type roomTagImportResult struct {
	Processed   int              `json:"processed"`
	RoomsTagged int              `json:"rooms_tagged"`
	TagsCreated []string         `json:"tags_created"`
	Errors      []importRowError `json:"errors"`
}

// ImportRoomTags — POST /api/v1/room-tags/import, multipart "file": a CSV
// with columns building, room and tags (several separated by ";" or "|"),
// plus optional region and location to tell same-named buildings apart.
// Adds tags; it never removes any. Tags that don't exist yet are created.
// Every row is checked first: if any fails, nothing changes (400 with the
// row errors), as the asset import.
func (h *Handler) ImportRoomTags(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "could not parse multipart body")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "file field is required")
		return
	}
	defer file.Close()
	if header.Size > 4<<20 {
		writeErr(w, http.StatusBadRequest, "file exceeds 4MB")
		return
	}
	rows, err := parseRoomTagCSV(file)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res := roomTagImportResult{Processed: len(rows), TagsCreated: []string{}, Errors: []importRowError{}}
	if len(rows) == 0 {
		writeJSON(w, http.StatusOK, res)
		return
	}
	p, _ := portalauth.From(r.Context())

	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		buildings, err := loadBuildingsByName(ctx, tx)
		if err != nil {
			return err
		}
		rooms, err := loadRoomsByBuildingAndName(ctx, tx)
		if err != nil {
			return err
		}
		tags := map[string]string{} // lower(name) -> id
		trows, err := tx.Query(ctx, `SELECT id::text, name FROM room_tags`)
		if err != nil {
			return err
		}
		for trows.Next() {
			var id, name string
			if err := trows.Scan(&id, &name); err != nil {
				trows.Close()
				return err
			}
			tags[strings.ToLower(name)] = id
		}
		trows.Close()
		if err := trows.Err(); err != nil {
			return err
		}

		// Check every row before changing anything.
		type want struct {
			roomID string
			tags   []string
		}
		var wants []want
		newTags := map[string]string{} // lower -> name as first written
		for _, row := range rows {
			bID, msg := resolveBuilding(buildings[strings.ToLower(row.building)], row.region, row.location)
			if bID == "" {
				res.Errors = append(res.Errors, importRowError{Row: row.line, Message: "building " + strconv.Quote(row.building) + ": " + msg})
				continue
			}
			roomID := rooms[bID+"|"+strings.ToLower(row.room)]
			if roomID == "" {
				res.Errors = append(res.Errors, importRowError{Row: row.line, Message: "room " + strconv.Quote(row.room) + " not found in " + row.building})
				continue
			}
			if len(row.tags) == 0 {
				res.Errors = append(res.Errors, importRowError{Row: row.line, Message: "no tags given"})
				continue
			}
			for _, t := range row.tags {
				if len([]rune(t)) > maxRoomTagName {
					res.Errors = append(res.Errors, importRowError{Row: row.line, Message: "tag " + strconv.Quote(t) + " is longer than 60 characters"})
				}
				if _, ok := tags[strings.ToLower(t)]; !ok {
					if _, seen := newTags[strings.ToLower(t)]; !seen {
						newTags[strings.ToLower(t)] = t
					}
				}
			}
			wants = append(wants, want{roomID: roomID, tags: row.tags})
		}
		if len(res.Errors) > 0 {
			return nil
		}

		for lower, name := range newTags {
			var id string
			if err := tx.QueryRow(ctx, `
				INSERT INTO room_tags (customer_id, name) VALUES ($1, $2) RETURNING id::text`,
				p.CustomerID, name).Scan(&id); err != nil {
				return err
			}
			tags[lower] = id
			res.TagsCreated = append(res.TagsCreated, name)
		}
		sort.Strings(res.TagsCreated)

		tagged := map[string]bool{}
		for _, wnt := range wants {
			ids := make([]string, 0, len(wnt.tags))
			for _, t := range wnt.tags {
				ids = append(ids, tags[strings.ToLower(t)])
			}
			tag, err := tx.Exec(ctx, `
				INSERT INTO room_tag_assignments (customer_id, room_id, tag_id)
				SELECT $1, $2, unnest($3::uuid[])
				ON CONFLICT DO NOTHING`, p.CustomerID, wnt.roomID, ids)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				tagged[wnt.roomID] = true
			}
		}
		res.RoomsTagged = len(tagged)
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "room_tag.import", TargetKind: "room_tag",
			After: mustJSON(map[string]any{
				"rows": len(rows), "rooms_tagged": res.RoomsTagged, "tags_created": res.TagsCreated,
			}),
		}))
	})
	if !ok {
		return
	}
	if len(res.Errors) > 0 {
		writeJSON(w, http.StatusBadRequest, res)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type roomTagCSVRow struct {
	line                             int
	region, location, building, room string
	tags                             []string
}

// parseRoomTagCSV reads the import file: a header row naming the columns
// (any order, any case), then one row per room.
func parseRoomTagCSV(rd io.Reader) ([]roomTagCSVRow, error) {
	cr := csv.NewReader(rd)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("couldn't read the CSV header: " + err.Error())
	}
	col := map[string]int{}
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, string(rune(0xFEFF)))))
		col[h] = i
	}
	for _, need := range []string{"building", "room", "tags"} {
		if _, ok := col[need]; !ok {
			return nil, errors.New("the CSV needs building, room and tags columns (missing " + need + ")")
		}
	}
	get := func(rec []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	var out []roomTagCSVRow
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("CSV row " + strconv.Itoa(line) + ": " + err.Error())
		}
		row := roomTagCSVRow{
			line: line, region: get(rec, "region"), location: get(rec, "location"),
			building: get(rec, "building"), room: get(rec, "room"),
		}
		if row.building == "" && row.room == "" && get(rec, "tags") == "" {
			continue // blank line
		}
		seen := map[string]bool{}
		for _, t := range strings.FieldsFunc(get(rec, "tags"), func(r rune) bool { return r == ';' || r == '|' }) {
			t = strings.TrimSpace(t)
			if t != "" && !seen[strings.ToLower(t)] {
				seen[strings.ToLower(t)] = true
				row.tags = append(row.tags, t)
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func uniqueStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
