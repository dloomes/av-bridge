package retention

// Day-30 deletion of offboarded customers (GDPR, migration 0073). Once a
// customer's delete_after has passed, everything it holds is deleted, in one
// transaction per customer, and the customer_deletions row records what went
// as a certificate of row counts.
//
// The delete is a single DELETE FROM customers: every customer table
// references customers ON DELETE CASCADE. Cascades run as each table's owner
// with row security not forced, so they also remove rows app_admin can't
// delete directly (audit_log, which stays append-only for the app, and
// alert_tickets, firmware_targets, notification_channels).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

// keptTables have a customer_id but outlive the customer: the deletion
// record itself, which has no foreign key to customers.
var keptTables = []string{"customer_deletions"}

// parentRoute reaches a table through a parent table: table.col references
// parent.id.
type parentRoute struct{ table, col, parent string }

// customerDeleteTimeout bounds each statement of a customer's deletion.
const customerDeleteTimeout = "10min"

// viaParent are large customer_id tables with no customer_id index but an
// index led by a parent key (raw telemetry and events by device, Collector
// logs by Collector; see migrations 0001, 0060, 0063). They're deleted
// through that index before the customer, and counted by the delete.
var viaParent = []parentRoute{
	{"telemetry", "device_id", "devices"},
	{"events", "device_id", "devices"},
	{"collector_logs", "collector_id", "collectors"},
}

