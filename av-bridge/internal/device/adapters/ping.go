package adapters

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"runtime"
	"strings"
	"time"

	probing "github.com/prometheus-community/pro-bing"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// PingAdapter probes reachability of any host that answers ICMP echo.
// Useful for anything with no vendor API — network switches, PDUs,
// AV control processors, projector power packs — where the only
// question we can answer remotely is "is it responding?".
//
// Address is the target hostname or IP with no port; ICMP has no
// concept of port. Optional per-device tuning via device tags:
//
//	ping_count       int    number of echoes per Poll        (default 3)
//	ping_timeout_ms  int    per-echo timeout in milliseconds (default 1000)
//	ping_interval_ms int    gap between echoes in ms         (default 100)
//	ping_privileged  bool   raw ICMP sockets (default true on Windows, false elsewhere)
//
// Linux note: unprivileged mode (the default) uses UDP sockets that
// the kernel translates to ICMP. It requires the running user's GID
// to be inside net.ipv4.ping_group_range, or the process needs
// CAP_NET_RAW. Systemd deployments should add AmbientCapabilities.
// Windows only supports privileged mode, so it's the default there; the
// collector's Windows service runs as LocalSystem, which has the rights.
type PingAdapter struct {
	device.Base
	count      int
	timeout    time.Duration
	interval   time.Duration
	privileged bool
}

func NewPingAdapter(cfg config.DeviceConfig) *PingAdapter {
	a := &PingAdapter{
		Base:       device.NewBase(cfg),
		count:      3,
		timeout:    time.Second,
		interval:   100 * time.Millisecond,
		privileged: runtime.GOOS == "windows",
	}
	if v := parseIntTag(cfg.Tags, "ping_count"); v > 0 {
		a.count = v
	}
	if v := parseIntTag(cfg.Tags, "ping_timeout_ms"); v > 0 {
		a.timeout = time.Duration(v) * time.Millisecond
	}
	// > 0, not >= 0: parseIntTag returns 0 for a missing tag, and a zero
	// interval makes pro-bing panic (NewTicker) and take the collector down.
	if v := parseIntTag(cfg.Tags, "ping_interval_ms"); v > 0 {
		a.interval = time.Duration(v) * time.Millisecond
	}
	switch cfg.Tags["ping_privileged"] {
	case "true":
		a.privileged = true
	case "false":
		a.privileged = false
	}
	return a
}

func (a *PingAdapter) Connect(ctx context.Context) error {
	stats, err := a.run(ctx, 1)
	if err != nil || stats.PacketsRecv == 0 {
		a.SetStatus(device.StatusOffline)
		if err != nil {
			return fmt.Errorf("ping connect %s: %w", a.Cfg.ID, err)
		}
		return fmt.Errorf("ping connect %s: no reply", a.Cfg.ID)
	}
	a.SetStatus(device.StatusOnline)
	return nil
}

func (a *PingAdapter) Disconnect() error {
	a.SetStatus(device.StatusOffline)
	return nil
}

func (a *PingAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	stats, err := a.run(ctx, a.count)
	t := a.BaseTelemetry()
	t.Metrics = map[string]any{}

	if err != nil {
		a.SetStatus(device.StatusOffline)
		t.Status = device.StatusOffline
		t.Error = err.Error()
		t.Metrics["reachable"] = false
		t.Metrics["packet_loss_pct"] = 100.0
		return t, nil
	}

	loss := stats.PacketLoss
	t.Metrics["reachable"] = stats.PacketsRecv > 0
	t.Metrics["packets_sent"] = stats.PacketsSent
	t.Metrics["packets_recv"] = stats.PacketsRecv
	t.Metrics["packet_loss_pct"] = round2(loss)
	if stats.PacketsRecv > 0 {
		t.Metrics["response_ms"] = stats.AvgRtt.Milliseconds()
		t.Metrics["min_ms"] = stats.MinRtt.Milliseconds()
		t.Metrics["max_ms"] = stats.MaxRtt.Milliseconds()
	}

	switch {
	case stats.PacketsRecv == 0:
		a.SetStatus(device.StatusOffline)
		t.Status = device.StatusOffline
	case loss > 0:
		// Partial packet loss — device is technically reachable but not
		// reliably so. Report online; the loss metric is on telemetry
		// for anyone who wants to alert on quality separately.
		a.SetStatus(device.StatusOnline)
		t.Status = device.StatusOnline
	default:
		a.SetStatus(device.StatusOnline)
		t.Status = device.StatusOnline
	}
	return t, nil
}

