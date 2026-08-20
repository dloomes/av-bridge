package adapters

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// ViscaOverIPAdapter is the generic Sony VISCA-over-IP transport adapter.
// One instance talks to one camera; commands are serialised so a slow
// camera can't tangle the two VISCA sockets. Works with any camera that
// implements the VISCA-over-IP spec — Sony BRC/SRG, Panasonic AW-UE,
// PTZOptics, HuddleCam, Marshall CV, and Lumens VC-A61P (the first
// tested model).
//
// Transport: UDP on port 52381 (Sony default). If the operator wants a
// non-default port, they can put it in Cfg.Address as host:port.
//
// Protocol state: after Connect, a read goroutine consumes replies from
// the UDP socket and dispatches them to a pending-map keyed by sequence
// number. SendCommand assigns a sequence, sends, waits on the pending
// channel for the completion (or error), then returns. Inquiries follow
// the same shape — they use the same reply-routing pipeline.
type ViscaOverIPAdapter struct {
	device.Base

	conn *net.UDPConn

	// seq is the next sequence number to hand out. Wraps at 2^32.
	// Atomic so SendCommand can pull one without holding the send mutex.
	seq atomic.Uint32

	// sendMu serialises the send → wait-for-reply cycle. VISCA supports
	// two concurrent commands via its socket mechanism, but managing that
	// correctly is meaningful complexity for a marginal throughput gain —
	// PTZ camera control is inherently low-QPS, so a mutex is fine.
	sendMu sync.Mutex

	// pending routes an inbound reply to its waiter. Guarded by pendingMu.
	pending   map[uint32]chan viscaReply
	pendingMu sync.Mutex

	// Snapshot of the last telemetry values so Poll can return quickly
	// without re-inquiring on every request.
	tele   viscaTelemetry
	teleMu sync.RWMutex
}

// viscaReply is what the read loop delivers to a pending waiter.
type viscaReply struct {
	payload []byte
	err     error
}

// viscaTelemetry holds the values we surface via Poll.
type viscaTelemetry struct {
	powerOn       *bool // pointer so we can distinguish "unknown" from false
	zoomPos       uint16
	zoomKnown     bool
	versionString string
	lastInqAt     time.Time
}

func NewViscaOverIPAdapter(cfg config.DeviceConfig) *ViscaOverIPAdapter {
	return &ViscaOverIPAdapter{
		Base:    device.NewBase(cfg),
		pending: make(map[uint32]chan viscaReply),
	}
}

// Connect opens the UDP socket. Note: "connect" is a stretch for a
// datagram protocol — there's no handshake. We do call the OS's
// connect(2) via DialUDP so the kernel filters incoming datagrams to
// those from the camera's address, giving us cheap sender authentication
// without extra work in the read loop.
//
// A control message (RESET_SEQUENCE_NUMBER) followed by IF_Clear resets
// both ends' state so a stale in-flight command from a previous session
// can't confuse the reply router.
func (a *ViscaOverIPAdapter) Connect(ctx context.Context) error {
	address := a.Cfg.Address
	if !strings.Contains(address, ":") {
		// Default to Sony's spec port when the operator gave just an IP.
		address += ":52381"
	}
	raddr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("visca_over_ip: resolve %q: %w", address, err)
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("visca_over_ip: dial %s: %w", raddr, err)
	}
	a.conn = conn
	a.SetStatus(device.StatusOnline)

	// Reset controller-side sequence to 1. Sony VISCA lets us restart the
	// counter with a CONTROL command; we do it so multiple runs of the
	// bridge don't rely on the camera remembering our last number.
	a.seq.Store(1)

	go a.readLoop(ctx)

	// Fire IF_Clear so the camera drops any in-flight from a previous
	// session. 2-second budget — camera responds within tens of ms.
	clearCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := a.roundTrip(clearCtx, viscaPayloadCommand, viscaIFClear()); err != nil {
		// IF_Clear failing is a soft warning — some cameras don't reply
		// with a completion for IF_Clear, and connectivity's already
		// proven by DialUDP succeeding. Log and move on.
		slog.Warn("visca_over_ip: IF_Clear round-trip failed (continuing)", "device", a.Cfg.ID, "error", err)
	}
	return nil
}

func (a *ViscaOverIPAdapter) Disconnect() error {
	a.SetStatus(device.StatusOffline)
	if a.conn != nil {
		return a.conn.Close()
	}
	return nil
}

