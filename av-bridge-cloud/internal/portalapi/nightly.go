package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// Nightly Room Readiness — schedule CRUD.
//
// Slice 1 of Phase A. Covers the customer-level schedule row only; per-room
// override endpoints and recipe CRUD land in subsequent slices. See
// docs/nightly-lifecycle-spec.md for the full design.
//
// One row per customer, auto-provisioned with sensible defaults on first
// read so the portal never has to distinguish "schedule not created yet"
// from "schedule with default values" — both look the same to the UI.

// scheduleOut is the wire shape for GET /api/v1/nightly/schedule.
type scheduleOut struct {
	PowerOffTime  string `json:"power_off_time"`  // HH:MM
	PowerOnTime   string `json:"power_on_time"`   // HH:MM
	DaysOfWeek    []int  `json:"days_of_week"`    // ISO 1-7
	Timezone      string `json:"timezone"`        // IANA
	TestRecipeID  string `json:"test_recipe_id,omitempty"`
	HelpdeskEmail string `json:"helpdesk_email,omitempty"`
	RetentionDays int    `json:"retention_days"`
	Enabled       bool   `json:"enabled"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

// GetNightlySchedule — GET /api/v1/nightly/schedule
//
// Returns the caller's customer nightly-schedule row, creating one with
// defaults if none exists. Gated on nightly.view at the route layer.
func (h *Handler) GetNightlySchedule(w http.ResponseWriter, r *http.Request) {
	var out scheduleOut
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		row, err := loadSchedule(ctx, tx)
		if errors.Is(err, pgx.ErrNoRows) {
			// Auto-provision default row on first read so the portal doesn't
			// need a separate "create schedule" flow.
			if err := insertDefaultSchedule(ctx, tx); err != nil {
				return err
			}
			row, err = loadSchedule(ctx, tx)
		}
		if err != nil {
			return err
		}
		out = row
		return nil
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// updateNightlyScheduleReq — pointer-per-field so absent fields are
// distinguishable from "clear to null". Not every field is nullable in the
// DB, but this shape keeps the client-side experience uniform.
type updateNightlyScheduleReq struct {
	PowerOffTime  *string `json:"power_off_time,omitempty"`
	PowerOnTime   *string `json:"power_on_time,omitempty"`
	DaysOfWeek    *[]int  `json:"days_of_week,omitempty"`
	Timezone      *string `json:"timezone,omitempty"`
	TestRecipeID  *string `json:"test_recipe_id,omitempty"`
	HelpdeskEmail *string `json:"helpdesk_email,omitempty"`
	RetentionDays *int    `json:"retention_days,omitempty"`
	Enabled       *bool   `json:"enabled,omitempty"`
}

// UpdateNightlySchedule — PATCH /api/v1/nightly/schedule
//
// Updates the caller's customer schedule row. Auto-provisions the row if
// missing (same as GET). Gated on nightly.manage at the route layer.
func (h *Handler) UpdateNightlySchedule(w http.ResponseWriter, r *http.Request) {
	var req updateNightlyScheduleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Validate up front so we return 400 with a specific message rather
	// than 500 from a constraint violation.
	if req.PowerOffTime != nil {
		if !isHHMM(*req.PowerOffTime) {
			writeErr(w, http.StatusBadRequest, "power_off_time must be HH:MM")
			return
		}
	}
	if req.PowerOnTime != nil {
		if !isHHMM(*req.PowerOnTime) {
			writeErr(w, http.StatusBadRequest, "power_on_time must be HH:MM")
			return
		}
	}
	if req.PowerOffTime != nil && req.PowerOnTime != nil && *req.PowerOffTime == *req.PowerOnTime {
		writeErr(w, http.StatusBadRequest, "power_off_time and power_on_time must differ")
		return
	}
	if req.DaysOfWeek != nil {
		for _, d := range *req.DaysOfWeek {
			if d < 1 || d > 7 {
				writeErr(w, http.StatusBadRequest, "days_of_week must contain ISO weekday values 1-7")
				return
			}
		}
	}
	if req.Timezone != nil {
		if _, err := time.LoadLocation(*req.Timezone); err != nil {
			writeErr(w, http.StatusBadRequest, "timezone must be a valid IANA name")
			return
		}
	}
	if req.RetentionDays != nil && *req.RetentionDays < 30 {
		writeErr(w, http.StatusBadRequest, "retention_days must be at least 30")
		return
	}
	if req.HelpdeskEmail != nil && *req.HelpdeskEmail != "" {
		if !strings.Contains(*req.HelpdeskEmail, "@") {
			writeErr(w, http.StatusBadRequest, "helpdesk_email is not a valid address")
			return
		}
	}

	p, _ := portalauth.From(r.Context())

	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		// Ensure a row exists before we UPDATE so a first-time save doesn't
		// silently no-op.
		if _, err := loadSchedule(ctx, tx); errors.Is(err, pgx.ErrNoRows) {
			if err := insertDefaultSchedule(ctx, tx); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}

		// Build dynamic UPDATE. RLS restricts to caller's tenant so no
		// WHERE customer_id clause is required — but include it as belt +
		// braces so a bug in the resolver can't silently update another
		// tenant's row.
		set := []string{}
		args := []any{p.CustomerID}
		add := func(col string, val any) {
			args = append(args, val)
			set = append(set, fmt.Sprintf("%s = $%d", col, len(args)))
		}
		if req.PowerOffTime != nil {
			add("power_off_time", *req.PowerOffTime)
		}
		if req.PowerOnTime != nil {
			add("power_on_time", *req.PowerOnTime)
		}
		if req.DaysOfWeek != nil {
			add("days_of_week", *req.DaysOfWeek)
		}
		if req.Timezone != nil {
			add("timezone", *req.Timezone)
		}
		if req.TestRecipeID != nil {
			if *req.TestRecipeID == "" {
				add("test_recipe_id", nil)
			} else {
				add("test_recipe_id", *req.TestRecipeID)
			}
		}
		if req.HelpdeskEmail != nil {
			if *req.HelpdeskEmail == "" {
				add("helpdesk_email", nil)
			} else {
				add("helpdesk_email", *req.HelpdeskEmail)
			}
		}
		if req.RetentionDays != nil {
			add("retention_days", *req.RetentionDays)
		}
		if req.Enabled != nil {
			add("enabled", *req.Enabled)
		}
		if len(set) == 0 {
			return nil
		}
		sql := "UPDATE nightly_schedule SET " + strings.Join(set, ", ") +
			" WHERE customer_id = $1"
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return err
		}

		// Audit — capture the requested changes (not the full row).
		payload := map[string]any{}
		if req.PowerOffTime != nil {
			payload["power_off_time"] = *req.PowerOffTime
		}
		if req.PowerOnTime != nil {
			payload["power_on_time"] = *req.PowerOnTime
		}
		if req.DaysOfWeek != nil {
			payload["days_of_week"] = *req.DaysOfWeek
		}
		if req.Timezone != nil {
			payload["timezone"] = *req.Timezone
		}
		if req.TestRecipeID != nil {
			payload["test_recipe_id"] = *req.TestRecipeID
		}
		if req.HelpdeskEmail != nil {
			// Log presence, not value — helpdesk email is arguably PII.
			if *req.HelpdeskEmail == "" {
				payload["helpdesk_email"] = "cleared"
			} else {
				payload["helpdesk_email"] = "set"
			}
		}
		if req.RetentionDays != nil {
			payload["retention_days"] = *req.RetentionDays
		}
		if req.Enabled != nil {
			payload["enabled"] = *req.Enabled
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "nightly.schedule.update",
			TargetKind: "customer", TargetID: p.CustomerID,
			After: mustJSON(payload),
		}))
	})
	if !ok {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// loadSchedule returns the caller's schedule row. Assumes we're already
// inside a tenant-scoped tx so RLS handles the customer_id filter — no
// explicit filter here so the query stays trivial.
func loadSchedule(ctx context.Context, tx pgx.Tx) (scheduleOut, error) {
	var (
		out          scheduleOut
		powerOff     time.Time
		powerOn      time.Time
		days         []int32
		recipeID     *string
		helpdeskAddr *string
		updatedAt    time.Time
	)
	err := tx.QueryRow(ctx, `
		SELECT power_off_time, power_on_time, days_of_week, timezone,
		       test_recipe_id, helpdesk_email, retention_days, enabled,
		       updated_at
		  FROM nightly_schedule LIMIT 1
	`).Scan(
		&powerOff, &powerOn, &days, &out.Timezone,
		&recipeID, &helpdeskAddr, &out.RetentionDays, &out.Enabled,
		&updatedAt,
	)
	if err != nil {
		return out, err
	}
	// Postgres `time` scans as time.Time with a zero date. Format HH:MM.
	out.PowerOffTime = powerOff.Format("15:04")
	out.PowerOnTime = powerOn.Format("15:04")
	out.DaysOfWeek = make([]int, len(days))
	for i, d := range days {
		out.DaysOfWeek[i] = int(d)
	}
	if recipeID != nil {
		out.TestRecipeID = *recipeID
	}
	if helpdeskAddr != nil {
		out.HelpdeskEmail = *helpdeskAddr
	}
	out.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	return out, nil
}

// insertDefaultSchedule creates the customer's row using DB defaults.
// Idempotent-ish: unique(customer_id) means a concurrent create races to a
// unique-violation, which the caller can retry via loadSchedule.
func insertDefaultSchedule(ctx context.Context, tx pgx.Tx) error {
	p, _ := portalauth.From(ctx)
	_, err := tx.Exec(ctx, `
		INSERT INTO nightly_schedule (customer_id) VALUES ($1)
		ON CONFLICT (customer_id) DO NOTHING
	`, p.CustomerID)
	return err
}

// isHHMM checks the HH:MM shape. Anything Go's time.Parse would accept
// with layout 15:04 is fine; anything else rejects with a 400.
func isHHMM(s string) bool {
	_, err := time.Parse("15:04", s)
	return err == nil
}
