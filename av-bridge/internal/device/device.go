package device

import (
	"context"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
)

// Status represents the connection/health state of a device
type Status string

const (
	StatusOnline   Status = "online"
	StatusOffline  Status = "offline"
	StatusDegraded Status = "degraded"
	StatusUnknown  Status = "unknown"
)

// Telemetry is a full snapshot of device state, pushed to the cloud.
//
// Metrics are split by source so consumers can render them under distinct
// headings: Metrics carries values read directly from the device, while
// LensMetrics carries values fetched from Poly Lens (or any future external
// inventory source). Adapters without a secondary source leave LensMetrics nil.
type Telemetry struct {
	DeviceID    string            `json:"device_id"`
	DeviceName  string            `json:"device_name"`
	DeviceType  string            `json:"device_type"`
	Location    string            `json:"location,omitempty"`
	Protocol    string            `json:"protocol"`
	Status      Status            `json:"status"`
	Timestamp   time.Time         `json:"timestamp"`
	Metrics     map[string]any    `json:"metrics,omitempty"`
	LensMetrics map[string]any    `json:"lens_metrics,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	Error       string            `json:"error,omitempty"`
}

// Event is an async notification pushed from a device (input, state change, alert)
type Event struct {
	DeviceID   string         `json:"device_id"`
	DeviceName string         `json:"device_name"`
	DeviceType string         `json:"device_type"`
	EventType  string         `json:"event_type"`
	Payload    map[string]any `json:"payload,omitempty"`
	Timestamp  time.Time      `json:"timestamp"`
}

// CommandRequest is sent to a device
type CommandRequest struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

// CommandResponse is returned from a device after a command
type CommandResponse struct {
	Raw     string         `json:"raw"`
	Parsed  map[string]any `json:"parsed,omitempty"`
	Latency time.Duration  `json:"latency_ms"`
}

// Device is the interface every AV device adapter must satisfy
type Device interface {
	ID() string
	Info() config.DeviceConfig
	Connect(ctx context.Context) error
	Disconnect() error
	Poll(ctx context.Context) (*Telemetry, error)
	SendCommand(ctx context.Context, req CommandRequest) (*CommandResponse, error)
	Events() <-chan *Event
	Status() Status
}

// -------------------------------------------------------------------
// Base — shared state embedded in every adapter
// -------------------------------------------------------------------

type Base struct {
	Cfg      config.DeviceConfig
	status   Status
	mu       sync.RWMutex
	eventsCh chan *Event
}

func NewBase(cfg config.DeviceConfig) Base {
	return Base{
		Cfg:      cfg,
		status:   StatusUnknown,
		eventsCh: make(chan *Event, 128),
	}
}

func (b *Base) ID() string                { return b.Cfg.ID }
func (b *Base) Info() config.DeviceConfig { return b.Cfg }
func (b *Base) Events() <-chan *Event      { return b.eventsCh }

func (b *Base) Status() Status {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.status
}

func (b *Base) SetStatus(s Status) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status = s
}

func (b *Base) Emit(e *Event) {
	select {
	case b.eventsCh <- e:
	default:
		// drop if consumer is slow
	}
}

func (b *Base) BaseTelemetry() *Telemetry {
	return &Telemetry{
		DeviceID:   b.Cfg.ID,
		DeviceName: b.Cfg.Name,
		DeviceType: b.Cfg.Type,
		Location:   b.Cfg.Location,
		Protocol:   b.Cfg.Protocol,
		Status:     b.Status(),
		Timestamp:  time.Now().UTC(),
		Tags:       b.Cfg.Tags,
	}
}
