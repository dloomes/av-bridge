package adapters

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
	"go.bug.st/serial"
)

// SerialAdapter communicates with AV devices over RS-232/serial.
type SerialAdapter struct {
	device.Base
	port    serial.Port
	reader  *bufio.Reader
	portMu  sync.Mutex
	lastRaw string
	lastMu  sync.RWMutex
}

func NewSerialAdapter(cfg config.DeviceConfig) *SerialAdapter {
	return &SerialAdapter{
		Base: device.NewBase(cfg),
	}
}

func (a *SerialAdapter) Connect(_ context.Context) error {
	a.portMu.Lock()
	defer a.portMu.Unlock()

	mode := &serial.Mode{
		BaudRate: a.Cfg.BaudRate,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}

	port, err := serial.Open(a.Cfg.Address, mode)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("serial open %s (%s): %w", a.Cfg.ID, a.Cfg.Address, err)
	}

	a.port = port
	a.reader = bufio.NewReader(port)
	a.SetStatus(device.StatusOnline)

	go a.readLoop()
	return nil
}

func (a *SerialAdapter) Disconnect() error {
	a.portMu.Lock()
	defer a.portMu.Unlock()
	a.SetStatus(device.StatusOffline)
	if a.port != nil {
		return a.port.Close()
	}
	return nil
}

func (a *SerialAdapter) readLoop() {
	for {
		a.portMu.Lock()
		reader := a.reader
		a.portMu.Unlock()
		if reader == nil {
			return
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			slog.Warn("serial read error", "device", a.Cfg.ID, "error", err)
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
			EventType:  "serial_message",
			Payload:    map[string]any{"raw": line},
			Timestamp:  time.Now().UTC(),
		})
	}
}

func (a *SerialAdapter) Poll(_ context.Context) (*device.Telemetry, error) {
	t := a.BaseTelemetry()
	a.lastMu.RLock()
	last := a.lastRaw
	a.lastMu.RUnlock()
	if last != "" {
		t.Metrics = map[string]any{"last_message": last}
	}
	return t, nil
}

func (a *SerialAdapter) SendCommand(_ context.Context, cmd device.CommandRequest) (*device.CommandResponse, error) {
	a.portMu.Lock()
	port := a.port
	a.portMu.Unlock()

	if port == nil {
		return nil, fmt.Errorf("device %s not connected", a.Cfg.ID)
	}

	raw, ok := a.Cfg.Commands[cmd.Name]
	if !ok {
		raw = cmd.Name
	}
	if !strings.HasSuffix(raw, "\r\n") {
		raw += "\r\n"
	}

	start := time.Now()
	_, err := fmt.Fprint(port, raw)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return nil, fmt.Errorf("serial write: %w", err)
	}

	// Brief wait then read response
	time.Sleep(100 * time.Millisecond)
	a.portMu.Lock()
	resp, _ := a.reader.ReadString('\n')
	a.portMu.Unlock()
	resp = strings.TrimSpace(resp)

	return &device.CommandResponse{
		Raw:     resp,
		Latency: time.Since(start),
	}, nil
}
