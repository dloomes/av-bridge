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
// bridge can't trap a command in an infinite loop. It also expires commands
// still pending pendingExpireAfter after submission (their collector never
// picked them up), so they can't run long after the operator gave up.
//
// Runs as app_admin (BYPASSRLS) because it operates across all tenants and
// isn't tied to a request-bound session.
type Sweeper struct {
	pool       *pgxpool.Pool
	interval   time.Duration
	staleAfter time.Duration
	maxClaims  int
	// pendingExpireAfter <= 0 disables pending expiry.
	pendingExpireAfter time.Duration
	log                *slog.Logger
}

func NewSweeper(pool *pgxpool.Pool, interval, staleAfter time.Duration, maxClaims int, pendingExpireAfter time.Duration, log *slog.Logger) *Sweeper {
	return &Sweeper{
		pool:               pool,
		interval:           interval,
		staleAfter:         staleAfter,
		maxClaims:          maxClaims,
		pendingExpireAfter: pendingExpireAfter,
		log:                log,
	}
}

// Run blocks until ctx is cancelled. Sweeps once per interval.
func (s *Sweeper) Run(ctx context.Context) {
	if s.interval <= 0 || s.staleAfter <= 0 {
		s.log.Warn("command sweeper disabled (interval or stale_after non-positive)")
		return
	}
	s.log.Info("command sweeper started",
		"interval", s.interval, "stale_after", s.staleAfter, "max_claims", s.maxClaims,
		"pending_expire_after", s.pendingExpireAfter)
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

// Sweep runs one expire+fail+requeue pass. Exposed for tests; Run calls it
// on a tick. Fails first, then requeues — order matters so a command that
// just crossed maxClaims doesn't get re-pended in the same pass it should be
// failed in.
func (s *Sweeper) Sweep(ctx context.Context) {
	s.expirePending(ctx)

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

// expirePending fails pending commands older than pendingExpireAfter with
// error 'expired', then NOTIFYs cmd_done for each so a portal request or
// nightly routine still waiting on one wakes immediately. A command that
// was claimed and requeued keeps its original submitted_at, so it expires
// on the same clock.
func (s *Sweeper) expirePending(ctx context.Context) {
	if s.pendingExpireAfter <= 0 {
		return
	}
	rows, err := s.pool.Query(ctx, `
		UPDATE commands
		   SET status = 'failed',
		       error = 'expired',
		       completed_at = now()
		 WHERE status = 'pending'
		   AND submitted_at < now() - make_interval(secs => $1)
		RETURNING id::text`,
		int(s.pendingExpireAfter.Seconds()))
	if err != nil {
		s.log.Warn("sweeper expire-step error", "error", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			s.log.Warn("sweeper expire scan error", "error", err)
			return
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		s.log.Warn("sweeper expire iterate error", "error", err)
		return
	}
	for _, id := range ids {
		if _, err := s.pool.Exec(ctx, `SELECT pg_notify($1, $2)`, ChannelDone, id); err != nil {
			s.log.Warn("sweeper expire notify error", "command", id, "error", err)
		}
	}
	if len(ids) > 0 {
		s.log.Info("sweeper expired unclaimed commands",
			"count", len(ids), "pending_expire_after", s.pendingExpireAfter)
	}
}
