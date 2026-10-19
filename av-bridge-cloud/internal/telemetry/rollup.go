// Package telemetry keeps raw telemetry bounded: it folds readings into a
// daily summary per device (telemetry_daily, migration 0060) that reports
// read, and deletes raw readings once they're summarised and older than the
// retention period.
//
// Readings are picked up by when they arrived (inserted_at), not when they
// were taken, so a Collector replaying hours of buffered readings after an
// outage is still counted. Rows from before migration 0060 have no arrival
// time; they're backfilled a day at a time by reading time.
package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// Interval between rollup passes.
	Interval = 5 * time.Minute
	// Lag leaves the newest readings for the next pass, so an ingest
	// transaction still committing isn't skipped.
	Lag = 2 * time.Minute
	// SummaryRetentionDays is past the longest report window (365).
	SummaryRetentionDays = 400

	pruneBatch  = 20_000
	passBudget  = 2 * time.Minute // stop backfill and pruning after this, resume next pass
	lockKey     = 0x7e1e_0060     // pg advisory lock: one pass at a time across cloud tasks
	backfillDay = 24 * time.Hour
)

// Service runs rollup and retention passes.
type Service struct {
	pool          *pgxpool.Pool // admin pool: works across every organisation
	retentionDays int
	log           *slog.Logger
}

func New(pool *pgxpool.Pool, retentionDays int, log *slog.Logger) *Service {
	if retentionDays < 7 {
		retentionDays = 7
	}
	return &Service{pool: pool, retentionDays: retentionDays, log: log}
}

