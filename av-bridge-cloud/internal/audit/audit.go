// Package audit writes the cloud-wide audit log. Every portal-driven mutation
// calls Record inside the same tenant tx that performed the change, so the
// audit insert succeeds or rolls back atomically with the write — there's no
// way to mutate a row without leaving an audit trail (short of bypassing the
// portal layer entirely).
//
// Credential columns (username_enc, password_enc) MUST be excluded from
// snapshots by the caller. Audit is read by every operator with portal
// access; even ciphertext doesn't belong in that surface.
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Entry is one audited mutation. Before is nil for create, After is nil for
// delete, both populated for update. TargetID may be empty for actions that
// operate on collections (rare; today every portal write has a concrete
// target).
//
// RelatedTargetKind / RelatedTargetID let one action cross-reference a
// secondary entity — e.g. command.submit has target=command, related=device,
// so the device's activity feed surfaces the command without needing
// metadata-JSON lookups.
//
// ActorRole / ActorScope / ActorIsVendor snapshot the caller's authorization
// state at the moment of the action. Roles and scope can drift after the fact
// (a helpdesk promotion, a widened scope) — freezing them here keeps the
// trail forensics-ready. Leave zero for legacy paths with no principal
// context; the columns are nullable.
type Entry struct {
	Actor             string
	Action            string
	TargetKind        string
	TargetID          string
	RelatedTargetKind string
	RelatedTargetID   string
	Before            any
	After             any
	Metadata          map[string]any
	ActorRole         string
	ActorScope        []string
	ActorIsVendor     bool
}

// Record inserts the audit row inside the caller's tx. The session variable
// app.current_customer is already set by Store.WithTenant, so the INSERT is
// covered by the same RLS policy as the change it accompanies.
func Record(ctx context.Context, tx pgx.Tx, customerID string, e Entry) error {
	beforeParam, err := jsonOrNil(e.Before)
	if err != nil {
		return fmt.Errorf("audit before: %w", err)
	}
	afterParam, err := jsonOrNil(e.After)
	if err != nil {
		return fmt.Errorf("audit after: %w", err)
	}
	metaParam, err := jsonOrNil(e.Metadata)
	if err != nil {
		return fmt.Errorf("audit metadata: %w", err)
	}
	targetIDParam := nullIfEmpty(e.TargetID)
	relKindParam := nullIfEmpty(e.RelatedTargetKind)
	relIDParam := nullIfEmpty(e.RelatedTargetID)
	actorRoleParam := nullIfEmpty(e.ActorRole)
	// nil scope reads as "unknown / not stamped" (pre-slice-7 rows); an empty
	// slice reads as "explicitly unscoped, full-tenant access". Preserve the
	// distinction rather than coalescing both to NULL.
	var actorScopeParam any
	if e.ActorScope != nil {
		actorScopeParam = e.ActorScope
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_log (
			customer_id, actor, action, target_kind, target_id,
			related_target_kind, related_target_id,
			before, "after", metadata,
			actor_role, actor_scope, actor_is_vendor
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7,
			$8::jsonb, $9::jsonb, $10::jsonb,
			$11, $12, $13
		)`,
		customerID, e.Actor, e.Action, e.TargetKind, targetIDParam,
		relKindParam, relIDParam,
		beforeParam, afterParam, metaParam,
		actorRoleParam, actorScopeParam, e.ActorIsVendor)
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SnapshotDevice returns the safe-to-audit fields of a device as a raw
// jsonb-shaped string (or nil for a row that doesn't exist). Used as
// before/after by the device CRUD handlers. Excludes username_enc /
// password_enc so audit can't leak credential ciphertext.
func SnapshotDevice(ctx context.Context, tx pgx.Tx, id string) (json.RawMessage, error) {
	var snap json.RawMessage
	err := tx.QueryRow(ctx, `
		SELECT row_to_json(t) FROM (
			SELECT id::text, collector_id::text, room_id::text, reported_id,
			       name, type, protocol, address, baud_rate,
			       poll_rate_seconds, commands, tags, subscriptions
			  FROM devices WHERE id = $1
		) t`, id).Scan(&snap)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return snap, err
}

// SnapshotByTable returns row_to_json for a row in a simple hierarchy table
// (regions, locations, buildings, rooms). The table name is interpolated
// directly — restrict callers to known constants, never user input.
func SnapshotByTable(ctx context.Context, tx pgx.Tx, table, id string) (json.RawMessage, error) {
	allowed := map[string]bool{
		"regions": true, "locations": true, "buildings": true, "rooms": true,
		"commands": true,
	}
	if !allowed[table] {
		return nil, fmt.Errorf("snapshot: table %q not in audit allowlist", table)
	}
	var snap json.RawMessage
	err := tx.QueryRow(ctx,
		`SELECT row_to_json(t) FROM `+table+` t WHERE id = $1`, id).Scan(&snap)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return snap, err
}

func jsonOrNil(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	// json.RawMessage marshals to itself — saves a round-trip when callers
	// already have a row_to_json result in hand.
	if raw, ok := v.(json.RawMessage); ok {
		if len(raw) == 0 || string(raw) == "null" {
			return nil, nil
		}
		return string(raw), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if string(b) == "null" {
		return nil, nil
	}
	return string(b), nil
}
