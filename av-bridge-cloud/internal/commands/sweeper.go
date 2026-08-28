package commands

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Sweeper rescues commands stuck in_progress past the stale-after threshold:
// requeues them back to pending so the next bridge poll picks them up, and
// marks them failed once claim_count >= maxClaims so a flapping or absent
// bridge can't trap a command in an infinite loop.
//
// Runs as app_admin (BYPASSRLS) because it operates across all tenants and
// isn't tied to a request-bound session.
type Sweeper struct {
	pool       *pgxpool.Pool
	interval   time.Duration
	staleAfter time.Duration
	maxClaims  int
	log        *slog.Logger
}

func NewSweeper(pool *pgxpool.Pool, interval, staleAfter time.Duration, maxClaims int, log *slog.Logger) *Sweeper {
	return &Sweeper{
		pool:       pool,
		interval:   interval,
		staleAfter: staleAfter,
		maxClaims:  maxClaims,
		log:        log,
	}
}

// Run blocks until ctx is cancelled. Sweeps once per interval.
func (s *Sweeper) Run(ctx context.Context) {
	if s.interval <= 0 || s.staleAfter <= 0 {
		s.log.Warn("command sweeper disabled (interval or stale_after non-positive)")
		return
	}
	s.log.Info("command sweeper started",
		"interval", s.interval, "stale_after", s.staleAfter, "max_claims", s.maxClaims)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sweep(ctx)
		}
	}
}

// Sweep runs one fail+requeue pass. Exposed for tests; Run calls it on a tick.
// Fails first, then requeues — order matters so a command that just crossed
// maxClaims doesn't get re-pended in the same pass it should be failed in.
func (s *Sweeper) Sweep(ctx context.Context) {
	staleSecs := int(s.staleAfter.Seconds())

	failed, err := s.pool.Exec(ctx, `
		UPDATE commands
		   SET status = 'failed',
		       error = 'bridge_timeout',
		       completed_at = now()
		 WHERE status = 'in_progress'
		   AND claimed_at < now() - make_interval(secs => $1)
		   AND claim_count >= $2`,
		staleSecs, s.maxClaims)
	if err != nil {
		s.log.Warn("sweeper fail-step error", "error", err)
		return
	}
	if n := failed.RowsAffected(); n > 0 {
		s.log.Info("sweeper failed stale commands past max claims",
			"count", n, "max_claims", s.maxClaims)
	}

	// RETURNING collector_id so we can NOTIFY per-collector once the
	// implicit tx commits. Without this, a requeued row would sit until
	// the next unrelated cmd_pending wake or the bridge's fallback
	// re-poll — reintroducing the very latency the long-poll removes.
	rows, err := s.pool.Query(ctx, `
		UPDATE commands
		   SET status = 'pending',
		       claimed_at = NULL
		 WHERE status = 'in_progress'
		   AND claimed_at < now() - make_interval(secs => $1)
		   AND claim_count < $2
		RETURNING collector_id::text`,
		staleSecs, s.maxClaims)
	if err != nil {
		s.log.Warn("sweeper requeue-step error", "error", err)
		return
	}
	rowCount := 0
	collectors := make(map[string]struct{})
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			rows.Close()
			s.log.Warn("sweeper requeue scan error", "error", err)
			return
		}
		collectors[cid] = struct{}{}
		rowCount++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		s.log.Warn("sweeper requeue iterate error", "error", err)
		return
	}
	if rowCount > 0 {
		// One NOTIFY per affected collector — dedupes duplicate wake-ups
		// when a sweep requeues multiple rows for the same bridge.
		// Failure to notify is warn-only: the sweep already committed,
		// and the bridge's fallback pace still picks them up eventually.
		for cid := range collectors {
			if _, err := s.pool.Exec(ctx, `SELECT pg_notify($1, $2)`, ChannelPending, cid); err != nil {
				s.log.Warn("sweeper notify error", "collector", cid, "error", err)
			}
		}
		s.log.Info("sweeper requeued stale in-progress commands", "count", rowCount)
	}
}
