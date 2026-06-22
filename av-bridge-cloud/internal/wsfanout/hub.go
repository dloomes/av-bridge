// Package wsfanout serves the cloud's live event stream. Every portal client
// connects to GET /ws/events and is subscribed to its own customer's stream
// (RLS-equivalent for live data — a client never sees another tenant's
// events). The ingest path publishes after persist; subscribers fan out via
// per-conn channels with a small buffer that drops on slow consumers rather
// than backing the publisher up.
//
// Authentication is handled by the existing portalauth.Middleware in front of
// the handler — by the time ServeHTTP runs, the Principal is in ctx and we
// trust its CustomerID.
package wsfanout

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/gorilla/websocket"
)

// Buffer per subscriber. Big enough to absorb a brief portal stall without
// dropping; small enough that a stuck client doesn't tie up memory.
const subBufferSize = 64

// Event is the wire shape sent to portal subscribers. Field names match the
// portal's DeviceEvent type and the ingest path's eventDTO so the publisher
// can just hand its existing struct to Publish without remapping.
type Event struct {
	DeviceID   string         `json:"device_id"`
	DeviceName string         `json:"device_name"`
	DeviceType string         `json:"device_type"`
	EventType  string         `json:"event_type"`
	Payload    map[string]any `json:"payload,omitempty"`
	Timestamp  time.Time      `json:"timestamp"`
}

// Hub holds the per-customer subscriber set. The map-of-maps lets Publish
// fan out without walking unrelated tenants.
type Hub struct {
	mu          sync.RWMutex
	subscribers map[string]map[*subscriber]struct{}
	log         *slog.Logger
}

func NewHub(log *slog.Logger) *Hub {
	return &Hub{
		subscribers: make(map[string]map[*subscriber]struct{}),
		log:         log,
	}
}

// Publish fans an event to every subscriber of customerID. Non-blocking: if
// a subscriber's buffer is full, the event is dropped for that subscriber
// only (and we log at debug — these are observability events, not commands).
func (h *Hub) Publish(customerID string, ev Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	subs := h.subscribers[customerID]
	if len(subs) == 0 {
		return
	}
	for s := range subs {
		select {
		case s.ch <- ev:
		default:
			// Subscriber's buffer is full — drop. The reader is too slow.
			h.log.Debug("ws subscriber buffer full, dropping event",
				"customer", customerID, "event_type", ev.EventType)
		}
	}
}

type subscriber struct {
	ch chan Event
}

func (h *Hub) subscribe(customerID string) *subscriber {
	s := &subscriber{ch: make(chan Event, subBufferSize)}
	h.mu.Lock()
	if _, ok := h.subscribers[customerID]; !ok {
		h.subscribers[customerID] = make(map[*subscriber]struct{})
	}
	h.subscribers[customerID][s] = struct{}{}
	h.mu.Unlock()
	return s
}

func (h *Hub) unsubscribe(customerID string, s *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if subs, ok := h.subscribers[customerID]; ok {
		delete(subs, s)
		if len(subs) == 0 {
			delete(h.subscribers, customerID)
		}
	}
	close(s.ch)
}

// Allow any origin — auth is the token in the URL, not the origin header,
// and we want browser portals on arbitrary hosts to connect.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// ServeHTTP upgrades the connection, registers a subscriber, then pumps
// events until the client disconnects or the connection write fails.
//
// Must be mounted behind portalauth.Middleware so r.Context() carries the
// Principal — Publish needs the customer scope to send only their events.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := portalauth.From(r.Context())
	if !ok {
		http.Error(w, "no principal in context", http.StatusInternalServerError)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Warn("ws upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	sub := h.subscribe(p.CustomerID)
	defer h.unsubscribe(p.CustomerID, sub)
	h.log.Info("ws subscriber connected", "customer", p.CustomerID)

	// Reader goroutine: we don't expect client messages, but draining the
	// reader is required so the connection's close frame and ping/pong
	// machinery work. When the client disconnects, ReadMessage errors and
	// we signal the writer.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case ev, ok := <-sub.ch:
			if !ok {
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
				h.log.Debug("ws write failed, closing", "customer", p.CustomerID, "error", err)
				return
			}
		case <-pingTicker.C:
			// Periodic ping keeps NAT/proxy idle timeouts at bay and detects
			// dead peers when no real traffic is flowing.
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-done:
			h.log.Info("ws subscriber disconnected", "customer", p.CustomerID)
			return
		case <-r.Context().Done():
			return
		}
	}
}
