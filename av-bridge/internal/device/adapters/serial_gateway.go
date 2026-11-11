package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// Serial through a gateway: a device wired to an RS-232 port of an Aurora
// RXT touch panel, reached through the RXT's serial-over-IP (SOIP) TCP port
// instead of its own network port. The device's own adapter (NEC projector,
// generic Telnet) speaks its usual protocol; only the transport changes.
//
// Set on the device:
//
//	address:                     the RXT's IP or hostname
//	tags.via_rxt_serial:         1 or 2, the RXT serial port it's wired to
//	tags.soip_tcp_port:          the TCP port the RXT opens for it
//	                             (default 6900 for port 1, 6901 for port 2)
//	tags.serial_settings:        optional "baud data stop [parity handshaking]",
//	                             e.g. "9600 8 1 n none"; applied with config_rs232.
//	                             Left alone when not set, so a port someone
//	                             else configured keeps its settings.
//
// Before each connect the RXT is asked (RXT Control API, TCP 6975) to open
// SOIP on that serial port and TCP port. It answers with the TCP port the
// serial port is already shared on if it is; a different one is refused
// with a clear error rather than taken over. Reference: RXT-4/8 Control
// Application API 1.0.3, enable_rs232 / config_rs232.

const (
	tagViaRXTSerial   = "via_rxt_serial"
	tagSOIPTCPPort    = "soip_tcp_port"
	tagSerialSettings = "serial_settings"
	soipDefaultBase   = 6900
)

// serialGatewayProtocols are the adapters whose LAN protocol is the same as
// their RS-232 protocol, so they can run over the raw serial bridge.
var serialGatewayProtocols = map[string]bool{
	"nec_projector": true,
	"telnet":        true,
}

type serialGateway struct {
	apiAddr  string // RXT control API, host:6975
	dataAddr string // the SOIP port, host:<tcpPort>
	port     int    // RXT serial port, 1 or 2
	tcpPort  int
	settings string
}

// serialGatewayFromConfig returns the gateway a device's tags ask for, or
// nil when the device isn't wired through one.
func serialGatewayFromConfig(cfg config.DeviceConfig) (*serialGateway, error) {
	raw := strings.TrimSpace(cfg.Tags[tagViaRXTSerial])
	if raw == "" {
		return nil, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 2 {
		return nil, fmt.Errorf("%s must be 1 or 2, got %q", tagViaRXTSerial, raw)
	}
	if !serialGatewayProtocols[cfg.Protocol] {
		return nil, fmt.Errorf("%s can't run through an RXT serial port (supported: nec_projector, telnet)", cfg.Protocol)
	}
	host := strings.TrimSpace(cfg.Address)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		return nil, fmt.Errorf("address must be the RXT's IP or hostname")
	}
	tcpPort := soipDefaultBase + port - 1
	if v := strings.TrimSpace(cfg.Tags[tagSOIPTCPPort]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("%s must be a TCP port, got %q", tagSOIPTCPPort, v)
		}
		tcpPort = n
	}
	return &serialGateway{
		apiAddr:  net.JoinHostPort(host, strconv.Itoa(auroraDefaultPort)),
		dataAddr: net.JoinHostPort(host, strconv.Itoa(tcpPort)),
		port:     port,
		tcpPort:  tcpPort,
		settings: strings.Join(strings.Fields(cfg.Tags[tagSerialSettings]), " "),
	}, nil
}

