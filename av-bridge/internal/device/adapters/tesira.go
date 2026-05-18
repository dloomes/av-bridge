package adapters

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// TesiraAdapter communicates with Biamp Tesira DSPs using the Tesira Text
// Protocol (TTP) over a persistent TCP/Telnet connection.
//
// It extends the basic Telnet approach with two Tesira-specific behaviours:
//  1. TTP subscription — on connect it subscribes to configured DSP block
//     attributes (mute state, gain level, call status). The Tesira pushes
//     notifications whenever a value changes, so av-bridge receives events
//     in real-time without needing to poll every few seconds.
//  2. TTP response parsing — responses follow a structured format
//     (+OK, -ERR, or ! for subscription notifications) that the adapter
//     parses and routes appropriately.
//
// Config tags used:
//   - mute_instance_tag:  name of the mute DSP block (e.g. "MicMuteLevel1")
//   - gain_instance_tag:  name of the gain DSP block (e.g. "ProgramLevel1")
//   - call_instance_tag:  name of the VoIP/POTS dialler block (optional)
type TesiraAdapter struct {
	device.Base

	conn    net.Conn
	reader  *bufio.Reader
	connMu  sync.Mutex

	// Last parsed state from TTP responses and subscription notifications
	stateMu    sync.RWMutex
	lastMetrics map[string]any

	// Pending command response — single-request-at-a-time model matches TTP
	respCh chan string
}

func NewTesiraAdapter(cfg config.DeviceConfig) *TesiraAdapter {
	return &TesiraAdapter{
		Base:        device.NewBase(cfg),
		lastMetrics: map[string]any{},
		respCh:      make(chan string, 1),
	}
}

// ── Connection ────────────────────────────────────────────────────────────────

func (a *TesiraAdapter) Connect(ctx context.Context) error {
	log := slog.With("device", a.Cfg.ID, "address", a.Cfg.Address)

	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", a.Cfg.Address)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("tesira connect %s (%s): %w", a.Cfg.ID, a.Cfg.Address, err)
	}
	log.Info("tesira tcp connected")
	reader := bufio.NewReader(conn)

	// rawWrite operates on the local conn — the lock-managed a.conn isn't
	// published yet, so we avoid the writeLine() path (and its lock) here.
	rawWrite := func(s string) error {
		if !strings.HasSuffix(s, "\r\n") {
			s += "\r\n"
		}
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, err := fmt.Fprint(conn, s)
		return err
	}

	// Drain the welcome banner (Telnet negotiation + "Welcome to TTP server")
	// so it doesn't get mixed into the first command response.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break // timeout — banner fully consumed
		}
		log.Debug("tesira banner", "line", strings.TrimSpace(line))
	}
	_ = conn.SetReadDeadline(time.Time{})

	if a.Cfg.Username != "" {
		log.Info("tesira sending credentials", "user", a.Cfg.Username)
		if err := rawWrite(a.Cfg.Username); err != nil {
			conn.Close()
			a.SetStatus(device.StatusOffline)
			return fmt.Errorf("tesira auth username: %w", err)
		}
		time.Sleep(300 * time.Millisecond)
		if err := rawWrite(a.Cfg.Password); err != nil {
			conn.Close()
			a.SetStatus(device.StatusOffline)
			return fmt.Errorf("tesira auth password: %w", err)
		}
		time.Sleep(300 * time.Millisecond)
	}

	log.Info("tesira sending probe", "cmd", "DEVICE get serialNumber")
	if err := rawWrite("DEVICE get serialNumber"); err != nil {
		conn.Close()
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("tesira probe: %w", err)
	}

	// Publish the connection so reader/writer paths can use it.
	a.connMu.Lock()
	a.conn = conn
	a.reader = reader
	a.connMu.Unlock()

	a.SetStatus(device.StatusOnline)
	log.Info("tesira connected")

	// Read loop must start before subscriptions so their +OK acks are drained.
	go a.readLoop(ctx)

	a.sendSubscriptions()

	return nil
}

func (a *TesiraAdapter) Disconnect() error {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	a.SetStatus(device.StatusOffline)
	if a.conn != nil {
		return a.conn.Close()
	}
	return nil
}

// ── TTP subscription setup ────────────────────────────────────────────────────

