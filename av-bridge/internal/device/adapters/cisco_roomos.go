package adapters

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// CiscoRoomOSAdapter talks to Cisco collaboration devices running RoomOS
// (Room Kit, Room Bar, Board, Desk and Codec series) over the xAPI HTTP
// interface. Reference: Cisco "RoomOS 11 API Reference Guide" (D15502),
// section "Using the HTTP XMLAPI".
//
//   - GET  /getxml?location=/Status — full status document, one per poll
//   - POST /putxml (Content-Type: text/xml) — xCommand bodies
//
// Authentication is HTTP Basic as a local user with the ADMIN role, sent on
// every request. RoomOS also offers cookie sessions (/xmlapi/session/begin)
// but they must be closed explicitly or the device runs out of them, and a
// Collector that crashes mid-session would leak one per restart. One
// status read per poll keeps the per-request re-authentication cost low,
// so the adapter stays stateless (and therefore needs no Heartbeat).
//
// Devices ship with a self-signed certificate; set tags.tls_skip_verify:
// "true" to accept it. HTTPS is the default and the only mode enabled out of
// the box (xConfiguration NetworkServices HTTP Mode).
type CiscoRoomOSAdapter struct {
	device.Base

	client  *http.Client
	baseURL string
}

func NewCiscoRoomOSAdapter(cfg config.DeviceConfig) *CiscoRoomOSAdapter {
	base := cfg.Address
	if !strings.HasPrefix(base, "http") {
		base = "https://" + base
	}
	base = strings.TrimRight(base, "/")
	skipVerify := cfg.Tags["tls_skip_verify"] == "true"
	return &CiscoRoomOSAdapter{
		Base:    device.NewBase(cfg),
		baseURL: base,
		client: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: skipVerify, //nolint:gosec // opt-in, for the device's self-signed cert
				},
			},
		},
	}
}

// ── Connection ────────────────────────────────────────────────────────────

// Connect proves the address, credentials and role are right by reading a
// small status node. The adapter holds no connection state beyond that.
func (a *CiscoRoomOSAdapter) Connect(ctx context.Context) error {
	if _, err := a.getXML(ctx, "/Status/SystemUnit/ProductId"); err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("cisco roomos connect %s: %w", a.Cfg.ID, err)
	}
	a.SetStatus(device.StatusOnline)
	slog.Info("cisco roomos connected", "device", a.Cfg.ID, "address", a.Cfg.Address)
	return nil
}

func (a *CiscoRoomOSAdapter) Disconnect() error {
	a.SetStatus(device.StatusOffline)
	return nil
}

// ── Poll ─────────────────────────────────────────────────────────────────

func (a *CiscoRoomOSAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	t := a.BaseTelemetry()
	start := time.Now()

	root, err := a.getXML(ctx, "/Status")
	if err != nil {
		a.SetStatus(device.StatusOffline)
		t.Status = device.StatusOffline
		t.Error = err.Error()
		return t, nil
	}
	metrics := ciscoStatusMetrics(root)
	metrics["response_ms"] = time.Since(start).Milliseconds()

	a.SetStatus(device.StatusOnline)
	t.Status = device.StatusOnline
	t.Metrics = metrics
	return t, nil
}