// open asks the RXT to share the serial port on the SOIP TCP port, applying
// serial_settings first when set.
func (g *serialGateway) open(ctx context.Context) error {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", g.apiAddr)
	if err != nil {
		return fmt.Errorf("RXT control API %s: %w", g.apiAddr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	dec := json.NewDecoder(bufio.NewReader(newTelnetFilter(conn)))
	exec := func(cmd string) (map[string]any, error) {
		if _, err := conn.Write([]byte(cmd + "\r\n")); err != nil {
			return nil, fmt.Errorf("write %q: %w", cmd, err)
		}
		var resp map[string]any
		if err := dec.Decode(&resp); err != nil {
			return nil, fmt.Errorf("reply to %q: %w", cmd, err)
		}
		return resp, nil
	}

	if g.settings != "" {
		r, err := exec(fmt.Sprintf("config_rs232 %d %s", g.port, g.settings))
		if err != nil {
			return err
		}
		if s, _ := r["status"].(string); s != "SUCCESS" {
			return fmt.Errorf("RXT refused serial settings %q for port %d: %v", g.settings, g.port, r["error"])
		}
	}

	r, err := exec(fmt.Sprintf("enable_rs232 %d soip %d", g.port, g.tcpPort))
	if err != nil {
		return err
	}
	if s, _ := r["status"].(string); s != "SUCCESS" {
		msg := fmt.Sprintf("RXT refused to share serial port %d on TCP %d: %v", g.port, g.tcpPort, r["error"])
		if e, _ := r["error"].(string); e == "INVALID_STATE" {
			msg += fmt.Sprintf(" (TCP %d is already in use on the RXT; set %s to another port)", g.tcpPort, tagSOIPTCPPort)
		}
		return fmt.Errorf("%s", msg)
	}
	if existing := soipPortOf(r); existing != 0 && existing != g.tcpPort {
		return fmt.Errorf("RXT serial port %d is already shared on TCP %d; set %s to %d to use it", g.port, existing, tagSOIPTCPPort, existing)
	}
	return nil
}

// soipPortOf reads the soip_port an enable_rs232 reply carries when SOIP was
// already on; 0 when absent.
func soipPortOf(r map[string]any) int {
	switch v := r["soip_port"].(type) {
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	case float64:
		return int(v)
	}
	return 0
}

// gatewayOpener is a serial gateway that can be asked to share its port
// before the device's adapter dials it.
type gatewayOpener interface {
	open(ctx context.Context) error
}

// gatewayFromConfig returns the gateway a device's tags ask for (an RXT or a
// VPX serial port) and the address its adapter should dial, or nil.
func gatewayFromConfig(cfg config.DeviceConfig) (gatewayOpener, string, error) {
	rxt := strings.TrimSpace(cfg.Tags[tagViaRXTSerial]) != ""
	vpx := strings.TrimSpace(cfg.Tags[tagViaVPXSerial]) != ""
	if rxt && vpx {
		return nil, "", fmt.Errorf("set %s or %s, not both", tagViaRXTSerial, tagViaVPXSerial)
	}
	if rxt {
		gw, err := serialGatewayFromConfig(cfg)
		if err != nil {
			return nil, "", err
		}
		return gw, gw.dataAddr, nil
	}
	if vpx {
		gw, err := vpxSerialGatewayFromConfig(cfg)
		if err != nil || gw == nil {
			return nil, "", err
		}
		return gw, gw.dataAddr, nil
	}
	return nil, "", nil
}

// gatewayDevice runs a device's own adapter over the gateway's SOIP port.
type gatewayDevice struct {
	device.Device
	orig config.DeviceConfig
	gw   gatewayOpener
}

// Info reports the device as configured (the RXT's address), not the
// rewritten SOIP address its adapter dials.
func (g *gatewayDevice) Info() config.DeviceConfig { return g.orig }

func (g *gatewayDevice) Connect(ctx context.Context) error {
	if err := g.gw.open(ctx); err != nil {
		return err
	}
	return g.Device.Connect(ctx)
}

// Heartbeat and DefaultPollRate pass through to the adapter, which the
// embedded interface alone wouldn't expose.
func (g *gatewayDevice) Heartbeat(ctx context.Context) error {
	if hb, ok := g.Device.(device.Heartbeater); ok {
		return hb.Heartbeat(ctx)
	}
	return nil
}

func (g *gatewayDevice) DefaultPollRate() time.Duration {
	if pd, ok := g.Device.(device.PollRateDefaulter); ok {
		return pd.DefaultPollRate()
	}
	return device.DefaultPollRate
}
