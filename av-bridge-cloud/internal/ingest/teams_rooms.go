package ingest

// Teams Rooms on Windows read by a Collector (protocol mtr_windows) are
// alerted on by the integrations Alerter, the same as Poly Lens rooms: one
// owner for their offline, signed-out, unhealthy and peripheral alerts.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// teamsRoomState is a Collector Teams Room's latest stored state, read in the
// ingest transaction and evaluated after it commits.
type teamsRoomState struct {
	id      string
	status  string
	metrics map[string]any
}

// isCollectorTeamsRoomSQL: type is the portal's, so it decides, not the
// type a Collector reports.
const isCollectorTeamsRoomSQL = `type = 'teams_room' AND source = 'collector' AND deleted_at IS NULL`

// alerterOwnsEvent reports whether an event is a Collector alert the Teams
// Room alerter owns for Teams Room devices. The Collector only sends
// device_offline and device_recovered.
func alerterOwnsEvent(eventType string) bool {
	return eventType == "alert:device_offline" || eventType == "alert:device_recovered"
}

// teamsRoomCache remembers, per push, which devices are Collector Teams Rooms.
type teamsRoomCache map[string]bool

func (c teamsRoomCache) is(ctx context.Context, tx pgx.Tx, deviceID string) (bool, error) {
	if v, ok := c[deviceID]; ok {
		return v, nil
	}
	var v bool
	err := tx.QueryRow(ctx, `SELECT `+isCollectorTeamsRoomSQL+` FROM devices WHERE id = $1`, deviceID).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return false, err
	}
	c[deviceID] = v
	return v, nil
}

// loadTeamsRoomStates returns the latest state of those devices that are
// Collector Teams Rooms. The stored state, not the push's telemetry, so a
// replayed older reading never drives the alerts backwards.
func loadTeamsRoomStates(ctx context.Context, tx pgx.Tx, deviceIDs []string) ([]teamsRoomState, error) {
	if len(deviceIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, COALESCE(latest_status, ''), COALESCE(latest_metrics, '{}'::jsonb)
		  FROM devices WHERE id = ANY($1::uuid[]) AND `+isCollectorTeamsRoomSQL, deviceIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []teamsRoomState
	for rows.Next() {
		var s teamsRoomState
		var raw []byte
		if err := rows.Scan(&s.id, &s.status, &raw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &s.metrics)
		out = append(out, s)
	}
	return out, rows.Err()
}
