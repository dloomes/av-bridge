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
)

// Serial through an Aurora VPX: a device wired to a VPX encoder's or
// decoder's RS-232 port, reached through the VPX's serial-over-IP (SOIP)
// passthrough TCP port. Same idea as the RXT gateway (serial_gateway.go).
//
// Set on the device:
//
//	address:               the VPX's IP or hostname
//	tags.via_vpx_serial:   true
//	tags.soip_tcp_port:    the TCP port the VPX shares its serial port on.
//	                       Required: the VPX Protocol Guide (v1.2.7) doesn't
//	                       say which port passthrough listens on.
//	tags.serial_settings:  optional "baud data stop parity" (e.g. "9600 8 1 n")
//	                       or the VPX's own "9600-8n1". When not set, the
//	                       serial port's current settings are kept.
//
// Before each connect the VPX API (TCP 6970) is read with `get settings`
// (streams.rs232). A port routed to another VPX (redirection) or with
// another client connected is refused, not taken over. SOIP is switched on
// in passthrough mode only when it's off or its settings need changing.

const tagViaVPXSerial = "via_vpx_serial"

type vpxSerialGateway struct {
	apiAddr  string // VPX API, host:6970
	dataAddr string // the SOIP passthrough port, host:<tcpPort>
	tcpPort  int
	settings string // VPX form, e.g. "9600-8n1"; "" keeps the current ones
}

func vpxSerialGatewayFromConfig(cfg config.DeviceConfig) (*vpxSerialGateway, error) {
	raw := strings.ToLower(strings.TrimSpace(cfg.Tags[tagViaVPXSerial]))
	switch raw {
	case "":
		return nil, nil
	case "true", "yes", "y", "1":
	default:
		return nil, fmt.Errorf("%s must be true, got %q", tagViaVPXSerial, raw)
	}
	if !serialGatewayProtocols[cfg.Protocol] {
		return nil, fmt.Errorf("%s can't run through a VPX serial port (supported: nec_projector, telnet)", cfg.Protocol)
	}
	host := strings.TrimSpace(cfg.Address)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		return nil, fmt.Errorf("address must be the VPX's IP or hostname")
	}
	v := strings.TrimSpace(cfg.Tags[tagSOIPTCPPort])
	if v == "" {
		return nil, fmt.Errorf("%s is required with %s: the TCP port the VPX shares its serial port on", tagSOIPTCPPort, tagViaVPXSerial)
	}
	tcpPort, err := strconv.Atoi(v)
	if err != nil || tcpPort < 1 || tcpPort > 65535 {
		return nil, fmt.Errorf("%s must be a TCP port, got %q", tagSOIPTCPPort, v)
	}
	settings, err := vpxSerialSettings(cfg.Tags[tagSerialSettings])
	if err != nil {
		return nil, err
	}
	return &vpxSerialGateway{
		apiAddr:  net.JoinHostPort(host, strconv.Itoa(auroraVPXDefaultPort)),
		dataAddr: net.JoinHostPort(host, strconv.Itoa(tcpPort)),
		tcpPort:  tcpPort,
		settings: settings,
	}, nil
}

// vpxSerialSettings turns "9600 8 1 n" (the RXT-style tag) or "9600-8n1"
// into the VPX's "9600-8n1". Empty stays empty.
func vpxSerialSettings(s string) (string, error) {
	f := strings.Fields(strings.ToLower(s))
	if len(f) == 0 {
		return "", nil
	}
	if len(f) == 1 && strings.Contains(f[0], "-") {
		baud, rest, _ := strings.Cut(f[0], "-")
		if len(rest) == 3 {
			return vpxFormat(baud, rest[0:1], rest[2:3], rest[1:2])
		}
		return "", fmt.Errorf("%s %q: want e.g. \"9600-8n1\"", tagSerialSettings, s)
	}
	if len(f) < 3 {
		return "", fmt.Errorf("%s %q: want \"baud data stop parity\", e.g. \"9600 8 1 n\"", tagSerialSettings, s)
	}
	parity := "n"
	if len(f) >= 4 {
		parity = f[3]
	}
	return vpxFormat(f[0], f[1], f[2], parity)
}

func vpxFormat(baud, data, stop, parity string) (string, error) {
	b, err := strconv.Atoi(baud)
	if err != nil || b <= 0 || b > 115200 {
		return "", fmt.Errorf("%s: baud %q (max 115200)", tagSerialSettings, baud)
	}
	if !strings.Contains("5678", data) || len(data) != 1 {
		return "", fmt.Errorf("%s: data bits %q (5-8)", tagSerialSettings, data)
	}
	if stop != "1" && stop != "2" {
		return "", fmt.Errorf("%s: stop bits %q (1 or 2)", tagSerialSettings, stop)
	}
	p := vpxParity(parity)
	if p == "" {
		return "", fmt.Errorf("%s: parity %q (n, e or o)", tagSerialSettings, parity)
	}
	return fmt.Sprintf("%d-%s%s%s", b, data, p, stop), nil
}

func vpxParity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "n", "none", "":
		return "n"
	case "e", "even":
		return "e"
	case "o", "odd":
		return "o"
	}
	return ""
}

func (g *vpxSerialGateway) open(ctx context.Context) error {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", g.apiAddr)
	if err != nil {
		return fmt.Errorf("VPX API %s: %w", g.apiAddr, err)
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

	resp, err := exec("get settings")
	if err != nil {
		return err
	}
	settings, _ := settingsOf(resp)
	streams, _ := settings["streams"].(map[string]any)
	rs, _ := streams["rs232"].(map[string]any)
	if rs == nil {
		return fmt.Errorf("VPX didn't report its serial port (no streams.rs232 in get settings)")
	}
	str := func(k string) string { return strings.ToLower(strings.TrimSpace(fmt.Sprint(rs[k]))) }

	on := str("soip_status") == "on"
	if on && str("mode") == "redirection" {
		return fmt.Errorf("the VPX's serial port is routed to another VPX (SOIP redirection); not taking it over")
	}
	if on && str("soip_connected") == "true" {
		other := strings.TrimSpace(fmt.Sprint(rs["soip_ip"]))
		if mine := localIP(conn); other == "" || other != mine {
			return fmt.Errorf("the VPX's serial port is in use by %s; not taking it over", orUnknown(other))
		}
	}

	want := g.settings
	current := ""
	if b := str("baudrate"); b != "" && b != "<nil>" {
		current, _ = vpxFormat(b, str("data_bit"), str("stop_bit"), str("parity"))
	}
	if on && (want == "" || want == current) {
		return nil // already shared as wanted
	}
	if want == "" {
		want = current
	}
	if want == "" {
		return fmt.Errorf("the VPX didn't report its serial settings; set %s", tagSerialSettings)
	}
	r, err := exec("set soip_enable y passthrough " + want)
	if err != nil {
		return err
	}
	if s, _ := r["status"].(string); s != "SUCCESS" {
		return fmt.Errorf("VPX refused to share its serial port (%s): %v", want, r["error"])
	}
	return nil
}

func localIP(c net.Conn) string {
	if a, ok := c.LocalAddr().(*net.TCPAddr); ok {
		return a.IP.String()
	}
	return ""
}

func orUnknown(s string) string {
	if s == "" || s == "<nil>" {
		return "another client"
	}
	return s
}
