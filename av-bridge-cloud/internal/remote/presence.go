package remote

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Tunnels end on the worker copy, but the portal API runs on web copies, so
// which collectors are connected is recorded in remote_tunnel (migration
// 0080) where every copy can read it.

// SetPresence makes the tunnels record their connects and closes in the
// database. Without it (tests, tools) presence is in memory only.
func (t *Tunnels) SetPresence(pool *pgxpool.Pool) { t.presence = pool }

// recordConnect upserts the collector's row and returns its timestamp, which
// recordClose needs. Failures are logged and never fail the tunnel; the zero
// time means nothing was recorded.
func (t *Tunnels) recordConnect(ctx context.Context, id string) time.Time {
	if t.presence == nil {
		return time.Time{}
	}
	var at time.Time
	err := t.presence.QueryRow(ctx, `
		INSERT INTO remote_tunnel (collector_id, connected_at) VALUES ($1, now())
		ON CONFLICT (collector_id) DO UPDATE SET connected_at = now()
		RETURNING connected_at`, id).Scan(&at)
	if err != nil {
		t.log.Warn("remote access: recording tunnel presence failed", "collector", id, "error", err)
		return time.Time{}
	}
	return at
}

// recordClose removes the row recordConnect wrote. A reconnect's newer row
// has a different timestamp, so it survives.
func (t *Tunnels) recordClose(ctx context.Context, id string, at time.Time) {
	if t.presence == nil || at.IsZero() {
		return
	}
	if _, err := t.presence.Exec(ctx,
		`DELETE FROM remote_tunnel WHERE collector_id = $1 AND connected_at = $2`, id, at); err != nil {
		t.log.Warn("remote access: clearing tunnel presence failed", "collector", id, "error", err)
	}
}

// ClearPresence deletes every row. The tunnel-serving copy calls it at
// startup: tunnels don't survive a restart, so any row is stale.
func ClearPresence(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `DELETE FROM remote_tunnel`)
	return err
}

// TunnelConnected reports whether the collector (machine id) has a tunnel
// open on the tunnel-serving copy. Any copy can ask.
func TunnelConnected(ctx context.Context, pool *pgxpool.Pool, collectorID string) (bool, error) {
	var ok bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM remote_tunnel WHERE collector_id = $1)`, collectorID).Scan(&ok)
	return ok, err
}

// liveIDs lists the collectors with an open tunnel in this copy's map.
func (t *Tunnels) liveIDs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]string, 0, len(t.sessions))
	for id, s := range t.sessions {
		if !s.IsClosed() {
			ids = append(ids, id)
		}
	}
	return ids
}

// reconcilePresence makes remote_tunnel match the live sessions: rows for
// collectors with no live session go (an old worker exited mid-deploy
// without closing its tunnels), and live sessions with no row get one (a
// reconnect's close removed it, or a write failed). Valid because exactly one
// copy serves tunnels per environment; only that copy's Sweep runs. A tunnel
// connecting during the pass may lose its row until the next sweep.
func (t *Tunnels) reconcilePresence(ctx context.Context) error {
	if t.presence == nil {
		return nil
	}
	ids := t.liveIDs()
	if _, err := t.presence.Exec(ctx,
		`DELETE FROM remote_tunnel WHERE NOT (collector_id::text = ANY($1))`, ids); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	_, err := t.presence.Exec(ctx, `
		INSERT INTO remote_tunnel (collector_id)
		SELECT c.id FROM collectors c WHERE c.id::text = ANY($1)
		ON CONFLICT (collector_id) DO NOTHING`, ids)
	return err
}
