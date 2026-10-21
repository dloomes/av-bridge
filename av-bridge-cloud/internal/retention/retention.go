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
// A rule with Func instead calls that function (customer, batch) once per
// organisation, for tables the app may not delete from directly.
type Rule struct {
	Name  string
	Table string
	Where string
	Func  string
}

// Rules are the retention periods, as published.
var Rules = []Rule{
	// audit_log is append-only for the app; migration 0062's function
	// deletes only entries past 24 months.
	{Name: "audit log (24 months)", Table: "audit_log", Func: "retention_prune_audit_log"},
	{Name: "resolved alerts (12 months after resolution)", Table: "alerts",
		Where: `t.status = 'resolved' AND t.resolved_at < now() - interval '12 months'`},
	{Name: "device events (90 days)", Table: "events",
		Where: `t.ts < now() - interval '90 days'`},
	{Name: "command history (90 days after finishing)", Table: "commands",
		Where: `t.status IN ('succeeded', 'failed', 'cancelled') AND t.submitted_at < now() - interval '90 days'`},
	{Name: "Room Readiness runs (per-customer setting)", Table: "nightly_run",
		Where: `t.status NOT IN ('pending', 'in_progress')
		 AND t.scheduled_at < now() - make_interval(days => COALESCE(
		     (SELECT s.retention_days FROM nightly_schedule s WHERE s.customer_id = t.customer_id), 90))`},
	{Name: "remote access sessions (90 days after ending)", Table: "remote_session",
		Where: `t.ended_at < now() - interval '90 days'`},
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
// on the next pass. A rule that fails is logged and the rest still run.
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
			if ctx.Err() != nil {
				return err
			}
			s.log.Warn("retention rule failed", "records", r.Name, "error", err)
			continue
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
	if r.Func != "" {
		return applyFunc(ctx, conn, r, deadline)
	}
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

// applyFunc runs a Func rule for each organisation in turn.
func applyFunc(ctx context.Context, conn *pgx.Conn, r Rule, deadline time.Time) (int64, error) {
	rows, err := conn.Query(ctx, `SELECT id FROM customers`)
	if err != nil {
		return 0, err
	}
	customers, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	var total int64
	for _, c := range customers {
		for time.Now().Before(deadline) {
			var n int64
			if err := conn.QueryRow(ctx, `SELECT `+r.Func+`($1::uuid, $2)`, c, batch).Scan(&n); err != nil {
				return total, err
			}
			total += n
			if n < batch {
				break
			}
		}
	}
	return total, nil
}
