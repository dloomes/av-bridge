// Package remote is remote access: people open a device's web interface
// from the portal, relayed through the cloud and the device's collector.
//
//	browser ──https──▶ cloud (session address) ──tunnel──▶ collector ──▶ device
//
// Each collector keeps an outbound websocket open to /bridge/tunnel
// (tunnel.go). A portal user starts a session (sessions.go), which opens in
// its own browser tab on its own address, <label>.<access domain>; the
// cloud proxies that address's requests through the collector's tunnel to
// the device (access.go). The collector only ever connects to its own
// devices, on web ports.
package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"github.com/dloomes/av-bridge-cloud/internal/bridgeauth"
)

// ErrNotConnected: the collector has no tunnel open (offline, or a version
// before remote access).
var ErrNotConnected = errors.New("the device's Collector isn't connected for remote access")

// Tunnels holds each connected collector's tunnel. Tunnels end on the cloud
// task they connected to, so remote access assumes one cloud task (as UAT
// runs); more tasks would need sessions routed to the task holding the
// tunnel.
type Tunnels struct {
	auth *bridgeauth.Authenticator
	log  *slog.Logger

	mu       sync.Mutex
	sessions map[string]*yamux.Session // collector (machine) id
}

func NewTunnels(auth *bridgeauth.Authenticator, log *slog.Logger) *Tunnels {
	return &Tunnels{auth: auth, log: log, sessions: map[string]*yamux.Session{}}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	// The collector isn't a browser; there's no origin to check.
	CheckOrigin: func(*http.Request) bool { return true },
}

// Handle is GET /bridge/tunnel: a collector opening its tunnel.
func (t *Tunnels) Handle(w http.ResponseWriter, r *http.Request) {
	col, ok := t.auth.AuthenticateTunnel(w, r)
	if !ok {
		return
	}
	// The tunnel lives for hours: lift the server's per-request timeouts.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // the upgrader has written the response
	}
	_ = ws.UnderlyingConn().SetDeadline(time.Time{})
	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = 20 * time.Second
	cfg.LogOutput = io.Discard
	sess, err := yamux.Client(NewWSConn(ws), cfg)
	if err != nil {
		ws.Close()
		return
	}

	t.mu.Lock()
	if old := t.sessions[col.ID]; old != nil {
		old.Close() // a reconnect replaces the previous tunnel
	}
	t.sessions[col.ID] = sess
	t.mu.Unlock()
	t.log.Info("remote access tunnel connected", "collector", col.ID)

	<-sess.CloseChan()
	t.mu.Lock()
	if t.sessions[col.ID] == sess {
		delete(t.sessions, col.ID)
	}
	t.mu.Unlock()
	t.log.Info("remote access tunnel closed", "collector", col.ID)
}

// Connected reports whether the collector has a tunnel open.
func (t *Tunnels) Connected(collectorID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sessions[collectorID]
	return s != nil && !s.IsClosed()
}

// Dial opens a connection to a device's port through its collector's
// tunnel. reportedID is the device's id on the collector.
func (t *Tunnels) Dial(ctx context.Context, collectorID, reportedID string, port int) (net.Conn, error) {
	t.mu.Lock()
	sess := t.sessions[collectorID]
	t.mu.Unlock()
	if sess == nil || sess.IsClosed() {
		return nil, ErrNotConnected
	}
	stream, err := sess.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	req, _ := json.Marshal(map[string]any{"device": reportedID, "port": port})
	if _, err := stream.Write(append(req, '\n')); err != nil {
		stream.Close()
		return nil, err
	}
	deadline := time.Now().Add(20 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = stream.SetReadDeadline(deadline)
	br := bufio.NewReader(stream)
	reply, err := br.ReadString('\n')
	if err != nil {
		stream.Close()
		return nil, fmt.Errorf("no answer from the Collector: %w", err)
	}
	_ = stream.SetReadDeadline(time.Time{})
	if reply = strings.TrimSpace(reply); reply != "ok" {
		stream.Close()
		return nil, errors.New(strings.TrimPrefix(reply, "error: "))
	}
	return &bufferedConn{Conn: stream, r: br}, nil
}

// bufferedConn reads through the reader that consumed the "ok" line, in
// case the device's first bytes arrived with it.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
