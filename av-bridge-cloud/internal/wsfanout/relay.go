package wsfanout

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Publisher is what ingest and offboarding talk to. *Hub satisfies it for a
// single copy; *Relay satisfies it across copies via Postgres.
type Publisher interface {
	Publish(customerID string, ev Event)
	CloseCustomer(customerID string) int
}

const (
	liveChannel = "live_events"
	// Postgres caps a NOTIFY payload at 8000 bytes; stay under it.
	maxInlineNotify = 7900
	publishTimeout  = 2 * time.Second

	overflowSweepEvery = 10 * time.Minute
	overflowMaxAge     = "1 hour"
)

// liveMsg is the NOTIFY payload. K is "event", "close" or "ref" (the event
// is in live_event_overflow row ID because it was too big to send inline).
type liveMsg struct {
	C  string `json:"c"`
	K  string `json:"k"`
	E  *Event `json:"e,omitempty"`
	ID int64  `json:"id,omitempty"`
}

// Relay carries live events and customer closes through Postgres
// LISTEN/NOTIFY so browsers on every web copy see them. Publish and
// CloseCustomer only enqueue; a sender goroutine (started by Run) does the
// database work, so callers never wait on Postgres. Run also receives (on
// every copy, including the sender) and delivers into the local Hub.
type Relay struct {
	pool  *pgxpool.Pool
	hub   *Hub
	log   *slog.Logger
	queue chan liveMsg

	dropped  atomic.Int64
	lastWarn atomic.Int64 // unix nanos of the last drop warning
}

// sendQueueSize bounds events waiting for the sender goroutine.
const sendQueueSize = 1024

func NewRelay(pool *pgxpool.Pool, hub *Hub, log *slog.Logger) *Relay {
	return &Relay{pool: pool, hub: hub, log: log, queue: make(chan liveMsg, sendQueueSize)}
}

// Publish queues ev for every copy and returns at once. If the queue is full
// (database slow or down) the event is dropped: live events are
// observability, the persisted copy is the record.
func (r *Relay) Publish(customerID string, ev Event) {
	r.enqueue(liveMsg{C: customerID, K: "event", E: &ev})
}

// CloseCustomer asks every copy to end the customer's event streams. The
// closes happen asynchronously, so the count is unknown here: returns 0.
func (r *Relay) CloseCustomer(customerID string) int {
	r.enqueue(liveMsg{C: customerID, K: "close"})
	return 0
}

func (r *Relay) enqueue(m liveMsg) {
	select {
	case r.queue <- m:
	default:
		n := r.dropped.Add(1)
		now := time.Now().UnixNano()
		last := r.lastWarn.Load()
		if now-last > int64(time.Minute) && r.lastWarn.CompareAndSwap(last, now) {
			r.log.Warn("live event queue full, dropping events", "dropped_total", n)
		} else {
			r.log.Debug("live event queue full, dropping event", "customer", m.C)
		}
	}
}

// sendLoop drains the queue in order until ctx ends.
func (r *Relay) sendLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-r.queue:
			r.send(m)
		}
	}
}

// send does the NOTIFY (and the overflow INSERT for a big event) under a
// short timeout. Errors are logged, never returned.
func (r *Relay) send(m liveMsg) {
	b, err := json.Marshal(m)
	if err != nil {
		r.log.Warn("live event marshal failed", "error", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	if len(b) > maxInlineNotify {
		var id int64
		if err := r.pool.QueryRow(ctx,
			`INSERT INTO live_event_overflow (body) VALUES ($1::jsonb) RETURNING id`, string(b)).Scan(&id); err != nil {
			r.log.Warn("live event overflow insert failed", "error", err)
			return
		}
		b, _ = json.Marshal(liveMsg{C: m.C, K: "ref", ID: id})
	}
	r.notify(ctx, b)
}

func (r *Relay) notify(ctx context.Context, payload []byte) {
	if _, err := r.pool.Exec(ctx, `SELECT pg_notify($1, $2)`, liveChannel, string(payload)); err != nil {
		r.log.Warn("live event notify failed", "error", err)
	}
}

// Run listens until ctx is cancelled, reconnecting with backoff (1s to 30s)
// if the connection drops, sends queued events and prunes old overflow rows.
func (r *Relay) Run(ctx context.Context) {
	go r.sweepOverflow(ctx)
	go r.sendLoop(ctx)
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := r.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second // was healthy for a while; start over
		}
		r.log.Warn("live event listener lost, reconnecting", "error", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

// listen holds one dedicated connection until it fails or ctx ends.
func (r *Relay) listen(ctx context.Context) error {
	l, err := db.Listen(ctx, r.pool, liveChannel)
	if err != nil {
		return err
	}
	defer l.Close()
	for {
		payload, err := l.Wait(ctx)
		if err != nil {
			return err
		}
		r.handle(ctx, payload)
	}
}

// handle delivers one notification. A bad one is logged and skipped.
func (r *Relay) handle(ctx context.Context, payload string) {
	var m liveMsg
	if err := json.Unmarshal([]byte(payload), &m); err != nil || m.C == "" {
		r.log.Warn("malformed live event notification", "error", err)
		return
	}
	switch m.K {
	case "event":
		if m.E == nil {
			r.log.Warn("live event notification without event")
			return
		}
		r.hub.Publish(m.C, *m.E)
	case "close":
		r.hub.CloseCustomer(m.C)
	case "ref":
		var body []byte
		qctx, cancel := context.WithTimeout(ctx, publishTimeout)
		defer cancel()
		if err := r.pool.QueryRow(qctx,
			`SELECT body FROM live_event_overflow WHERE id = $1`, m.ID).Scan(&body); err != nil {
			r.log.Warn("live event overflow read failed", "id", m.ID, "error", err)
			return
		}
		var full liveMsg
		if err := json.Unmarshal(body, &full); err != nil || full.E == nil {
			r.log.Warn("malformed live event overflow row", "id", m.ID, "error", err)
			return
		}
		r.hub.Publish(m.C, *full.E)
	default:
		r.log.Warn("unknown live event notification kind", "kind", m.K)
	}
}

func (r *Relay) sweepOverflow(ctx context.Context) {
	t := time.NewTicker(overflowSweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if _, err := r.pool.Exec(qctx,
				`DELETE FROM live_event_overflow WHERE created_at < now() - interval '`+overflowMaxAge+`'`); err != nil {
				r.log.Warn("live event overflow sweep failed", "error", err)
			}
			cancel()
		}
	}
}
