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

// TelnetAdapter communicates with legacy AV devices over raw TCP/Telnet.
// It maintains a persistent connection and reads responses line-by-line.
type TelnetAdapter struct {
	device.Base
	conn    net.Conn
	reader  *bufio.Reader
	connMu  sync.Mutex
	lastRaw string
	lastMu  sync.RWMutex
}

func NewTelnetAdapter(cfg config.DeviceConfig) *TelnetAdapter {
	return &TelnetAdapter{
		Base: device.NewBase(cfg),
	}
}

func (a *TelnetAdapter) Connect(ctx context.Context) error {
	a.connMu.Lock()
	defer a.connMu.Unlock()

	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", a.Cfg.Address)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("telnet connect %s (%s): %w", a.Cfg.ID, a.Cfg.Address, err)
	}
	a.conn = conn
	a.reader = bufio.NewReader(conn)
	a.SetStatus(device.StatusOnline)

	// Send credentials if set
	if a.Cfg.Username != "" {
		_ = a.writeLine(a.Cfg.Username)
		time.Sleep(200 * time.Millisecond)
		_ = a.writeLine(a.Cfg.Password)
		time.Sleep(200 * time.Millisecond)
	}

	go a.readLoop(ctx)
	return nil
}

func (a *TelnetAdapter) Disconnect() error {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	a.SetStatus(device.StatusOffline)
	if a.conn != nil {
		return a.conn.Close()
	}
	return nil
}

func (a *TelnetAdapter) readLoop(ctx context.Context) {
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

		_ = a.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := reader.ReadString('\n')
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			slog.Warn("telnet read error", "device", a.Cfg.ID, "error", err)
			a.SetStatus(device.StatusOffline)
			return
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		a.lastMu.Lock()
		a.lastRaw = line
		a.lastMu.Unlock()

		a.Emit(&device.Event{
			DeviceID:   a.Cfg.ID,
			DeviceName: a.Cfg.Name,
			DeviceType: a.Cfg.Type,
			EventType:  "telnet_message",
			Payload:    map[string]any{"raw": line},
			Timestamp:  time.Now().UTC(),
		})
	}
}

func (a *TelnetAdapter) Poll(_ context.Context) (*device.Telemetry, error) {
	t := a.BaseTelemetry()
	a.lastMu.RLock()
	last := a.lastRaw
	a.lastMu.RUnlock()
	if last != "" {
		t.Metrics = map[string]any{"last_message": last}
	}
	return t, nil
}

func (a *TelnetAdapter) SendCommand(ctx context.Context, cmd device.CommandRequest) (*device.CommandResponse, error) {
	a.connMu.Lock()
	conn := a.conn
	a.connMu.Unlock()

	if conn == nil {
		return nil, fmt.Errorf("device %s not connected", a.Cfg.ID)
	}

	raw, ok := a.Cfg.Commands[cmd.Name]
	if !ok {
		raw = cmd.Name
	}

	start := time.Now()
	if err := a.writeLine(raw); err != nil {
		a.SetStatus(device.StatusOffline)
		return nil, fmt.Errorf("telnet write: %w", err)
	}

	// Read response with timeout
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	a.connMu.Lock()
	resp, _ := a.reader.ReadString('\n')
	a.connMu.Unlock()
	resp = strings.TrimSpace(resp)

	return &device.CommandResponse{
		Raw:     resp,
		Latency: time.Since(start),
	}, nil
}

func (a *TelnetAdapter) writeLine(s string) error {
	if !strings.HasSuffix(s, "\r\n") {
		s += "\r\n"
	}
	_, err := fmt.Fprint(a.conn, s)
	return err
}
