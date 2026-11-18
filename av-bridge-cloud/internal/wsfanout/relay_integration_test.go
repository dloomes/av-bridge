package wsfanout

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

// Needs a disposable Postgres 16 in TEST_DATABASE_URL; skipped otherwise.
func relayPair(t *testing.T) (a, b *Relay, hubA, hubB *Hub, st *db.Store) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping relay integration test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, dsn, quiet); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	as := func(user, pass string) string {
		u, _ := url.Parse(dsn)
		u.User = url.UserPassword(user, pass)
		return u.String()
	}
	st, err := db.New(ctx, as("app_admin", "app_admin_dev"), as("app_tenant", "app_tenant_dev"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	hubA, hubB = NewHub(quiet), NewHub(quiet)
	a = NewRelay(st.AdminPool(), hubA, quiet)
	b = NewRelay(st.AdminPool(), hubB, quiet)
	done := make(chan struct{}, 2)
	for _, r := range []*Relay{a, b} {
		go func() { r.Run(ctx); done <- struct{}{} }()
	}
	// Registered after st.Close, so it runs first: stop both listeners
	// (releasing their connections) before the pool closes.
	t.Cleanup(func() {
		cancel()
		for range 2 {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("relay Run did not stop after cancel")
			}
		}
	})
	// Let both LISTENs register before publishing.
	time.Sleep(500 * time.Millisecond)
	return
}

func recv(s *subscriber, d time.Duration) (Event, bool) {
	select {
	case ev := <-s.ch:
		return ev, true
	case <-time.After(d):
		return Event{}, false
	}
}

func TestRelayDeliversAcrossHubs(t *testing.T) {
	a, _, _, hubB, _ := relayPair(t)
	cust, other := newID(), newID()
	sub := hubB.subscribe(cust)
	defer hubB.unsubscribe(cust, sub)
	otherSub := hubB.subscribe(other)
	defer hubB.unsubscribe(other, otherSub)

	a.Publish(cust, Event{DeviceID: "d1", EventType: "x", Timestamp: time.Now()})
	ev, ok := recv(sub, 2*time.Second)
	if !ok || ev.DeviceID != "d1" {
		t.Fatalf("hub B subscriber got %+v ok=%v", ev, ok)
	}
	if ev, ok := recv(otherSub, 300*time.Millisecond); ok {
		t.Fatalf("other customer received %+v", ev)
	}
}

func TestRelayOversizeEvent(t *testing.T) {
	a, _, _, hubB, st := relayPair(t)
	cust := newID()
	sub := hubB.subscribe(cust)
	defer hubB.unsubscribe(cust, sub)

	big := strings.Repeat("z", 20000)
	marker := newID()
	a.Publish(cust, Event{DeviceID: marker, EventType: "big", Payload: map[string]any{"blob": big}, Timestamp: time.Now()})
	ev, ok := recv(sub, 2*time.Second)
	if !ok {
		t.Fatal("oversize event not delivered")
	}
	if ev.Payload["blob"] != big {
		t.Fatalf("payload not intact (len %d)", len(ev.Payload["blob"].(string)))
	}
	var n int
	if err := st.AdminPool().QueryRow(context.Background(),
		`SELECT count(*) FROM live_event_overflow WHERE body->'e'->>'device_id' = $1`, marker).Scan(&n); err != nil || n != 1 {
		t.Fatalf("overflow rows = %d err=%v", n, err)
	}
}

func TestRelayCloseCustomer(t *testing.T) {
	a, _, _, hubB, _ := relayPair(t)
	cust := newID()
	sub := hubB.subscribe(cust)
	defer hubB.unsubscribe(cust, sub)

	if n := a.CloseCustomer(cust); n != 0 {
		t.Fatalf("CloseCustomer returned %d, want 0", n)
	}
	select {
	case <-sub.kick:
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber on hub B was not kicked")
	}
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