// ciscoStatusMetrics maps the /Status document onto telemetry. Missing
// nodes are simply omitted — not every product has every status (e.g.
// people count needs a camera that supports it).
func ciscoStatusMetrics(root *xnode) map[string]any {
	m := map[string]any{}
	set := func(key, val string) {
		if val != "" {
			m[key] = val
		}
	}

	// Identity.
	set("product_id", root.text("SystemUnit", "ProductId"))
	set("software_version", root.text("SystemUnit", "Software", "Version"))
	serial := root.text("SystemUnit", "Hardware", "Module", "SerialNumber")
	if serial == "" {
		serial = root.text("SystemUnit", "Hardware", "MainBoard", "SerialNumber")
	}
	set("serial_number", serial)
	if up, err := strconv.Atoi(root.text("SystemUnit", "Uptime")); err == nil {
		m["uptime_s"] = up
	}
	if nw := root.child("Network"); nw != nil { // Network item="1": primary interface
		set("ip_address", nw.text("IPv4", "Address"))
		set("mac_address", nw.text("Ethernet", "MacAddress"))
	}

	// Audio.
	if v, err := strconv.Atoi(root.text("Audio", "Volume")); err == nil {
		m["volume"] = v
	}
	if mute := root.text("Audio", "Microphones", "Mute"); mute != "" {
		m["mic_mute"] = mute == "On"
	}

	// Standby ↔ power. Off means awake; Standby / EnteringStandby /
	// Halfwake are all some form of asleep.
	if st := root.text("Standby", "State"); st != "" {
		m["standby_state"] = st
		if st == "Off" {
			m["power_status"] = "on"
		} else {
			m["power_status"] = "standby"
		}
	}

	// Calls. A Call item exists only while a call does, so the count of
	// items is the number of active calls. call_state is the first call's
	// status, lower-cased (connected, dialling, ringing…), or "idle".
	calls := root.children("Call")
	m["active_calls"] = len(calls)
	m["call_state"] = "idle"
	if len(calls) > 0 {
		if s := calls[0].text("Status"); s != "" {
			m["call_state"] = strings.ToLower(s)
		}
		set("remote_number", calls[0].text("RemoteNumber"))
	}

	// Diagnostics — the device's own health check, same shape as the
	// Tesira fault list so the portal renders it the same way.
	faults := []any{}
	if d := root.child("Diagnostics"); d != nil {
		for _, msg := range d.children("Message") {
			level := msg.text("Level")
			if level != "Error" && level != "Critical" && level != "Warning" {
				continue
			}
			faults = append(faults, map[string]any{
				"level":       level,
				"type":        msg.text("Type"),
				"description": msg.text("Description"),
			})
		}
	}
	m["fault_count"] = len(faults)
	if len(faults) > 0 {
		m["faults"] = faults
	}

	// Occupancy, where the camera supports it. -1 means "not available".
	if pc, err := strconv.Atoi(root.text("RoomAnalytics", "PeopleCount", "Current")); err == nil && pc >= 0 {
		m["people_count"] = pc
	}
	return m
}

// ── Commands ─────────────────────────────────────────────────────────────

// SendCommand runs an xCommand through putxml:
//
//	dial        Dial Number: <args.address>
//	hangup      Call Disconnect           (no CallId = the active call)
//	mute        Audio Microphones Mute
//	unmute      Audio Microphones Unmute
//	vol_up      Audio Volume Increase     (5 steps of 0.5 dB, device default)
//	vol_dn      Audio Volume Decrease
//	power_on    Standby Deactivate
//	power_off   Standby Activate
//	reboot      SystemUnit Boot
func (a *CiscoRoomOSAdapter) SendCommand(ctx context.Context, req device.CommandRequest) (*device.CommandResponse, error) {
	var body string
	switch req.Name {
	case "dial":
		addr := strings.TrimSpace(fmt.Sprint(req.Args["address"]))
		if addr == "" || addr == "<nil>" {
			return nil, errors.New("dial needs an address (SIP URI, H.323 address or number)")
		}
		body = "<Dial command=\"True\"><Number>" + xmlEscape(addr) + "</Number></Dial>"
	case "hangup":
		body = `<Call><Disconnect command="True"/></Call>`
	case "mute":
		body = `<Audio><Microphones><Mute command="True"/></Microphones></Audio>`
	case "unmute":
		body = `<Audio><Microphones><Unmute command="True"/></Microphones></Audio>`
	case "vol_up":
		body = `<Audio><Volume><Increase command="True"/></Volume></Audio>`
	case "vol_dn":
		body = `<Audio><Volume><Decrease command="True"/></Volume></Audio>`
	case "power_on":
		body = `<Standby><Deactivate command="True"/></Standby>`
	case "power_off":
		body = `<Standby><Activate command="True"/></Standby>`
	case "reboot":
		body = `<SystemUnit><Boot command="True"/></SystemUnit>`
	default:
		return nil, fmt.Errorf("unsupported command %q for cisco_roomos", req.Name)
	}

	start := time.Now()
	raw, err := a.putXML(ctx, "<Command>"+body+"</Command>")
	if err != nil {
		// The device can drop the connection as it goes down for a
		// reboot; the command was delivered, so that's success.
		if req.Name == "reboot" && isConnectionDrop(err) {
			return &device.CommandResponse{Raw: "", Parsed: map[string]any{"success": true}, Latency: time.Since(start)}, nil
		}
		return nil, err
	}
	parsed, err := ciscoCommandResult(raw)
	if err != nil {
		return nil, err
	}
	return &device.CommandResponse{Raw: string(raw), Parsed: parsed, Latency: time.Since(start)}, nil
}

