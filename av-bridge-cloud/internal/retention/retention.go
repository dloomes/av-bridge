// Package retention deletes records once they're past the periods the
// Service Description commits to (migration 0061 lists them). Telemetry has
// its own job (internal/telemetry). Deletes run in small batches, a few
// minutes an hour at most, so the clean-up never competes with the platform.
package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	Interval   = time.Hour
	batch      = 5_000
	passBudget = 3 * time.Minute
	lockKey    = 0x7e1e_0061 // pg advisory lock: one pass at a time across cloud tasks
)

// Rule is one kind of record and how long it's kept. Where selects, from
// alias t, the rows that are past it; ctid identifies them for the batch.
type Rule struct {
	Name  string
	Table string
	Where string
}

// Rules are the retention periods, as published.
var Rules = []Rule{
	{"audit log (24 months)", "audit_log",
		`t.ts < now() - interval '24 months'`},
	{"resolved alerts (12 months after resolution)", "alerts",
		`t.status = 'resolved' AND t.resolved_at < now() - interval '12 months'`},
	{"device events (90 days)", "events",
		`t.ts < now() - interval '90 days'`},
	{"command history (90 days after finishing)", "commands",
		`t.status IN ('succeeded', 'failed', 'cancelled') AND t.submitted_at < now() - interval '90 days'`},
	{"Room Readiness runs (per-customer setting)", "nightly_run",
		`t.status NOT IN ('pending', 'in_progress')
		 AND t.scheduled_at < now() - make_interval(days => COALESCE(
		     (SELECT s.retention_days FROM nightly_schedule s WHERE s.customer_id = t.customer_id), 90))`},
	{"remote access sessions (90 days after ending)", "remote_session",
		`t.ended_at < now() - interval '90 days'`},
}

// Service runs retention passes.
type Service struct {
	pool *pgxpool.Pool // admin pool: works across every organisation
	log  *slog.Logger
}

func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{pool: pool, log: log}
}

// Run makes a pass every Interval until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.log.Info("retention clean-up started", "rules", len(Rules))
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		if err := s.Pass(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("retention pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Pass applies every rule, within passBudget; anything left is picked up
// on the next pass.
func (s *Service) Pass(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&got); err != nil || !got {
		return err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockKey)

	deadline := time.Now().Add(passBudget)
	for _, r := range Rules {
		n, err := Apply(ctx, conn.Conn(), r, deadline)
		if err != nil {
			return err
		}
		if n > 0 {
			s.log.Info("retention: deleted", "records", r.Name, "count", n)
		}
	}
	return nil
}

// Apply deletes r's expired rows in batches until none are left or the
// deadline passes, and returns how many it deleted. Exposed for tests.
func Apply(ctx context.Context, conn *pgx.Conn, r Rule, deadline time.Time) (int64, error) {
	var total int64
	for time.Now().Before(deadline) {
		tag, err := conn.Exec(ctx, `
			DELETE FROM `+r.Table+`
			 WHERE ctid = ANY (ARRAY(SELECT t.ctid FROM `+r.Table+` t WHERE `+r.Where+` LIMIT $1))`, batch)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < batch {
			break
		}
	}
	return total, nil
}
