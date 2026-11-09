package roadmap

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// TestLock is for integration tests only. The roadmap tables are global and
// `go test ./...` runs packages concurrently against one database, so every
// test that reads or writes roadmap rows holds this session-level advisory
// lock on its own connection. Call the returned func (usually via t.Cleanup)
// to release it.
func TestLock(ctx context.Context, dsn string) (unlock func(), err error) {
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if _, err := c.Exec(ctx, `SELECT pg_advisory_lock(hashtext('roadmap-global-test'))`); err != nil {
		c.Close(ctx)
		return nil, err
	}
	return func() {
		_, _ = c.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext('roadmap-global-test'))`)
		c.Close(context.Background())
	}, nil
}