// sendSubscriptions registers for push notifications from the Tesira.
// The Tesira will proactively send a message whenever the subscribed
// attribute changes — eliminating the need to poll for those values.
//
// TTP subscription syntax:
//   <InstanceTag> subscribe <Attribute> <Index> <CustomLabel> [<MinRate>]
func (a *TesiraAdapter) sendSubscriptions() {
	log := slog.With("device", a.Cfg.ID)

	if tag := a.Cfg.Tags["mute_instance_tag"]; tag != "" {
		cmd := fmt.Sprintf("%s subscribe mute 1 mute_state 500", tag)
		if err := a.writeLine(cmd); err != nil {
			log.Warn("tesira subscribe mute failed", "error", err)
		} else {
			log.Debug("subscribed to mute", "tag", tag)
		}
	}

	if tag := a.Cfg.Tags["gain_instance_tag"]; tag != "" {
		cmd := fmt.Sprintf("%s subscribe level 1 gain_level 500", tag)
		if err := a.writeLine(cmd); err != nil {
			log.Warn("tesira subscribe gain failed", "error", err)
		} else {
			log.Debug("subscribed to gain", "tag", tag)
		}
	}

	if tag := a.Cfg.Tags["call_instance_tag"]; tag != "" {
		cmd := fmt.Sprintf("%s subscribe callState 1 call_state 500", tag)
		if err := a.writeLine(cmd); err != nil {
			log.Warn("tesira subscribe call failed", "error", err)
		} else {
			log.Debug("subscribed to call state", "tag", tag)
		}
	}
}

// ── Read loop — parses TTP responses and subscription notifications ────────────

// TTP response formats:
//   +OK                        — success, no value returned
//   +OK "value"                — success with a value
//   +OK {"key":"val",...}      — success with JSON object (networkInfo etc.)
//   -ERR <code> <message>      — error response
//   ! <CustomLabel> <value>    — subscription notification (push)
func (a *TesiraAdapter) readLoop(ctx context.Context) {
	log := slog.With("device", a.Cfg.ID)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		a.connMu.Lock()
		reader := a.reader
		a.connMu.Unlock()
		if reader == nil {
			return
		}

		_ = a.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		line, err := reader.ReadString('\n')
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Warn("tesira read error", "error", err)
			a.SetStatus(device.StatusOffline)
			return
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		log.Debug("tesira rx", "line", line)

		switch {
		case strings.HasPrefix(line, "! "):
			// Subscription notification — parse and emit as event
			a.HandleSubscriptionNotification(line)

		case strings.HasPrefix(line, "+OK") || strings.HasPrefix(line, "-ERR"):
			// Command response — forward to waiting SendCommand call
			select {
			case a.respCh <- line:
			default:
				// No one waiting — store as last metric anyway
				a.stateMu.Lock()
				a.lastMetrics["last_response"] = line
				a.stateMu.Unlock()
			}

		default:
			// Informational line (banner, prompt etc.) — log and ignore
			log.Debug("tesira info line", "line", line)
		}
	}
}

// handleSubscriptionNotification parses lines like:
//   ! mute_state true
//   ! gain_level -10.0
//   ! call_state "Init"
func (a *TesiraAdapter) HandleSubscriptionNotification(line string) {
	// Strip leading "! "
	content := strings.TrimPrefix(line, "! ")
	parts := strings.SplitN(content, " ", 2)
	if len(parts) != 2 {
		return
	}

	label := parts[0]
	value := strings.Trim(parts[1], "\"")

	// Update cached metrics
	a.stateMu.Lock()
	a.lastMetrics[label] = value
	a.stateMu.Unlock()

	// Emit as a device event so the hub forwards it to the cloud
	a.Emit(&device.Event{
		DeviceID:   a.Cfg.ID,
		DeviceName: a.Cfg.Name,
		DeviceType: a.Cfg.Type,
		EventType:  "tesira_subscription:" + label,
		Payload: map[string]any{
			"label": label,
			"value": value,
			"raw":   line,
		},
		Timestamp: time.Now().UTC(),
	})

	slog.Debug("tesira subscription update",
		"device", a.Cfg.ID,
		"label", label,
		"value", value)
}

// ── Poll ──────────────────────────────────────────────────────────────────────