// ciscoCommandResult reads a putxml reply: <Command><XxxResult status="OK"/>
// </Command>, or status="Error" with a <Reason>.
func ciscoCommandResult(raw []byte) (map[string]any, error) {
	root, err := parseXNode(raw)
	if err != nil {
		return nil, fmt.Errorf("parse command response: %w", err)
	}
	var res *xnode
	for _, c := range root.kids {
		if c.attrs["status"] != "" {
			res = c
			break
		}
	}
	if res == nil {
		return nil, fmt.Errorf("unexpected command response: %s", strings.TrimSpace(string(raw)))
	}
	if !strings.EqualFold(res.attrs["status"], "OK") {
		reason := res.text("Reason")
		if reason == "" {
			reason = res.attrs["status"]
		}
		return nil, fmt.Errorf("device rejected command: %s", reason)
	}
	parsed := map[string]any{"success": true}
	if id := res.text("CallId"); id != "" {
		parsed["call_id"] = id
	}
	return parsed, nil
}

var ciscoCapabilities = device.Capabilities{
	// Standby Deactivate / Activate wake and sleep the device.
	Power: device.PowerCapability{On: true, Off: true},
	Commands: []string{
		"dial", "hangup",
		"mute", "unmute",
		"vol_up", "vol_dn",
		"power_on", "power_off",
		"reboot",
	},
	Metrics: []string{
		"call_state", "active_calls", "remote_number",
		"mic_mute", "volume",
		"power_status", "standby_state",
		"product_id", "software_version", "serial_number",
		"ip_address", "mac_address", "uptime_s",
		"fault_count", "faults",
		"people_count",
		"response_ms",
	},
}

func (a *CiscoRoomOSAdapter) Capabilities() device.Capabilities { return ciscoCapabilities }

// ── HTTP ─────────────────────────────────────────────────────────────────

func (a *CiscoRoomOSAdapter) getXML(ctx context.Context, location string) (*xnode, error) {
	raw, err := a.do(ctx, http.MethodGet, "/getxml?location="+url.QueryEscape(location), nil)
	if err != nil {
		return nil, err
	}
	return parseXNode(raw)
}

func (a *CiscoRoomOSAdapter) putXML(ctx context.Context, body string) ([]byte, error) {
	return a.do(ctx, http.MethodPost, "/putxml", strings.NewReader(body))
}

func (a *CiscoRoomOSAdapter) do(ctx context.Context, method, path string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(a.Cfg.Username, a.Cfg.Password)
	if body != nil {
		req.Header.Set("Content-Type", "text/xml")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, errors.New("authentication failed — check the username and password, and that the user has the Admin role")
	case resp.StatusCode == http.StatusForbidden:
		return nil, errors.New("access denied — the user needs the Admin role")
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("HTTP %d from device", resp.StatusCode)
	}
	return raw, nil
}

func isConnectionDrop(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		strings.Contains(err.Error(), "connection reset") ||
		strings.Contains(err.Error(), "EOF")
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// ── Minimal XML tree ─────────────────────────────────────────────────────
//
// The xAPI status document is large and product-dependent, so rather than
// model it with structs the adapter walks a generic element tree.

type xnode struct {
	name  string
	attrs map[string]string
	value string
	kids  []*xnode
}

func parseXNode(raw []byte) (*xnode, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var stack []*xnode
	var root *xnode
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xnode{name: t.Name.Local, attrs: map[string]string{}}
			for _, a := range t.Attr {
				n.attrs[a.Name.Local] = a.Value
			}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.kids = append(parent.kids, n)
			} else {
				root = n
			}
			stack = append(stack, n)
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].value += string(t)
			}
		case xml.EndElement:
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.value = strings.TrimSpace(top.value)
				stack = stack[:len(stack)-1]
			}
		}
	}
	if root == nil {
		return nil, errors.New("empty XML document")
	}
	return root, nil
}

// child returns the first direct child with the given name.
func (n *xnode) child(name string) *xnode {
	if n == nil {
		return nil
	}
	for _, c := range n.kids {
		if c.name == name {
			return c
		}
	}
	return nil
}

// children returns every direct child with the given name (list items).
func (n *xnode) children(name string) []*xnode {
	var out []*xnode
	if n == nil {
		return out
	}
	for _, c := range n.kids {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

// text follows a path of first-children and returns the leaf's text.
func (n *xnode) text(path ...string) string {
	cur := n
	for _, p := range path {
		cur = cur.child(p)
		if cur == nil {
			return ""
		}
	}
	return cur.value
}
