// Package tunnel keeps a connection open from the collector to the cloud so
// people can reach a device's web interface through the portal (remote
// access), wherever the collector runs — on site or in the cloud, with no
// inbound ports.
//
// The collector dials wss://<cloud>/bridge/tunnel, signing its collector id
// and the time with its HMAC secret, and serves yamux streams the cloud
// opens over it. Each stream starts with one JSON line naming a device and
// a port; the collector connects to that device — only to devices it
// manages, and only on web ports — answers "ok", and relays bytes both ways.
// A stream can never be used to reach anything else on the site network.
package tunnel

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"github.com/dloomes/av-bridge/internal/config"
)

// Devices is the collector's device list (satisfied by *hub.Hub).
type Devices interface {
	DeviceConfig(id string) (config.DeviceConfig, bool)
}

// webPorts are always allowed for a device; its own control port and a
// web_port tag are allowed as well.
var webPorts = map[int]bool{80: true, 443: true, 8080: true, 8443: true}

// Client is the collector's end of the tunnel.
type Client struct {
	url         string
	collectorID string
	secret      string
	devices     Devices
	dialer      websocket.Dialer
	dialDevice  func(ctx context.Context, network, addr string) (net.Conn, error)
}

// New returns a client for the cloud at portalAPI (http[s]://host).
func New(portalAPI, collectorID, secret string, tlsSkipVerify bool, devices Devices) *Client {
	u := strings.TrimRight(portalAPI, "/")
	u = strings.Replace(strings.Replace(u, "https://", "wss://", 1), "http://", "ws://", 1)
	d := &net.Dialer{Timeout: 10 * time.Second}
	return &Client{
		url:         u + "/bridge/tunnel",
		collectorID: collectorID,
		secret:      secret,
		devices:     devices,
		dialer: websocket.Dialer{
			HandshakeTimeout: 15 * time.Second,
			TLSClientConfig:  &tls.Config{InsecureSkipVerify: tlsSkipVerify},
			Proxy:            http.ProxyFromEnvironment,
		},
		dialDevice: d.DialContext,
	}
}

// Sign is the tunnel's credential: HMAC-SHA256 over "tunnel:<id>:<unix>".
// The cloud accepts it for a few minutes either side of the stamp.
func Sign(secret, collectorID string, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "tunnel:%s:%d", collectorID, ts)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Run keeps the tunnel up until ctx ends, reconnecting with backoff.
func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := c.serveOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second // it was up for a while: retry promptly
		}
		slog.Warn("remote access tunnel down, reconnecting", "error", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

func (c *Client) serveOnce(ctx context.Context) error {
	ts := time.Now().Unix()
	h := http.Header{}
	h.Set("X-Collector-ID", c.collectorID)
	h.Set("X-Timestamp", strconv.FormatInt(ts, 10))
	h.Set("X-Signature", Sign(c.secret, c.collectorID, ts))
	ws, resp, err := c.dialer.DialContext(ctx, c.url, h)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("connect: %w (HTTP %d)", err, resp.StatusCode)
		}
		return fmt.Errorf("connect: %w", err)
	}
	conn := NewWSConn(ws)
	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = 20 * time.Second // well inside load-balancer idle timeouts
	cfg.LogOutput = io.Discard
	sess, err := yamux.Server(conn, cfg)
	if err != nil {
		conn.Close()
		return err
	}
	defer sess.Close()
	slog.Info("remote access tunnel connected")
	go func() {
		<-ctx.Done()
		sess.Close()
	}()
	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			return err
		}
		go c.handle(ctx, stream)
	}
}

// request is the first line of every stream.
type request struct {
	Device string `json:"device"` // reported id
	Port   int    `json:"port"`
}

func (c *Client) handle(ctx context.Context, stream net.Conn) {
	defer stream.Close()
	_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(stream)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return
	}
	_ = stream.SetReadDeadline(time.Time{})
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		fmt.Fprintf(stream, "error: bad request\n")
		return
	}
	addr, err := c.target(req)
	if err != nil {
		fmt.Fprintf(stream, "error: %v\n", err)
		return
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	dev, err := c.dialDevice(dctx, "tcp", addr)
	cancel()
	if err != nil {
		fmt.Fprintf(stream, "error: can't reach the device: %v\n", err)
		return
	}
	defer dev.Close()
	if _, err := io.WriteString(stream, "ok\n"); err != nil {
		return
	}
	// Anything the cloud sent after the request line is already buffered.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(dev, br)
		if cw, ok := dev.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	_, _ = io.Copy(stream, dev)
	_ = stream.Close()
	wg.Wait()
}

// target is the host:port to dial for a request: the device's own host, on
// a web port, its control port, or its web_port tag. Nothing else.
func (c *Client) target(req request) (string, error) {
	cfg, ok := c.devices.DeviceConfig(req.Device)
	if !ok {
		return "", fmt.Errorf("device %q isn't on this collector", req.Device)
	}
	host, ownPort := splitAddress(cfg.Address)
	if host == "" {
		return "", fmt.Errorf("device %q has no network address", req.Device)
	}
	allowed := webPorts[req.Port] || (ownPort != 0 && req.Port == ownPort)
	if p, err := strconv.Atoi(cfg.Tags["web_port"]); err == nil && p == req.Port {
		allowed = true
	}
	if !allowed || req.Port <= 0 || req.Port > 65535 {
		return "", fmt.Errorf("port %d isn't allowed for this device", req.Port)
	}
	return net.JoinHostPort(host, strconv.Itoa(req.Port)), nil
}

// splitAddress returns the host and, when present, the port of a device
// address, which may be a bare host, host:port, or a URL.
func splitAddress(addr string) (string, int) {
	addr = strings.TrimSpace(addr)
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil {
			return "", 0
		}
		p, _ := strconv.Atoi(u.Port())
		return u.Hostname(), p
	}
	if h, p, err := net.SplitHostPort(addr); err == nil {
		port, _ := strconv.Atoi(p)
		return h, port
	}
	return strings.Trim(addr, "[]"), 0
}
