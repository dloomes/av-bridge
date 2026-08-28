package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Listener holds a pooled connection with an active LISTEN subscription.
// Each Listener ties up one admin-pool connection for its lifetime — the
// command-queue signalling paths use these for at most tens of seconds
// per request, which fits within the admin pool's budget at current scale
// (single-digit collectors, sub-second portal waits). Grow the admin
// pool or add a dedicated listener pool if concurrent listener count
// creeps into the hundreds.
//
// Callers MUST call Close to return the conn.
type Listener struct {
	conn    *pgxpool.Conn
	channel string
}

// Listen acquires a conn from the given pool and issues LISTEN <channel>.
// The channel name is a Postgres identifier — pass only compile-time
// constants (see commands.ChannelPending / ChannelDone). Payload matching
// is the caller's job.
func Listen(ctx context.Context, pool *pgxpool.Pool, channel string) (*Listener, error) {
	c, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire listener conn: %w", err)
	}
	if _, err := c.Exec(ctx, "LISTEN "+quoteIdent(channel)); err != nil {
		c.Release()
		return nil, fmt.Errorf("LISTEN %s: %w", channel, err)
	}
	return &Listener{conn: c, channel: channel}, nil
}

// Listen is a convenience wrapper for callers that hold a Store: uses the
// admin pool (BYPASSRLS role — LISTEN/NOTIFY doesn't touch RLS so this is
// semantically fine; it just centralises conn usage).
func (s *Store) Listen(ctx context.Context, channel string) (*Listener, error) {
	return Listen(ctx, s.admin, channel)
}

// Wait blocks until a NOTIFY arrives on the subscribed channel or ctx
// fires. Returns the raw payload string — callers filter by their
// per-request identifier (collector_id, command_id).
func (l *Listener) Wait(ctx context.Context) (string, error) {
	n, err := l.conn.Conn().WaitForNotification(ctx)
	if err != nil {
		return "", err
	}
	return n.Payload, nil
}

// Close UNLISTENs and releases the conn back to the pool. Best-effort —
// if the backend conn is already dead, the pool discards it on release.
func (l *Listener) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = l.conn.Exec(ctx, "UNLISTEN *")
	l.conn.Release()
}

// quoteIdent double-quotes a Postgres identifier for safe interpolation
// into LISTEN. Only called with package-local channel-name constants;
// the quoting is defence in depth, not the security boundary.
func quoteIdent(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			out = append(out, '"', '"')
			continue
		}
		out = append(out, s[i])
	}
	out = append(out, '"')
	return string(out)
}