// SendCommand supports a single "ping" command that runs an ad-hoc
// probe and returns the raw stats. Everything else 404s — the point
// of a ping adapter is that the device has no real control surface.
func (a *PingAdapter) SendCommand(ctx context.Context, cmd device.CommandRequest) (*device.CommandResponse, error) {
	if cmd.Name != "ping" {
		return nil, fmt.Errorf("ping adapter: unsupported command %q (only 'ping' is supported)", cmd.Name)
	}
	start := time.Now()
	stats, err := a.run(ctx, a.count)
	if err != nil {
		return nil, err
	}
	parsed := map[string]any{
		"packets_sent":    stats.PacketsSent,
		"packets_recv":    stats.PacketsRecv,
		"packet_loss_pct": round2(stats.PacketLoss),
		"avg_ms":          stats.AvgRtt.Milliseconds(),
		"min_ms":          stats.MinRtt.Milliseconds(),
		"max_ms":          stats.MaxRtt.Milliseconds(),
	}
	return &device.CommandResponse{
		Raw:     fmt.Sprintf("%d/%d packets, avg %v", stats.PacketsRecv, stats.PacketsSent, stats.AvgRtt),
		Parsed:  parsed,
		Latency: time.Since(start),
	}, nil
}

// run drives pro-bing for the given count and returns its stats.
// A new Pinger per call is intentional — Pinger holds a socket and
// timers we don't want to keep hot between polls, and the setup
// cost (few ms) is trivial next to the network round-trip.
func (a *PingAdapter) run(ctx context.Context, count int) (*probing.Statistics, error) {
	return PingHost(ctx, a.Cfg.Address, PingOptions{Count: count, Timeout: a.timeout, Interval: a.interval, Privileged: a.privileged})
}

// PingOptions tunes PingHost. Zero values take the ping adapter's defaults.
type PingOptions struct {
	Count      int
	Timeout    time.Duration // per echo
	Interval   time.Duration
	Privileged bool
}

// DefaultPingOptions are the ping adapter's defaults, for pinging a host
// that isn't a ping device (a Room Readiness Ping step).
func DefaultPingOptions() PingOptions {
	return PingOptions{Count: 3, Timeout: time.Second, Interval: 100 * time.Millisecond, Privileged: runtime.GOOS == "windows"}
}

// PingHost sends ICMP echoes to host and returns the stats. No replies is
// not an error: the stats say so.
func PingHost(ctx context.Context, host string, o PingOptions) (*probing.Statistics, error) {
	d := DefaultPingOptions()
	if o.Count <= 0 {
		o.Count = d.Count
	}
	if o.Timeout <= 0 {
		o.Timeout = d.Timeout
	}
	if o.Interval <= 0 {
		o.Interval = d.Interval
	}
	pinger, err := probing.NewPinger(host)
	if err != nil {
		return nil, fmt.Errorf("resolving %q: %w", host, err)
	}
	pinger.Count = o.Count
	pinger.Timeout = o.Timeout * time.Duration(o.Count)
	pinger.Interval = o.Interval
	pinger.SetPrivileged(o.Privileged)

	done := make(chan error, 1)
	go func() { done <- pinger.RunWithContext(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		pinger.Stop()
		return nil, ctx.Err()
	}
	return pinger.Statistics(), nil
}

// HostFromAddress is the host part of a device address, whatever form the
// device's adapter takes it in: "10.0.0.5", "10.0.0.5:23", "[fe80::1]:80",
// "https://display.local/api". ok is false for addresses with no network
// host (a serial port).
func HostFromAddress(addr string) (host string, ok bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", false
	}
	if i := strings.Index(addr, "://"); i >= 0 {
		u, err := url.Parse(addr)
		if err != nil || u.Hostname() == "" {
			return "", false
		}
		return u.Hostname(), true
	}
	if h, _, err := net.SplitHostPort(addr); err == nil {
		addr = h
	}
	addr = strings.Trim(addr, "[]")
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		addr = addr[:i]
	}
	lower := strings.ToLower(addr)
	if addr == "" || strings.HasPrefix(lower, "com") && len(lower) <= 6 && isDigits(lower[3:]) {
		return "", false
	}
	return addr, true
}

// ── Capabilities ─────────────────────────────────────────────────────────────

var pingCapabilities = device.Capabilities{
	Power: device.PowerCapability{On: false, Off: false},
	Commands: []string{
		"ping",
	},
	Metrics: []string{
		"reachable",
		"packets_sent",
		"packets_recv",
		"packet_loss_pct",
		"response_ms",
		"min_ms",
		"max_ms",
	},
}

func (a *PingAdapter) Capabilities() device.Capabilities {
	return pingCapabilities
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func parseIntTag(tags map[string]string, key string) int {
	v, ok := tags[key]
	if !ok || v == "" {
		return 0
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
