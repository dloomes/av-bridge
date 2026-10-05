package pubapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// publicAuditEntry is one row of the tenant's audit trail. before/after
// are snapshots of the changed record; secrets (device credentials,
// password hashes, token hashes) are excluded when the entry is written,
// so nothing here needs redacting on the way out.
type publicAuditEntry struct {
	ID                string          `json:"id"`
	Timestamp         time.Time       `json:"timestamp"`
	Actor             string          `json:"actor"`
	Action            string          `json:"action"`
	TargetKind        string          `json:"target_kind"`
	TargetID          string          `json:"target_id,omitempty"`
	RelatedTargetKind string          `json:"related_target_kind,omitempty"`
	RelatedTargetID   string          `json:"related_target_id,omitempty"`
	Before            json.RawMessage `json:"before,omitempty"`
	After             json.RawMessage `json:"after,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

// ListAudit — GET /pub/v1/audit
//
// Filters (all optional):
//
//	action       exact action, e.g. device.update; or a prefix ending in
//	             "." (device.) for every action on that kind of record
//	target_kind  exact, e.g. device, user, notification_channel
//	target_id    matches the primary or the related target, so a device's
//	             feed includes commands sent to it (as in the portal)
//	actor        exact actor label
//	since/until  RFC 3339; since is inclusive, until exclusive
//	cursor+limit standard pagination
//
// Requires the view.audit scope. Ordered by (ts, id) desc. Tenant scoping
// comes from RLS on audit_log, as everywhere else in the public API.
func (h *Handler) ListAudit(w http.ResponseWriter, r *http.Request) {
	cursor, err := ParseCursor(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, ErrInvalidCursor.Error())
		return
	}
	var cursorID int64
	if cursor.TS != nil {
		if cursorID, err = strconv.ParseInt(cursor.ID, 10, 64); err != nil {
			writeErr(w, http.StatusBadRequest, ErrInvalidCursor.Error())
			return
		}
	}
	limit := ParseLimit(r)
	qv := r.URL.Query()
	parseTime := func(name string) (*time.Time, bool) {
		v := strings.TrimSpace(qv.Get(name))
		if v == "" {
			return nil, true
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, name+" must be an RFC 3339 timestamp, e.g. 2026-10-01T00:00:00Z")
			return nil, false
		}
		return &t, true
	}
	since, ok := parseTime("since")
	if !ok {
		return
	}
	until, ok := parseTime("until")
	if !ok {
		return
	}

	sql := `
		SELECT a.id::text, a.ts, a.actor, a.action, a.target_kind,
		       COALESCE(a.target_id, ''),
		       COALESCE(a.related_target_kind, ''), COALESCE(a.related_target_id, ''),
		       a.before, a."after", a.metadata
		  FROM audit_log a
		 WHERE 1=1`
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + itoa(len(args))
	}

	if action := strings.TrimSpace(qv.Get("action")); action != "" {
		if strings.HasSuffix(action, ".") {
			sql += " AND starts_with(a.action, " + arg(action) + ")"
		} else {
			sql += " AND a.action = " + arg(action)
		}
	}
	targetKind := strings.TrimSpace(qv.Get("target_kind"))
	if targetID := strings.TrimSpace(qv.Get("target_id")); targetID != "" {
		idP := arg(targetID)
		if targetKind != "" {
			kindP := arg(targetKind)
			sql += " AND ((a.target_id = " + idP + " AND a.target_kind = " + kindP + ")" +
				" OR (a.related_target_id = " + idP + " AND a.related_target_kind = " + kindP + "))"
		} else {
			sql += " AND (a.target_id = " + idP + " OR a.related_target_id = " + idP + ")"
		}
	} else if targetKind != "" {
		sql += " AND a.target_kind = " + arg(targetKind)
	}
	if actor := strings.TrimSpace(qv.Get("actor")); actor != "" {
		sql += " AND a.actor = " + arg(actor)
	}
	if since != nil {
		sql += " AND a.ts >= " + arg(*since)
	}
	if until != nil {
		sql += " AND a.ts < " + arg(*until)
	}
	if cursor.TS != nil {
		tsP := arg(*cursor.TS)
		idP := arg(cursorID)
		sql += " AND (a.ts, a.id) < (" + tsP + ", " + idP + "::bigint)"
	}
	sql += " ORDER BY a.ts DESC, a.id DESC LIMIT " + arg(limit+1) + "::int"

	out := []publicAuditEntry{}
	ok = h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e publicAuditEntry
			if err := rows.Scan(&e.ID, &e.Timestamp, &e.Actor, &e.Action, &e.TargetKind,
				&e.TargetID, &e.RelatedTargetKind, &e.RelatedTargetID,
				&e.Before, &e.After, &e.Metadata); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	var next *string
	if len(out) > limit {
		last := out[limit-1]
		nc := EncodeCursor(Cursor{TS: &last.Timestamp, ID: last.ID})
		next = &nc
		out = out[:limit]
	}
	writeJSON(w, http.StatusOK, Page(out, next))
}
