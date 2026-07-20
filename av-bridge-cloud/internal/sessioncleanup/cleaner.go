package sessioncleanup

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Cleaner deletes long-expired and long-revoked portal sessions from
// user_sessions. Rows are kept for `retention` past the moment they
// became non-functional (either expires_at passed or revoked_at was set)
// so a future "recent sessions" audit view has something to render.
//
// Runs as app_admin (BYPASSRLS). user_sessions is deliberately outside
// RLS — login has to look the row up before knowing which tenant the
// user belongs to — so this cleaner operates cross-tenant, mirroring the
// local auth resolver's access pattern.
//
// Expired sessions are already non-functional at lookup time (the
// resolver query filters `expires_at > now() AND revoked_at IS NULL`),
// so this is a housekeeping job — not a security fix. Left unchecked
// the table grows unbounded and its expires_at index bloats.
type Cleaner struct {
	pool      *pgxpool.Pool
	interval  time.Duration
	retention time.Duration
	log       *slog.Logger
}

func NewCleaner(pool *pgxpool.Pool, interval, retention time.Duration, log *slog.Logger) *Cleaner {
	return &Cleaner{pool: pool, interval: interval, retention: retention, log: log}
}

// Run blocks until ctx is cancelled. Sweeps once per interval; the first
// sweep fires after one interval, keeping the startup path DB-cheap.
func (c *Cleaner) Run(ctx context.Context) {
	if c.interval <= 0 || c.retention <= 0 {
		c.log.Warn("session cleaner disabled (interval or retention non-positive)",
			"interval", c.interval, "retention", c.retention)
		return
	}
	c.log.Info("session cleaner started",
		"interval", c.interval, "retention", c.retention)
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Sweep(ctx)
		}
	}
}

// Sweep runs one delete pass. Exposed so tests can drive it directly
// rather than waiting on a ticker.
func (c *Cleaner) Sweep(ctx context.Context) {
	retSecs := int(c.retention.Seconds())
	// LEAST(expires_at, revoked_at) picks the earliest moment the row
	// became non-functional. Postgres LEAST ignores NULLs, so a session
	// that was never revoked falls back to expires_at alone; a session
	// revoked before its natural expiry uses revoked_at. Anything past
	// (that moment + retention) is safe to delete.
	res, err := c.pool.Exec(ctx, `
		DELETE FROM user_sessions
		 WHERE LEAST(expires_at, revoked_at) < now() - make_interval(secs => $1)`,
		retSecs)
	if err != nil {
		c.log.Warn("session cleanup error", "error", err)
		return
	}
	if n := res.RowsAffected(); n > 0 {
		c.log.Info("session cleanup deleted stale sessions", "count", n)
	}
}
