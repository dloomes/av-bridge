package pubapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// publicAlert — customer-facing alert shape. Elides the portal
// internals (acknowledged_by / resolved_by email addresses, actor
// context) — those are staff identifiers that shouldn't leak through
// a customer API. status + timestamps are what an integrating system
// actually consumes (route to on-call, update a downstream ticket).
type publicAlert struct {
	ID             string          `json:"id"`
	SubjectKind    string          `json:"subject_kind"`
	DeviceID       string          `json:"device_id,omitempty"`
	DeviceName     string          `json:"device_name,omitempty"`
	CollectorID    string          `json:"collector_id,omitempty"`
	CollectorName  string          `json:"collector_name,omitempty"`
	AlertKey       string          `json:"alert_key"`
	Severity       string          `json:"severity"`
	Message        string          `json:"message"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	Status         string          `json:"status"`
	OpenedAt       time.Time       `json:"opened_at"`
	AcknowledgedAt *time.Time      `json:"acknowledged_at,omitempty"`
	ResolvedAt     *time.Time      `json:"resolved_at,omitempty"`
}

// ListAlerts — GET /pub/v1/alerts
//
// Filters (all optional):
//   status    = open | acknowledged | resolved   exact match
//   severity  = critical | warning | info         exact match
//   cursor + limit
//
// Requires view.dashboard scope. Ordered by (opened_at, id) desc so
// the head of the list is the most recent alert regardless of status.
func (h *Handler) ListAlerts(w http.ResponseWriter, r *http.Request) {
	cursor, err := ParseCursor(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, ErrInvalidCursor.Error())
		return
	}
	limit := ParseLimit(r)

	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	sevFilter := strings.TrimSpace(r.URL.Query().Get("severity"))

	sql := `
		SELECT a.id::text,
		       CASE WHEN a.collector_id IS NOT NULL THEN 'collector' ELSE 'device' END,
		       COALESCE(a.device_id::text, ''),
		       COALESCE(d.name, d.reported_id, ''),
		       COALESCE(a.collector_id::text, ''),
		       COALESCE(c.name, ''),
		       a.alert_key, a.severity, a.message, a.payload, a.status,
		       a.opened_at, a.acknowledged_at, a.resolved_at
		  FROM alerts a
		  LEFT JOIN devices d    ON d.id = a.device_id
		  LEFT JOIN collectors c ON c.id = a.collector_id
		 WHERE 1=1`
	args := []any{}
	next := func() string { return "$" + itoa(len(args)+1) }

	if statusFilter != "" {
		args = append(args, statusFilter)
		sql += " AND a.status = " + next()
	}
	if sevFilter != "" {
		args = append(args, sevFilter)
		sql += " AND a.severity = " + next()
	}
	if cursor.TS != nil {
		args = append(args, *cursor.TS, cursor.ID)
		sql += " AND (a.opened_at, a.id::text) < (" + next()
		sql += ", $" + itoa(len(args)) + ")"
	}
	args = append(args, limit+1)
	sql += " ORDER BY a.opened_at DESC, a.id::text DESC LIMIT " + next()

	out := []publicAlert{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a publicAlert
			if err := rows.Scan(
				&a.ID, &a.SubjectKind,
				&a.DeviceID, &a.DeviceName,
				&a.CollectorID, &a.CollectorName,
				&a.AlertKey, &a.Severity, &a.Message, &a.Payload, &a.Status,
				&a.OpenedAt, &a.AcknowledgedAt, &a.ResolvedAt,
			); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	var nextCursor *string
	if len(out) > limit {
		last := out[limit-1]
		nc := EncodeCursor(Cursor{TS: &last.OpenedAt, ID: last.ID})
		nextCursor = &nc
		out = out[:limit]
	}
	writeJSON(w, http.StatusOK, Page(out, nextCursor))
}