// Poll sends a TTP probe command and merges the response with any cached
// subscription state. Subscription-based attributes (mute, gain, call state)
// are served from the cached state updated by the read loop, so the probe
// only needs to confirm the session is still alive.
func (a *TesiraAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	resp, err := a.sendAndReceive(ctx, "DEVICE get serialNumber", 3*time.Second)

	t := a.BaseTelemetry()

	a.stateMu.RLock()
	metrics := make(map[string]any, len(a.lastMetrics))
	for k, v := range a.lastMetrics {
		metrics[k] = v
	}
	a.stateMu.RUnlock()

	if err != nil {
		a.SetStatus(device.StatusDegraded)
		t.Error = err.Error()
		metrics["poll_error"] = err.Error()
	} else {
		switch {
		case strings.HasPrefix(resp, "+OK"):
			// "+OK \"H123456\"" — strip the prefix and surrounding quotes.
			val := strings.TrimSpace(strings.TrimPrefix(resp, "+OK"))
			val = strings.Trim(val, "\"")
			if val != "" {
				metrics["serial_number"] = val
			}
			a.SetStatus(device.StatusOnline)
		case strings.HasPrefix(resp, "-ERR"):
			a.SetStatus(device.StatusDegraded)
			metrics["ttp_error"] = resp
		}
		metrics["last_poll"] = time.Now().UTC().Format(time.RFC3339)
	}

	t.Metrics = metrics
	t.Status = a.Status()
	return t, nil
}

// ── SendCommand ───────────────────────────────────────────────────────────────

// SendCommand translates a named command from config.yaml into a TTP string
// and sends it to the Tesira, then waits for the response.
//
// Named commands are looked up in config.yaml Commands map first.
// If not found, the command Name is sent as a raw TTP string — useful for
// ad-hoc commands during testing.
func (a *TesiraAdapter) SendCommand(ctx context.Context, req device.CommandRequest) (*device.CommandResponse, error) {
	// Resolve the TTP command string
	ttp, ok := a.Cfg.Commands[req.Name]
	if !ok {
		// Allow raw TTP strings as the command name for flexibility
		ttp = req.Name
	}

	// Replace any args placeholders — e.g. "level" arg for gain commands
	// Args can contain: level, channel, value
	for k, v := range req.Args {
		ttp = strings.ReplaceAll(ttp, "{"+k+"}", fmt.Sprintf("%v", v))
	}

	start := time.Now()
	resp, err := a.sendAndReceive(ctx, ttp, 5*time.Second)
	if err != nil {
		return nil, err
	}

	parsed := map[string]any{"ttp_response": resp}
	if strings.HasPrefix(resp, "+OK") {
		parsed["success"] = true
		val := strings.TrimPrefix(resp, "+OK ")
		if val != "" {
			parsed["value"] = strings.Trim(val, "\"")
		}
	} else if strings.HasPrefix(resp, "-ERR") {
		parsed["success"] = false
		parsed["error"] = resp
	}

	return &device.CommandResponse{
		Raw:     resp,
		Parsed:  parsed,
		Latency: time.Since(start),
	}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// sendAndReceive writes a TTP command and blocks until a response is received
// or the timeout elapses. TTP is strictly sequential — one command at a time.
func (a *TesiraAdapter) sendAndReceive(ctx context.Context, cmd string, timeout time.Duration) (string, error) {
	a.connMu.Lock()
	conn := a.conn
	a.connMu.Unlock()

	if conn == nil {
		return "", fmt.Errorf("device %s not connected", a.Cfg.ID)
	}

	// Drain any stale response
	select {
	case <-a.respCh:
	default:
	}

	if err := a.writeLine(cmd); err != nil {
		a.SetStatus(device.StatusOffline)
		return "", fmt.Errorf("tesira write: %w", err)
	}

	select {
	case resp := <-a.respCh:
		return resp, nil
	case <-time.After(timeout):
		return "", fmt.Errorf("tesira timeout waiting for response to: %q", cmd)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (a *TesiraAdapter) writeLine(s string) error {
	if !strings.HasSuffix(s, "\r\n") {
		s += "\r\n"
	}
	a.connMu.Lock()
	defer a.connMu.Unlock()
	if a.conn == nil {
		return fmt.Errorf("not connected")
	}
	_ = a.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := fmt.Fprint(a.conn, s)
	return err
}
