package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Querier is what CustomerIDTables needs: a pgx.Tx, *pgx.Conn or pool.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// CustomerIDTables lists, by name, every table in the public schema with a
// customer_id column, except those in exclude. It reads pg_catalog, not
// information_schema, so a table the role can't read is still listed (and a
// query on it then fails loudly) rather than silently left out; new tables
// are picked up without a code change. Partitions are left out (their parent
// covers them).
func CustomerIDTables(ctx context.Context, q Querier, exclude ...string) ([]string, error) {
	if exclude == nil {
		exclude = []string{}
	}
	rows, err := q.Query(ctx, `
		SELECT c.relname::text
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  JOIN pg_attribute a ON a.attrelid = c.oid
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition
		   AND a.attname = 'customer_id' AND NOT a.attisdropped
		   AND c.relname <> ALL($1::text[])
		 ORDER BY 1`, exclude)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
