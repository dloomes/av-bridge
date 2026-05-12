package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// RESTAdapter communicates with devices that expose an HTTP REST API
type RESTAdapter struct {
	device.Base
	client  *http.Client
	baseURL string
}

func NewRESTAdapter(cfg config.DeviceConfig) *RESTAdapter {
	base := "http://" + cfg.Address
	if strings.HasPrefix(cfg.Address, "http") {
		base = cfg.Address
	}
	return &RESTAdapter{
		Base:    device.NewBase(cfg),
		baseURL: strings.TrimRight(base, "/"),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (a *RESTAdapter) Connect(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/status", nil)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("rest connect %s: %w", a.Cfg.ID, err)
	}
	a.injectAuth(req)
	resp, err := a.client.Do(req)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("rest connect %s: %w", a.Cfg.ID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		a.SetStatus(device.StatusDegraded)
		return fmt.Errorf("rest connect %s: HTTP %d", a.Cfg.ID, resp.StatusCode)
	}
	a.SetStatus(device.StatusOnline)
	return nil
}

func (a *RESTAdapter) Disconnect() error {
	a.SetStatus(device.StatusOffline)
	return nil
}

func (a *RESTAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	start := time.Now()
	path := "/status"
	if custom, ok := a.Cfg.Commands["poll"]; ok {
		path = custom
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	a.injectAuth(req)

	resp, err := a.client.Do(req)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		t := a.BaseTelemetry()
		t.Error = err.Error()
		return t, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	metrics := map[string]any{}
	_ = json.Unmarshal(body, &metrics)

	t := a.BaseTelemetry()
	t.Metrics = metrics
	t.Metrics["http_status"] = resp.StatusCode
	t.Metrics["response_ms"] = time.Since(start).Milliseconds()

	if resp.StatusCode >= 400 {
		a.SetStatus(device.StatusDegraded)
		t.Status = device.StatusDegraded
	} else {
		a.SetStatus(device.StatusOnline)
	}
	return t, nil
}

func (a *RESTAdapter) SendCommand(ctx context.Context, cmd device.CommandRequest) (*device.CommandResponse, error) {
	start := time.Now()

	path, ok := a.Cfg.Commands[cmd.Name]
	if !ok {
		path = "/" + cmd.Name
	}

	method := http.MethodPost
	var body io.Reader
	if len(cmd.Args) > 0 {
		b, _ := json.Marshal(cmd.Args)
		body = strings.NewReader(string(b))
	}

	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	a.injectAuth(req)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending command: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	parsed := map[string]any{}
	_ = json.Unmarshal(raw, &parsed)
	parsed["http_status"] = resp.StatusCode

	return &device.CommandResponse{
		Raw:     string(raw),
		Parsed:  parsed,
		Latency: time.Since(start),
	}, nil
}

func (a *RESTAdapter) injectAuth(req *http.Request) {
	if a.Cfg.Username != "" {
		req.SetBasicAuth(a.Cfg.Username, a.Cfg.Password)
	}
	if token := a.Cfg.Tags["auth_token"]; token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}