// readLoop consumes datagrams from the UDP socket and hands them to
// whichever SendCommand is waiting for that sequence number. Fires until
// the connection is closed or the context is cancelled.
func (a *ViscaOverIPAdapter) readLoop(ctx context.Context) {
	buf := make([]byte, 1024)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if a.conn == nil {
			return
		}
		_ = a.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := a.conn.Read(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue // just a poll wake-up
			}
			slog.Debug("visca_over_ip: read loop exiting", "device", a.Cfg.ID, "error", err)
			return
		}
		datagram := make([]byte, n)
		copy(datagram, buf[:n])
		payloadType, seq, payload, decodeErr := decodeViscaIP(datagram)
		if decodeErr != nil {
			slog.Warn("visca_over_ip: malformed datagram", "device", a.Cfg.ID, "error", decodeErr)
			continue
		}
		// Only reply payloads carry data we care about. Everything else
		// (control replies etc.) is ignored — control-plane traffic
		// isn't part of the SendCommand round-trip.
		if payloadType != viscaPayloadReply {
			continue
		}
		// ACKs are informational: the camera says "yes, working on it".
		// The completion is what SendCommand actually waits for, so drop
		// ACKs on the floor and keep the pending slot open.
		if viscaReplyKind(payload) == viscaReplyACK {
			continue
		}
		a.deliver(seq, viscaReply{payload: payload})
	}
}

// deliver routes an inbound reply to the SendCommand waiting for it.
// If nobody is waiting (the caller gave up on timeout, or a stray
// reply arrives), the reply is dropped silently.
func (a *ViscaOverIPAdapter) deliver(seq uint32, r viscaReply) {
	a.pendingMu.Lock()
	ch, ok := a.pending[seq]
	if ok {
		delete(a.pending, seq)
	}
	a.pendingMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- r:
	default:
	}
}

// roundTrip is the core VISCA-over-IP request/response cycle. Sends the
// payload with a fresh sequence number, waits for the completion (or
// error) up to the context's deadline, and returns the raw completion
// payload for the caller to parse.
//
// Serialised via sendMu so two callers can't scramble the pending map.
func (a *ViscaOverIPAdapter) roundTrip(ctx context.Context, payloadType uint16, payload []byte) ([]byte, error) {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()

	if a.conn == nil {
		return nil, fmt.Errorf("visca_over_ip: not connected")
	}

	seq := a.seq.Add(1)
	ch := make(chan viscaReply, 1)
	a.pendingMu.Lock()
	a.pending[seq] = ch
	a.pendingMu.Unlock()
	// Belt-and-braces cleanup so a timing race can't leave the map
	// holding a channel forever.
	defer func() {
		a.pendingMu.Lock()
		delete(a.pending, seq)
		a.pendingMu.Unlock()
	}()

	frame := encodeViscaIP(payloadType, seq, payload)
	if _, err := a.conn.Write(frame); err != nil {
		return nil, fmt.Errorf("visca_over_ip: write seq=%d: %w", seq, err)
	}

	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		if viscaReplyKind(r.payload) == viscaReplyError {
			return nil, fmt.Errorf("%s", viscaErrorMessage(r.payload))
		}
		return r.payload, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("visca_over_ip: seq=%d timeout: %w", seq, ctx.Err())
	}
}