// deleteViaParent deletes the customer's rows of p.table through its
// parent-keyed index and returns how many went. Rows are matched on the
// customer too, so a row naming another customer is never touched.
func deleteViaParent(ctx context.Context, tx pgx.Tx, p parentRoute, id string) (int64, error) {
	var parents []string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(array_agg(id::text), '{}') FROM `+pgx.Identifier{p.parent}.Sanitize()+
		` WHERE customer_id = $1::uuid`, id).Scan(&parents); err != nil {
		return 0, fmt.Errorf("list %s: %w", p.parent, err)
	}
	if len(parents) == 0 {
		return 0, nil
	}
	tag, err := tx.Exec(ctx, `DELETE FROM `+pgx.Identifier{p.table}.Sanitize()+
		` WHERE `+pgx.Identifier{p.col}.Sanitize()+` = ANY ($1::uuid[]) AND customer_id = $2::uuid`, parents, id)
	if err != nil {
		return 0, fmt.Errorf("delete %s: %w", p.table, err)
	}
	return tag.RowsAffected(), nil
}

// errNotDue means the customer was no longer due by the time its
// transaction locked it (offboarding cancelled in between, or already gone).
var errNotDue = errors.New("customer no longer due for deletion")

// deleteDueCustomers permanently deletes every offboarding customer whose
// delete_after has passed, each in its own transaction. A customer that
// fails is rolled back, logged and retried next pass; the rest still run.
func deleteDueCustomers(ctx context.Context, conn *pgx.Conn, log *slog.Logger) error {
	rows, err := conn.Query(ctx, `
		SELECT id::text FROM customers
		 WHERE offboarding_started_at IS NOT NULL AND delete_after < now()
		 ORDER BY delete_after`)
	if err != nil {
		return err
	}
	due, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range due {
		counts, err := deleteCustomer(ctx, conn, id)
		switch {
		case errors.Is(err, errNotDue):
			continue
		case err != nil:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Warn("retention: customer delete failed", "customer_id", id, "error", err)
			continue
		}
		var total int64
		for _, n := range counts {
			total += n
		}
		log.Info("retention: customer deleted", "customer_id", id, "tables", len(counts), "rows", total)
	}
	return nil
}

// deleteCustomer deletes one customer in one transaction and returns the
// rows deleted per table (the customers row included).
func deleteCustomer(ctx context.Context, conn *pgx.Conn, id string) (map[string]int64, error) {
	var counts map[string]int64
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		// A runaway delete fails and rolls back rather than holding the
		// retention lock indefinitely; the customer is retried next pass.
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '`+customerDeleteTimeout+`'`); err != nil {
			return err
		}

		// Lock the customer and check it's still due: a cancel committed
		// since the list was read clears delete_after.
		var name, startedBy string
		var startedAt *string
		err := tx.QueryRow(ctx, `
			SELECT name, offboarding_started_at::text, COALESCE(offboarding_started_by, '')
			  FROM customers
			 WHERE id = $1::uuid AND offboarding_started_at IS NOT NULL AND delete_after < now()
			   FOR UPDATE`, id).Scan(&name, &startedAt, &startedBy)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotDue
		}
		if err != nil {
			return err
		}

		// 1. Clear links that would block the cascade, such as
		// collectors.standby_for (a warm-standby pair).
		if err := clearRestrictLinks(ctx, tx, id); err != nil {
			return err
		}

		// 2. Count what is about to go. The big tables with no customer_id
		// index are deleted first through their parent's index, counting
		// as they go, so they're never scanned to count.
		counts = map[string]int64{"customers": 1}
		for _, p := range viaParent {
			n, err := deleteViaParent(ctx, tx, p, id)
			if err != nil {
				return err
			}
			counts[p.table] = n
		}
		tables, err := db.CustomerIDTables(ctx, tx, keptTables...)
		if err != nil {
			return fmt.Errorf("list customer tables: %w", err)
		}
		for _, t := range tables {
			if _, done := counts[t]; done {
				continue
			}
			var n int64
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{t}.Sanitize()+` WHERE customer_id = $1::uuid`, id).Scan(&n); err != nil {
				return fmt.Errorf("count %s: %w", t, err)
			}
			counts[t] = n
		}

		// 3. Delete; every customer table cascades from customers.
		tag, err := tx.Exec(ctx, `DELETE FROM customers WHERE id = $1::uuid`, id)
		if err != nil {
			return fmt.Errorf("delete customer: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("delete customer: %d rows deleted", tag.RowsAffected())
		}

		// 4. The certificate. The offboard endpoint opened the row; if it's
		// missing (offboarded some other way), write one so the deletion is
		// never unrecorded.
		js, err := json.Marshal(counts)
		if err != nil {
			return err
		}
		tag, err = tx.Exec(ctx, `
			UPDATE customer_deletions
			   SET deleted_at = now(), row_counts = $2::jsonb
			 WHERE customer_id = $1::uuid AND deleted_at IS NULL AND cancelled_at IS NULL`, id, string(js))
		if err != nil {
			return fmt.Errorf("record deletion: %w", err)
		}
		if tag.RowsAffected() == 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO customer_deletions (customer_id, customer_name, started_at, started_by, deleted_at, row_counts)
				VALUES ($1::uuid, $2, COALESCE($3::timestamptz, now()), COALESCE(NULLIF($4, ''), 'unknown'), now(), $5::jsonb)`,
				id, name, startedAt, startedBy, string(js)); err != nil {
				return fmt.Errorf("record deletion: %w", err)
			}
		}
		return nil
	})
	return counts, err
}

// clearRestrictLinks sets to NULL, for the customer's rows, every ON DELETE
// RESTRICT foreign key on a customer_id table. RESTRICT is checked row by
// row, so one row referencing another the same cascade deletes (a standby
// Collector and its primary) would otherwise stop the delete. Found from
// the catalogue, so a new RESTRICT link is handled without a code change.
// A link that includes customer_id itself isn't touched: it must not
// exist, and the delete then fails loudly.
func clearRestrictLinks(ctx context.Context, tx pgx.Tx, id string) error {
	rows, err := tx.Query(ctx, `
		SELECT c.relname::text, array_agg(a.attname::text ORDER BY a.attnum)
		  FROM pg_constraint k
		  JOIN pg_class c ON c.oid = k.conrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  JOIN pg_attribute a ON a.attrelid = k.conrelid AND a.attnum = ANY (k.conkey)
		 WHERE k.contype = 'f' AND k.confdeltype = 'r' AND n.nspname = 'public'
		   AND EXISTS (SELECT 1 FROM pg_attribute ca
		                WHERE ca.attrelid = k.conrelid AND ca.attname = 'customer_id' AND NOT ca.attisdropped)
		 GROUP BY k.oid, c.relname
		HAVING NOT bool_or(a.attname = 'customer_id')
		 ORDER BY 1, 2`)
	if err != nil {
		return err
	}
	type link struct {
		table string
		cols  []string
	}
	links, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (link, error) {
		var l link
		err := r.Scan(&l.table, &l.cols)
		return l, err
	})
	if err != nil {
		return err
	}
	for _, l := range links {
		set := make([]string, len(l.cols))
		notNull := make([]string, len(l.cols))
		for i, c := range l.cols {
			col := pgx.Identifier{c}.Sanitize()
			set[i] = col + " = NULL"
			notNull[i] = col + " IS NOT NULL"
		}
		if _, err := tx.Exec(ctx, `UPDATE `+pgx.Identifier{l.table}.Sanitize()+` SET `+strings.Join(set, ", ")+
			` WHERE customer_id = $1::uuid AND (`+strings.Join(notNull, " OR ")+`)`, id); err != nil {
			return fmt.Errorf("clear %s.%s: %w", l.table, strings.Join(l.cols, ","), err)
		}
	}
	return nil
}