// Run makes a pass every Interval until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.log.Info("telemetry rollups started", "raw_retention_days", s.retentionDays, "summary_retention_days", SummaryRetentionDays)
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		if err := s.Pass(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("telemetry rollup pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Pass rolls up new readings, backfills old ones, and prunes.
func (s *Service) Pass(ctx context.Context) error {
	deadline := time.Now().Add(passBudget)
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&got); err != nil || !got {
		return err // another task is mid-pass
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockKey)

	if err := Rollup(ctx, conn.Conn(), Lag); err != nil {
		return err
	}
	for time.Now().Before(deadline) {
		done, err := backfillDayStep(ctx, conn.Conn())
		if err != nil {
			return err
		}
		if done {
			break
		}
	}
	return s.prune(ctx, conn.Conn(), deadline)
}

// mergeSQL folds the readings selected by the caller's WHERE clause into
// telemetry_daily. Summaries merge: counts add, hour masks OR, peaks max.
const mergeSQL = `
	INSERT INTO telemetry_daily AS d
	       (customer_id, device_id, day, samples, online_samples,
	        call_hours, occupied_hours, display_on_hours, usage_signal, peak_people)
	SELECT t.customer_id, t.device_id, (t.ts AT TIME ZONE 'UTC')::date,
	       count(*),
	       count(*) FILTER (WHERE t.status = 'online'),
	       COALESCE(bit_or(1 << extract(hour FROM t.ts AT TIME ZONE 'UTC')::int)
	                FILTER (WHERE t.status = 'online'
	                          AND jsonb_typeof(t.metrics->'active_calls') = 'number'
	                          AND (t.metrics->>'active_calls')::numeric > 0), 0),
	       COALESCE(bit_or(1 << extract(hour FROM t.ts AT TIME ZONE 'UTC')::int)
	                FILTER (WHERE t.status = 'online'
	                          AND jsonb_typeof(t.metrics->'people_count') = 'number'
	                          AND (t.metrics->>'people_count')::numeric > 0), 0),
	       COALESCE(bit_or(1 << extract(hour FROM t.ts AT TIME ZONE 'UTC')::int)
	                FILTER (WHERE t.status = 'online'
	                          AND t.metrics->>'power_status' IN ('on', 'active')), 0),
	       COALESCE(bool_or(t.metrics ? 'active_calls' OR t.metrics ? 'people_count')
	                FILTER (WHERE t.status = 'online'), false),
	       max(CASE WHEN jsonb_typeof(t.metrics->'people_count') = 'number'
	                 AND (t.metrics->>'people_count')::numeric >= 0
	                THEN (t.metrics->>'people_count')::numeric END)
	           FILTER (WHERE t.status = 'online')::int
	  FROM telemetry t
	 WHERE %s
	 GROUP BY t.customer_id, t.device_id, (t.ts AT TIME ZONE 'UTC')::date
	ON CONFLICT (device_id, day) DO UPDATE SET
	       samples          = d.samples + EXCLUDED.samples,
	       online_samples   = d.online_samples + EXCLUDED.online_samples,
	       call_hours       = d.call_hours | EXCLUDED.call_hours,
	       occupied_hours   = d.occupied_hours | EXCLUDED.occupied_hours,
	       display_on_hours = d.display_on_hours | EXCLUDED.display_on_hours,
	       usage_signal     = d.usage_signal OR EXCLUDED.usage_signal,
	       peak_people      = GREATEST(d.peak_people, EXCLUDED.peak_people)`

// Rollup folds readings that arrived since the last pass (up to lag ago)
// into the daily summaries. Exposed for tests, which pass lag 0.
func Rollup(ctx context.Context, conn *pgx.Conn, lag time.Duration) error {
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		var from, upto time.Time
		if err := tx.QueryRow(ctx, `
			SELECT GREATEST(rolled_to, 'epoch'::timestamptz), now() - make_interval(secs => $1)
			  FROM telemetry_rollup_state WHERE id = 1 FOR UPDATE`, lag.Seconds()).Scan(&from, &upto); err != nil {
			return err
		}
		if !upto.After(from) {
			return nil
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(mergeSQL, `t.inserted_at > $1 AND t.inserted_at <= $2`), from, upto); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE telemetry_rollup_state SET rolled_to = $1 WHERE id = 1`, upto)
		return err
	})
}

// Backfill folds every reading from before migration 0060 into the
// summaries, a day at a time. Exposed for tests; Pass does it gradually.
func Backfill(ctx context.Context, conn *pgx.Conn) error {
	for {
		done, err := backfillDayStep(ctx, conn)
		if err != nil || done {
			return err
		}
	}
}

func backfillDayStep(ctx context.Context, conn *pgx.Conn) (bool, error) {
	done := false
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		var (
			cursor *time.Time
			fin    bool
		)
		if err := tx.QueryRow(ctx, `
			SELECT backfill_to, backfill_done FROM telemetry_rollup_state WHERE id = 1 FOR UPDATE`).Scan(&cursor, &fin); err != nil {
			return err
		}
		if fin {
			done = true
			return nil
		}
		if cursor == nil {
			// Start from the oldest reading without an arrival time.
			var oldest *time.Time
			if err := tx.QueryRow(ctx, `SELECT min(ts) FROM telemetry WHERE inserted_at IS NULL`).Scan(&oldest); err != nil {
				return err
			}
			if oldest == nil {
				done = true
				_, err := tx.Exec(ctx, `UPDATE telemetry_rollup_state SET backfill_done = true WHERE id = 1`)
				return err
			}
			start := oldest.UTC().Truncate(backfillDay)
			cursor = &start
		}
		next := cursor.Add(backfillDay)
		if _, err := tx.Exec(ctx, fmt.Sprintf(mergeSQL, `t.inserted_at IS NULL AND t.ts >= $1 AND t.ts < $2`), *cursor, next); err != nil {
			return err
		}
		fin = !next.Before(time.Now())
		done = fin
		_, err := tx.Exec(ctx, `UPDATE telemetry_rollup_state SET backfill_to = $1, backfill_done = $2 WHERE id = 1`, next, fin)
		return err
	})
	return done, err
}

// Prune deletes raw readings older than the retention period that have
// been summarised, and summaries past SummaryRetentionDays. Exposed for
// tests.
func Prune(ctx context.Context, conn *pgx.Conn, retentionDays int) error {
	return prune(ctx, conn, retentionDays, time.Now().Add(time.Hour))
}

func (s *Service) prune(ctx context.Context, conn *pgx.Conn, deadline time.Time) error {
	return prune(ctx, conn, s.retentionDays, deadline)
}

func prune(ctx context.Context, conn *pgx.Conn, retentionDays int, deadline time.Time) error {
	// Only readings already in the summaries: arrived before rolled_to, or
	// (from before migration 0060) taken before the backfill cursor.
	for time.Now().Before(deadline) {
		tag, err := conn.Exec(ctx, `
			DELETE FROM telemetry
			 WHERE ctid = ANY (ARRAY(
			     SELECT t.ctid FROM telemetry t, telemetry_rollup_state s
			      WHERE s.id = 1
			        AND t.ts < now() - make_interval(days => $1)
			        AND ((t.inserted_at IS NOT NULL AND t.inserted_at <= s.rolled_to)
			             OR (t.inserted_at IS NULL AND (s.backfill_done OR t.ts < s.backfill_to)))
			      LIMIT $2))`, retentionDays, pruneBatch)
		if err != nil {
			return err
		}
		if tag.RowsAffected() < pruneBatch {
			break
		}
	}
	_, err := conn.Exec(ctx, `DELETE FROM telemetry_daily WHERE day < (now() AT TIME ZONE 'UTC')::date - $1::int`, SummaryRetentionDays)
	return err
}
