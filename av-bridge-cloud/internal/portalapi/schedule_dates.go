package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

// One-off schedule dates (migration 0066): a date that closes a room, or
// every room, for the day, or opens it with its own times, whatever the
// weekly schedule says. The scheduler reads them (nightly.dayPlan).

type scheduleDate struct {
	ID           string `json:"id"`
	Date         string `json:"date"` // YYYY-MM-DD, the organisation's timezone
	RoomID       string `json:"room_id,omitempty"`
	RoomName     string `json:"room_name,omitempty"`
	Closed       bool   `json:"closed"`
	PowerOnTime  string `json:"power_on_time,omitempty"`  // HH:MM, open dates only
	PowerOffTime string `json:"power_off_time,omitempty"` // HH:MM, open dates only
	Note         string `json:"note,omitempty"`
	CreatedBy    string `json:"created_by,omitempty"`
}

// ListScheduleDates — GET /api/v1/nightly/dates?from=YYYY-MM-DD
//
// Dates from `from` (default: a week ago) onwards, soonest first.
func (h *Handler) ListScheduleDates(w http.ResponseWriter, r *http.Request) {
	from := r.URL.Query().Get("from")
	if from == "" {
		from = time.Now().UTC().AddDate(0, 0, -7).Format(time.DateOnly)
	} else if _, err := time.Parse(time.DateOnly, from); err != nil {
		writeErr(w, http.StatusBadRequest, "from must be a date, YYYY-MM-DD")
		return
	}
	out := []scheduleDate{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT sd.id::text, sd.on_date::text, COALESCE(sd.room_id::text, ''), COALESCE(r.name, ''),
			       sd.closed,
			       COALESCE(to_char(sd.power_on_time, 'HH24:MI'), ''),
			       COALESCE(to_char(sd.power_off_time, 'HH24:MI'), ''),
			       sd.note, sd.created_by
			  FROM schedule_date sd
			  LEFT JOIN rooms r ON r.id = sd.room_id
			 WHERE sd.on_date >= $1::date
			 ORDER BY sd.on_date, sd.room_id NULLS FIRST, r.name`, from)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (scheduleDate, error) {
			var d scheduleDate
			err := row.Scan(&d.ID, &d.Date, &d.RoomID, &d.RoomName, &d.Closed,
				&d.PowerOnTime, &d.PowerOffTime, &d.Note, &d.CreatedBy)
			return d, err
		})
		return err
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type createScheduleDateReq struct {
	Date         string `json:"date"`
	RoomID       string `json:"room_id"` // empty: every room
	Closed       bool   `json:"closed"`
	PowerOnTime  string `json:"power_on_time"`
	PowerOffTime string `json:"power_off_time"`
	Note         string `json:"note"`
}

// CreateScheduleDate — POST /api/v1/nightly/dates
func (h *Handler) CreateScheduleDate(w http.ResponseWriter, r *http.Request) {
	var req createScheduleDateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if _, err := time.Parse(time.DateOnly, req.Date); err != nil {
		writeErr(w, http.StatusBadRequest, "date must be YYYY-MM-DD")
		return
	}
	parseHM := func(s, field string) (any, bool) {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, true
		}
		if _, err := time.Parse("15:04", s); err != nil {
			writeErr(w, http.StatusBadRequest, field+" must be a time, HH:MM")
			return nil, false
		}
		return s, true
	}
	on, ok := parseHM(req.PowerOnTime, "power_on_time")
	if !ok {
		return
	}
	off, ok := parseHM(req.PowerOffTime, "power_off_time")
	if !ok {
		return
	}
	if req.Closed && (on != nil || off != nil) {
		writeErr(w, http.StatusBadRequest, "a closed date has no times")
		return
	}
	if on != nil && off != nil && on.(string) >= off.(string) {
		writeErr(w, http.StatusBadRequest, "power-on must be before power-off")
		return
	}
	var roomArg any
	if req.RoomID != "" {
		roomArg = req.RoomID
	}
	note := strings.TrimSpace(req.Note)
	if len(note) > 200 {
		note = note[:200]
	}

	p, _ := portalauth.From(r.Context())
	var (
		id                  string
		roomMissing, exists bool
	)
	ok = h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		if roomArg != nil {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM rooms WHERE id::text = $1`, roomArg).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				roomMissing = true
				return nil
			}
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO schedule_date (customer_id, room_id, on_date, closed, power_on_time, power_off_time, note, created_by)
			VALUES ($1, $2::uuid, $3::date, $4, $5::time, $6::time, $7, $8)
			RETURNING id::text`,
			p.CustomerID, roomArg, req.Date, req.Closed, on, off, note, p.Email).Scan(&id)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			exists = true
			return nil
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "nightly.date_added", TargetKind: "schedule_date", TargetID: id,
			After: map[string]any{"date": req.Date, "room_id": req.RoomID, "closed": req.Closed,
				"power_on_time": req.PowerOnTime, "power_off_time": req.PowerOffTime, "note": note},
		}))
	})
	switch {
	case !ok:
	case roomMissing:
		writeErr(w, http.StatusNotFound, "room not found")
	case exists:
		writeErr(w, http.StatusConflict, "there's already a date for that day; remove it first")
	default:
		writeJSON(w, http.StatusCreated, map[string]string{"id": id})
	}
}

// DeleteScheduleDate — DELETE /api/v1/nightly/dates/{id}
func (h *Handler) DeleteScheduleDate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !collectorIDRE.MatchString(id) {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	p, _ := portalauth.From(r.Context())
	found := false
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var date, room string
		var closed bool
		err := tx.QueryRow(ctx, `
			DELETE FROM schedule_date WHERE id = $1
			RETURNING on_date::text, COALESCE(room_id::text, ''), closed`, id).Scan(&date, &room, &closed)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "nightly.date_removed", TargetKind: "schedule_date", TargetID: id,
			Before: map[string]any{"date": date, "room_id": room, "closed": closed},
		}))
	})
	if !ok {
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "date not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
