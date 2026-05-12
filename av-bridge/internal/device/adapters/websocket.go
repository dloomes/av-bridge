package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
	"github.com/gorilla/websocket"
)

// WebSocketAdapter communicates with devices over a persistent WebSocket connection.
// It receives async events from the device and can send commands.
type WebSocketAdapter struct {
	device.Base
	conn     *websocket.Conn
	connMu   sync.Mutex
	lastMsg  map[string]any
	lastMsgMu sync.RWMutex
}

func NewWebSocketAdapter(cfg config.DeviceConfig) *WebSocketAdapter {
	return &WebSocketAdapter{
		Base:    device.NewBase(cfg),
		lastMsg: map[string]any{},
	}
}

func (a *WebSocketAdapter) wsURL() string {
	addr := a.Cfg.Address
	if strings.HasPrefix(addr, "ws") {
		return addr
	}
	return "ws://" + addr
}

func (a *WebSocketAdapter) Connect(ctx context.Context) error {
	a.connMu.Lock()
	defer a.connMu.Unlock()

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, a.wsURL(), nil)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("ws connect %s: %w", a.Cfg.ID, err)
	}
	a.conn = conn
	a.SetStatus(device.StatusOnline)

	// Authenticate if credentials provided
	if a.Cfg.Username != "" {
		authMsg := map[string]any{
			"type":     "auth",
			"username": a.Cfg.Username,
			"password": a.Cfg.Password,
		}
		if b, err := json.Marshal(authMsg); err == nil {
			_ = conn.WriteMessage(websocket.TextMessage, b)
		}
	}

	go a.readLoop(ctx)
	return nil
}

func (a *WebSocketAdapter) Disconnect() error {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	a.SetStatus(device.StatusOffline)
	if a.conn != nil {
		return a.conn.Close()
	}
	return nil
}

func (a *WebSocketAdapter) readLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		a.connMu.Lock()
		conn := a.conn
		a.connMu.Unlock()

		if conn == nil {
			return
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				slog.Warn("ws read error", "device", a.Cfg.ID, "error", err)
			}
			a.SetStatus(device.StatusOffline)
			return
		}

		payload := map[string]any{}
		if err := json.Unmarshal(msg, &payload); err == nil {
			a.lastMsgMu.Lock()
			a.lastMsg = payload
			a.lastMsgMu.Unlock()

			// Emit as an event
			a.Emit(&device.Event{
				DeviceID:   a.Cfg.ID,
				DeviceName: a.Cfg.Name,
				DeviceType: a.Cfg.Type,
				EventType:  "ws_message",
				Payload:    payload,
				Timestamp:  time.Now().UTC(),
			})
		}
	}
}

func (a *WebSocketAdapter) Poll(_ context.Context) (*device.Telemetry, error) {
	t := a.BaseTelemetry()
	a.lastMsgMu.RLock()
	for k, v := range a.lastMsg {
		if t.Metrics == nil {
			t.Metrics = map[string]any{}
		}
		t.Metrics[k] = v
	}
	a.lastMsgMu.RUnlock()
	return t, nil
}

func (a *WebSocketAdapter) SendCommand(_ context.Context, cmd device.CommandRequest) (*device.CommandResponse, error) {
	a.connMu.Lock()
	conn := a.conn
	a.connMu.Unlock()

	if conn == nil {
		return nil, fmt.Errorf("device %s not connected", a.Cfg.ID)
	}

	// Check for a raw command string override
	raw, ok := a.Cfg.Commands[cmd.Name]
	var payload []byte
	if ok {
		payload = []byte(raw)
	} else {
		msg := map[string]any{
			"command": cmd.Name,
			"args":    cmd.Args,
		}
		var err error
		payload, err = json.Marshal(msg)
		if err != nil {
			return nil, fmt.Errorf("marshalling command: %w", err)
		}
	}

	start := time.Now()
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		a.SetStatus(device.StatusOffline)
		return nil, fmt.Errorf("ws write: %w", err)
	}

	return &device.CommandResponse{
		Raw:     string(payload),
		Latency: time.Since(start),
	}, nil
}