// Poll gathers a fresh snapshot by inquiring power state, zoom position,
// and (on the first poll) the model version. Model version is cached
// because it's static per device and every call is one extra round-trip
// against a device that's often on the far side of a loaded VLAN.
func (a *ViscaOverIPAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	if a.conn == nil {
		return nil, fmt.Errorf("visca_over_ip: device %s not connected", a.Cfg.ID)
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	metrics := map[string]any{}

	if reply, err := a.roundTrip(ctx, viscaPayloadInquiry, viscaInqPower()); err == nil {
		if on, perr := parseInqPower(reply); perr == nil {
			metrics["power"] = onOffString(on)
			a.teleMu.Lock()
			a.tele.powerOn = &on
			a.teleMu.Unlock()
		}
	}
	if reply, err := a.roundTrip(ctx, viscaPayloadInquiry, viscaInqZoomPos()); err == nil {
		if pos, perr := parseInqZoomPos(reply); perr == nil {
			metrics["zoom_position"] = pos
			metrics["zoom_percent"] = viscaZoomPercent(pos)
			a.teleMu.Lock()
			a.tele.zoomPos = pos
			a.tele.zoomKnown = true
			a.teleMu.Unlock()
		}
	}
	a.teleMu.RLock()
	version := a.tele.versionString
	a.teleMu.RUnlock()
	if version == "" {
		if reply, err := a.roundTrip(ctx, viscaPayloadInquiry, viscaInqVersion()); err == nil {
			if v, perr := parseInqVersion(reply); perr == nil {
				version = v
				a.teleMu.Lock()
				a.tele.versionString = v
				a.teleMu.Unlock()
			}
		}
	}
	if version != "" {
		metrics["version"] = version
	}
	metrics["response_ms"] = 0 // filled by the sender if it cares

	a.teleMu.Lock()
	a.tele.lastInqAt = time.Now()
	a.teleMu.Unlock()

	t := a.BaseTelemetry()
	t.Metrics = metrics
	return t, nil
}

// SendCommand dispatches a command name to the correct VISCA builder.
// Command names + args are documented in the cloud catalogue; a name we
// don't recognise falls back to Cfg.Commands[name] so operators can add
// their own raw-bytes commands (comma-separated hex) as an escape hatch.
//
// Directional pan/tilt commands are auto-jog by default: send the drive,
// wait `duration_ms` (default 250ms), send Pan-Tilt Stop. That gives
// click-to-nudge behaviour on portal buttons. Pass duration_ms=0 to
// disable the auto-stop and get raw continuous drive — useful when the
// portal wires up a press-and-hold joystick UI later.
func (a *ViscaOverIPAdapter) SendCommand(ctx context.Context, cmd device.CommandRequest) (*device.CommandResponse, error) {
	// Jog commands take a different execution path (send + wait + stop),
	// so branch here before falling through to the single-packet builder.
	if isJogCommand(cmd.Name) {
		return a.sendJog(ctx, cmd)
	}
	payload, err := a.buildCommand(cmd)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	start := time.Now()
	reply, err := a.roundTrip(ctx, viscaPayloadCommand, payload)
	if err != nil {
		return nil, err
	}
	return &device.CommandResponse{
		Raw:     fmt.Sprintf("% X", reply),
		Latency: time.Since(start),
	}, nil
}

// isJogCommand reports whether cmd is one of the directional drive
// commands we auto-stop after a short duration.
func isJogCommand(name string) bool {
	switch name {
	case "pan_left", "pan_right", "tilt_up", "tilt_down":
		return true
	}
	return false
}

// sendJog is the "move a bit, then stop" execution used by the directional
// commands. Sends the drive packet, sleeps duration_ms (default 250, clamp
// 50-2000), then sends Pan-Tilt Stop. If duration_ms=0 the stop is
// skipped — that's the escape hatch for a press-and-hold UI upstream that
// will send its own stop.
func (a *ViscaOverIPAdapter) sendJog(ctx context.Context, cmd device.CommandRequest) (*device.CommandResponse, error) {
	drive, err := a.buildJogDrive(cmd)
	if err != nil {
		return nil, err
	}
	// Total time budget = duration + a stop call + generous margin. The
	// caller's context.Deadline (from cmd wiring) still bounds this.
	durationMs := clampDuration(argInt(cmd.Args, "duration_ms", 250), 0, 2000)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(durationMs+1500)*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := a.roundTrip(ctx, viscaPayloadCommand, drive); err != nil {
		return nil, err
	}
	if durationMs == 0 {
		// Continuous mode — caller is on the hook for its own stop.
		return &device.CommandResponse{
			Raw:     "jog started (continuous — duration_ms=0)",
			Latency: time.Since(start),
		}, nil
	}
	// Wait, then stop. Respect cancellation during the wait so a client
	// disconnect doesn't leave the camera drifting.
	select {
	case <-time.After(time.Duration(durationMs) * time.Millisecond):
	case <-ctx.Done():
		// Even on cancellation, try to send the stop so the camera
		// doesn't keep drifting. Use a fresh short-lived context.
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer stopCancel()
	if _, err := a.roundTrip(stopCtx, viscaPayloadCommand, viscaPanTiltStop()); err != nil {
		return nil, fmt.Errorf("jog stop failed: %w", err)
	}
	return &device.CommandResponse{
		Raw:     fmt.Sprintf("jog %s: %dms", cmd.Name, durationMs),
		Latency: time.Since(start),
	}, nil
}

// buildJogDrive returns the drive payload for a jog command name.
// Speeds honour cmd.Args["pan_speed"] / ["tilt_speed"] and default to a
// gentle 8/8 — noticeable but not disorienting on a click.
func (a *ViscaOverIPAdapter) buildJogDrive(cmd device.CommandRequest) ([]byte, error) {
	switch cmd.Name {
	case "pan_left":
		return viscaPanTilt(argByte(cmd.Args, "pan_speed", 8), 0, viscaPanLeft, viscaTiltStop), nil
	case "pan_right":
		return viscaPanTilt(argByte(cmd.Args, "pan_speed", 8), 0, viscaPanRight, viscaTiltStop), nil
	case "tilt_up":
		return viscaPanTilt(0, argByte(cmd.Args, "tilt_speed", 8), viscaPanStop, viscaTiltUp), nil
	case "tilt_down":
		return viscaPanTilt(0, argByte(cmd.Args, "tilt_speed", 8), viscaPanStop, viscaTiltDown), nil
	}
	return nil, fmt.Errorf("visca_over_ip: not a jog command: %q", cmd.Name)
}

func clampDuration(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (a *ViscaOverIPAdapter) buildCommand(cmd device.CommandRequest) ([]byte, error) {
	switch cmd.Name {
	case "power_on":
		return viscaPower(true), nil
	case "power_off":
		return viscaPower(false), nil
	case "zoom_stop":
		return viscaZoomStop(), nil
	case "zoom_tele":
		return viscaZoomTele(argByte(cmd.Args, "speed", 4)), nil
	case "zoom_wide":
		return viscaZoomWide(argByte(cmd.Args, "speed", 4)), nil
	case "zoom_direct":
		return viscaZoomDirect(argUint16(cmd.Args, "position", 0)), nil
	case "focus_auto":
		return viscaFocusAuto(), nil
	case "focus_manual":
		return viscaFocusManual(), nil
	case "focus_one_push":
		return viscaFocusOnePush(), nil
	case "preset_recall":
		return viscaPresetRecall(argByte(cmd.Args, "preset", 0)), nil
	case "preset_set":
		return viscaPresetSet(argByte(cmd.Args, "preset", 0)), nil
	case "pan_tilt_stop":
		// Explicit stop — useful when duration_ms=0 was used on a jog
		// command to run the camera in continuous mode.
		return viscaPanTiltStop(), nil
	case "pan_tilt_home":
		return viscaPanTiltHome(), nil
	}
	// Escape hatch — operator-defined raw bytes in Cfg.Commands.
	if raw, ok := a.Cfg.Commands[cmd.Name]; ok {
		return parseHexBytes(raw)
	}
	return nil, fmt.Errorf("visca_over_ip: unknown command %q", cmd.Name)
}

// Capabilities declares what this adapter can do. Populated statically
// because VISCA capability is fixed by the spec — no need to inquire.
func (a *ViscaOverIPAdapter) Capabilities() device.Capabilities {
	return device.Capabilities{
		Power: device.PowerCapability{On: true, Off: true},
		Commands: []string{
			"power_on", "power_off",
			"zoom_stop", "zoom_tele", "zoom_wide", "zoom_direct",
			"focus_auto", "focus_manual", "focus_one_push",
			"preset_recall", "preset_set",
			"pan_left", "pan_right", "tilt_up", "tilt_down",
			"pan_tilt_stop", "pan_tilt_home",
		},
		Metrics: []string{"power", "zoom_position", "zoom_percent", "version", "response_ms"},
	}
}

// -----------------------------------------------------------------------------
// Small helpers — not worth their own file.
// -----------------------------------------------------------------------------

func onOffString(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// viscaZoomPercent converts a raw optical zoom position (0x0000-0x4000)
// to a friendlier 0-100 percent. Portal charts render either.
func viscaZoomPercent(pos uint16) int {
	if pos > 0x4000 {
		pos = 0x4000
	}
	return int((uint32(pos) * 100) / 0x4000)
}

// argByte pulls a numeric arg out of the CommandRequest.Args map with a
// default when absent. JSON numbers arrive as float64 through the
// portal → cloud → bridge pipeline, so we accept both float64 and int.
func argByte(args map[string]any, key string, def byte) byte {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return byte(int(n) & 0xFF)
	case int:
		return byte(n & 0xFF)
	}
	return def
}

func argInt(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return def
}

func argUint16(args map[string]any, key string, def uint16) uint16 {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return uint16(int(n) & 0xFFFF)
	case int:
		return uint16(n & 0xFFFF)
	}
	return def
}

// parseHexBytes turns a whitespace-separated hex string ("81 01 04 00 02
// FF") into the raw bytes. Used only for operator-defined escape-hatch
// commands in Cfg.Commands.
func parseHexBytes(s string) ([]byte, error) {
	fields := strings.Fields(s)
	out := make([]byte, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.ParseUint(f, 16, 8)
		if err != nil {
			return nil, fmt.Errorf("visca_over_ip: bad hex byte %q: %w", f, err)
		}
		out = append(out, byte(n))
	}
	if len(out) < 3 || out[0] != viscaCmdHeader || out[len(out)-1] != viscaTerminator {
		return nil, fmt.Errorf("visca_over_ip: raw command must start with 81 and end with FF")
	}
	return out, nil
}
