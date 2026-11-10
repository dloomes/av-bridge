package adapters

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// NECProjectorAdapter controls NEC / Sharp NEC projectors over the NEC
// projector control protocol on TCP port 7142. Written against the
// "Projector Control Command Reference Manual" BDT140013 (rev 7.1 body,
// 2023 appendix), first for the NP-PA804UL, which the manual's appendix
// lists alongside the NP-PA1004UL.
//
// Frame (manual §2.1–2.2): <CMD1> <CMD2> <ID1> <ID2> <LEN> <DATA…> <CKS>,
// where CKS is the low byte of the sum of every preceding byte. A reply
// echoes CMD1 with 0x20 set and CMD2; a failed command sets 0x80 as well
// (A0h–A3h) and carries <ERR1> <ERR2> (§2.4).
//
// Session model: one TCP connection per poll or command, serialised by
// connMu. Projectors commonly accept a single control client, so the
// connection is not held open between polls (a room controller can use it
// in between). While the projector powers on or cools down it may accept
// no other commands (§3.2, §3.3); that is reported as warming / cooling,
// not as the projector going offline.
type NECProjectorAdapter struct {
	device.Base
	address   string
	controlID byte

	connMu sync.Mutex

	// Identity (model, serial, MAC) rarely changes: read at connect and
	// every necInfoEvery after, and reported with each poll.
	infoMu sync.Mutex
	info   map[string]any
	infoAt time.Time

	// The last power state seen and the last power command sent, so a
	// projector too busy to answer mid-transition reads as warming or
	// cooling, and commands rejected then say why.
	stateMu     sync.Mutex
	lastPower   string
	lastPowerAt time.Time
	powerCmd    string
	powerCmdAt  time.Time
}

const (
	necDefaultPort      = 7142
	necDialTimeout      = 5 * time.Second
	necReplyWait        = 3 * time.Second
	necPowerReplyWait   = 10 * time.Second
	necInfoEvery        = 6 * time.Hour
	necTransitionWindow = 3 * time.Minute
)

// Command frames, without the checksum (necFrame appends it). Each cites
// the manual's command number and its full byte sequence.
var (
	necCmdErrorStatus   = []byte{0x00, 0x88, 0x00, 0x00, 0x00}                   // [009. ERROR STATUS REQUEST] 00h 88h 00h 00h 00h 88h
	necCmdPowerOn       = []byte{0x02, 0x00, 0x00, 0x00, 0x00}                   // [015. POWER ON] 02h 00h 00h 00h 00h 02h
	necCmdPowerOff      = []byte{0x02, 0x01, 0x00, 0x00, 0x00}                   // [016. POWER OFF] 02h 01h 00h 00h 00h 03h
	necCmdPictureMuteOn = []byte{0x02, 0x10, 0x00, 0x00, 0x00}                   // [020. PICTURE MUTE ON] 02h 10h 00h 00h 00h 12h
	necCmdPictureMuteOf = []byte{0x02, 0x11, 0x00, 0x00, 0x00}                   // [021. PICTURE MUTE OFF] 02h 11h 00h 00h 00h 13h
	necCmdSoundMuteOn   = []byte{0x02, 0x12, 0x00, 0x00, 0x00}                   // [022. SOUND MUTE ON] 02h 12h 00h 00h 00h 14h
	necCmdSoundMuteOff  = []byte{0x02, 0x13, 0x00, 0x00, 0x00}                   // [023. SOUND MUTE OFF] 02h 13h 00h 00h 00h 15h
	necCmdShutterClose  = []byte{0x02, 0x16, 0x00, 0x00, 0x00}                   // [051. SHUTTER CLOSE] 02h 16h 00h 00h 00h 18h
	necCmdShutterOpen   = []byte{0x02, 0x17, 0x00, 0x00, 0x00}                   // [052. SHUTTER OPEN] 02h 17h 00h 00h 00h 19h
	necCmdFilterUsage   = []byte{0x03, 0x95, 0x00, 0x00, 0x00}                   // [037-3. FILTER USAGE INFORMATION REQUEST] 03h 95h 00h 00h 00h 98h
	necCmdLampHours     = []byte{0x03, 0x96, 0x00, 0x00, 0x02, 0x00, 0x01}       // [037-4. LAMP INFORMATION REQUEST 3] 03h 96h 00h 00h 02h 00h 01h 9Ch (lamp 1, usage seconds)
	necCmdLampLife      = []byte{0x03, 0x96, 0x00, 0x00, 0x02, 0x00, 0x04}       // [037-4] 03h 96h 00h 00h 02h 00h 04h 9Fh (lamp 1, remaining life %)
	necCmdVolumeGet     = []byte{0x03, 0x05, 0x00, 0x00, 0x03, 0x05, 0x00, 0x00} // [060-1. GAIN PARAMETER REQUEST 3] 03h 05h 00h 00h 03h 05h 00h 00h 10h (VOLUME)
	necCmdRunningStatus = []byte{0x00, 0x85, 0x00, 0x00, 0x01, 0x01}             // [078-2. RUNNING STATUS REQUEST] 00h 85h 00h 00h 01h 01h 87h
	necCmdInputStatus   = []byte{0x00, 0x85, 0x00, 0x00, 0x01, 0x02}             // [078-3. INPUT STATUS REQUEST] 00h 85h 00h 00h 01h 02h 88h
	necCmdMuteStatus    = []byte{0x00, 0x85, 0x00, 0x00, 0x01, 0x03}             // [078-4. MUTE STATUS REQUEST] 00h 85h 00h 00h 01h 03h 89h
	necCmdModelName     = []byte{0x00, 0x85, 0x00, 0x00, 0x01, 0x04}             // [078-5. MODEL NAME REQUEST] 00h 85h 00h 00h 01h 04h 8Ah
	necCmdMACAddress    = []byte{0x03, 0xB0, 0x00, 0x00, 0x02, 0x9A, 0x00}       // [097-155. LAN MAC ADDRESS STATUS REQUEST2] 03h B0h 00h 00h 02h 9Ah 00h 4Fh
	necCmdSerialNumber  = []byte{0x00, 0xBF, 0x00, 0x00, 0x02, 0x01, 0x06}       // [305-2. SERIAL NUMBER REQUEST] 00h BFh 00h 00h 02h 01h 06h C8h
	necCmdBasicInfo     = []byte{0x00, 0xBF, 0x00, 0x00, 0x01, 0x02}             // [305-3. BASIC INFORMATION REQUEST] 00h BFh 00h 00h 01h 02h C2h
)

// necInputCommand is [018. INPUT SW CHANGE] 02h 03h 00h 00h 02h 01h <DATA01> <CKS>.
func necInputCommand(terminal byte) []byte {
	return []byte{0x02, 0x03, 0x00, 0x00, 0x02, 0x01, terminal}
}

// necVolumeCommand is [030-2. VOLUME ADJUST]
// 03h 10h 00h 00h 05h 05h 00h <mode> <value lo> <value hi> <CKS>;
// mode 00h absolute, 01h relative (the value is a signed 16-bit number).
func necVolumeCommand(relative bool, value int16) []byte {
	mode := byte(0x00)
	if relative {
		mode = 0x01
	}
	v := uint16(value)
	return []byte{0x03, 0x10, 0x00, 0x00, 0x05, 0x05, 0x00, mode, byte(v), byte(v >> 8)}
}

// necInputCommands are the input_* buttons: [018] input terminal values
// for the NP-PA1004UL / NP-PA804UL family, from the appendix
// "Supplementary Information by Command → Input terminal".
var necInputCommands = map[string]byte{
	"input_computer":    0x01, // COMPUTER
	"input_hdmi1":       0xA1, // HDMI1
	"input_hdmi2":       0xA2, // HDMI2
	"input_displayport": 0xA6, // DisplayPort
	"input_hdbaset":     0xBF, // HDBaseT
}

var errNECNoReply = errors.New("no reply from projector")

// NECError is a failed-command reply: ERR1/ERR2 per manual §2.4.
type NECError struct {
	Err1, Err2 byte
}

func (e *NECError) Error() string {
	if s, ok := necErrorText[[2]byte{e.Err1, e.Err2}]; ok {
		return s
	}
	return fmt.Sprintf("projector returned error %02Xh %02Xh", e.Err1, e.Err2)
}

// necErrorText is the §2.4 error code list in plain words.
var necErrorText = map[[2]byte]string{
	{0x00, 0x00}: "Projector didn't recognise the command",
	{0x00, 0x01}: "Command not supported by this projector model",
	{0x01, 0x00}: "Value out of range for this projector",
	{0x01, 0x01}: "That input isn't available on this projector",
	{0x01, 0x02}: "Language not valid",
	{0x02, 0x00}: "Projector memory allocation error",
	{0x02, 0x02}: "Projector memory in use",
	{0x02, 0x03}: "Value can't be set at the moment",
	{0x02, 0x04}: "Forced onscreen mute is on",
	{0x02, 0x06}: "Projector viewer error",
	{0x02, 0x07}: "No signal",
	{0x02, 0x08}: "A test pattern or filter is displayed",
	{0x02, 0x09}: "No PC card inserted",
	{0x02, 0x0A}: "Projector memory operation error",
	{0x02, 0x0C}: "An entry list is displayed",
	{0x02, 0x0D}: "Projector is in standby, so it can't accept this command",
	{0x02, 0x0E}: "Projector couldn't carry out the command",
	{0x02, 0x0F}: "Not permitted on this projector",
	{0x03, 0x00}: "Gain number not valid",
	{0x03, 0x01}: "Gain not valid",
	{0x03, 0x02}: "Adjustment failed",
}

func NewNECProjectorAdapter(cfg config.DeviceConfig) *NECProjectorAdapter {
	addr := strings.TrimSpace(cfg.Address)
	if addr != "" && !strings.Contains(addr, ":") {
		addr = fmt.Sprintf("%s:%d", addr, necDefaultPort)
	}
	var id byte
	if v := strings.TrimSpace(cfg.Tags["control_id"]); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 254 {
			id = byte(n)
		}
	}
	return &NECProjectorAdapter{Base: device.NewBase(cfg), address: addr, controlID: id}
}

// ── Connection ───────────────────────────────────────────────────────────

// Connect proves the projector answers by reading its running status, and
// reads its identity while connected.
func (a *NECProjectorAdapter) Connect(ctx context.Context) error {
	var power string
	err := a.withConn(ctx, func(s *necSession) error {
		rs, err := s.runningStatus()
		if err != nil {
			var ne *NECError
			if !errors.As(err, &ne) {
				return err
			}
			// It answered, just not to this request: it's there.
		} else {
			power = rs.power
			a.notePower(power)
		}
		a.storeInfo(s.identity())
		return nil
	})
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("nec_projector connect %s (%s): %w", a.Cfg.ID, a.address, err)
	}
	a.SetStatus(device.StatusOnline)
	slog.Info("nec_projector connected", "device", a.Cfg.ID, "address", a.address, "power", power)
	return nil
}

func (a *NECProjectorAdapter) Disconnect() error {
	a.SetStatus(device.StatusOffline)
	return nil
}

// ── Poll ─────────────────────────────────────────────────────────────────

// Poll reads the running status first. Light-source and filter hours,
// faults and identity are read in any state; input, mutes and volume only
// while the projector is on (in standby they are refused). Every reading
// but the running status is best-effort.
func (a *NECProjectorAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	t := a.BaseTelemetry()
	start := time.Now()
	m := map[string]any{}

	err := a.withConn(ctx, func(s *necSession) error {
		power := ""
		rs, err := s.runningStatus()
		switch {
		case err == nil:
			power = rs.power
			m["power_status"] = rs.power
			if rs.operation != "" {
				m["operation_status"] = rs.operation
			}
			a.notePower(power)
		case isNECError(err):
			// Answering but refusing the request: still reachable.
		default:
			// Connected but silent: expected while it powers on or cools
			// down, when it accepts no commands (§3.2, §3.3).
			if st := a.transitionState(); st != "" {
				m["power_status"] = st
				return nil
			}
			return err
		}

		if d, err := s.request(necCmdErrorStatus); err == nil {
			faults := necDecodeErrors(d, power == "on")
			m["fault_count"] = len(faults)
			if len(faults) > 0 {
				m["faults"] = faults
			}
		}
		if d, err := s.request(necCmdLampHours); err == nil {
			if v, ok := necLampValue(d, 0x01); ok && v >= 0 {
				m["light_source_hours"] = v / 3600
			}
		}
		if d, err := s.request(necCmdLampLife); err == nil {
			if v, ok := necLampValue(d, 0x04); ok {
				m["light_source_life_pct"] = v
			}
		}
		if d, err := s.request(necCmdFilterUsage); err == nil {
			if v, ok := necFilterSeconds(d); ok && v >= 0 {
				m["filter_hours"] = v / 3600
			}
		}

		if power == "on" {
			a.pollPicture(s, m)
		}

		if a.infoStale() {
			a.storeInfo(s.identity())
		}
		for k, v := range a.cachedInfo() {
			m[k] = v
		}
		return nil
	})
	if err != nil {
		a.SetStatus(device.StatusOffline)
		t.Status = device.StatusOffline
		t.Error = err.Error()
		return t, nil
	}
	m["response_ms"] = time.Since(start).Milliseconds()
	a.SetStatus(device.StatusOnline)
	t.Status = device.StatusOnline
	t.Metrics = m
	return t, nil
}

// pollPicture adds input, signal, mutes and volume. Models that don't
// support 078-3 / 078-4 report the same through 305-3 (appendix: the
// asterisked models support only 305-3).
func (a *NECProjectorAdapter) pollPicture(s *necSession, m map[string]any) {
	gotInput, gotMute := false, false
	if d, err := s.request(necCmdInputStatus); err == nil {
		if in, ok := necParseInputStatus(d); ok {
			in.apply(m)
			gotInput = true
		}
	}
	if d, err := s.request(necCmdMuteStatus); err == nil {
		if mu, ok := necParseMuteStatus(d); ok {
			mu.apply(m)
			gotMute = true
		}
	}
	if !gotInput || !gotMute {
		if d, err := s.request(necCmdBasicInfo); err == nil {
			if bi, ok := necParseBasicInfo(d); ok {
				if !gotInput {
					bi.input.apply(m)
				}
				if !gotMute {
					bi.mute.apply(m)
				}
			}
		}
	}
	if d, err := s.request(necCmdVolumeGet); err == nil {
		if g, ok := necParseGain(d); ok && g.status != 0x00 && g.status != 0xFF {
			m["volume"] = g.current
		}
	}
}

// ── Commands ─────────────────────────────────────────────────────────────

// SendCommand runs one of:
//
//	power_on / power_off                   [015] / [016]
//	input_hdmi1, input_hdmi2, input_displayport, input_hdbaset,
//	input_computer                         [018]
//	input_select {code}                    [018] with a raw input terminal value, e.g. "A1"
//	picture_mute / picture_unmute          [020] / [021]
//	sound_mute / sound_unmute              [022] / [023]
//	set_volume {level}                     [030-2] absolute
//	volume_up / volume_down                [030-2] relative ±1
//	shutter_close / shutter_open           [051] / [052]
//
// A commands: entry in config wins: space-separated hex bytes of a whole
// command without its checksum, e.g. "02 14 00 00 00" (onscreen mute on),
// with {placeholders} substituted.
func (a *NECProjectorAdapter) SendCommand(ctx context.Context, req device.CommandRequest) (*device.CommandResponse, error) {
	cmd, err := a.resolveCommand(req)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	raw := fmt.Sprintf("% X", a.frame(cmd))
	var parsed map[string]any

	err = a.withConn(ctx, func(s *necSession) error {
		if req.Name == "power_on" || req.Name == "power_off" {
			done, already, perr := a.powerPrecheck(s, req.Name)
			if perr != nil || done {
				parsed = already
				return perr
			}
		}
		wait := necReplyWait
		if req.Name == "power_on" || req.Name == "power_off" {
			wait = necPowerReplyWait
		}
		if req.Name == "set_volume" {
			if err := checkVolumeRange(s, req); err != nil {
				return err
			}
		}
		d, err := s.exchange(wait, cmd)
		if err != nil {
			return err
		}
		return necCheckResult(cmd, d)
	})
	if err != nil {
		err = a.explain(req.Name, err)
		return &device.CommandResponse{Raw: raw, Parsed: map[string]any{"success": false}, Latency: time.Since(start)},
			fmt.Errorf("nec_projector command %q: %w", req.Name, err)
	}
	switch req.Name {
	case "power_on", "power_off":
		if parsed == nil {
			a.notePowerCommand(req.Name)
		}
	}
	if parsed == nil {
		parsed = map[string]any{}
	}
	parsed["success"] = true
	slog.Info("nec_projector command", "device", a.Cfg.ID, "command", req.Name, "frame", raw)
	return &device.CommandResponse{Raw: raw, Parsed: parsed, Latency: time.Since(start)}, nil
}

// powerPrecheck reads the running status before a power command. Power on
// while cooling is refused with a clear message (the projector would
// refuse it anyway); a projector already in the requested state, or on
// its way there, needs nothing. If the status can't be read, the command
// goes ahead.
func (a *NECProjectorAdapter) powerPrecheck(s *necSession, name string) (bool, map[string]any, error) {
	rs, err := s.runningStatus()
	if err != nil {
		return false, nil, nil
	}
	a.notePower(rs.power)
	already := func() (bool, map[string]any, error) {
		return true, map[string]any{"already": rs.power}, nil
	}
	if name == "power_on" {
		switch rs.power {
		case "cooling":
			return false, nil, errors.New("Projector is cooling down; it can be switched on once cooling has finished (usually a minute or two)")
		case "on", "warming":
			return already()
		}
		return false, nil, nil
	}
	switch rs.power {
	case "warming":
		return false, nil, errors.New("Projector is warming up; switch it off once it has finished starting")
	case "standby", "cooling":
		return already()
	}
	return false, nil, nil
}

// checkVolumeRange reads the volume range [060-1] and refuses a level
// outside it, so the operator sees the projector's own limits.
func checkVolumeRange(s *necSession, req device.CommandRequest) error {
	level, _ := necLevelArg(req.Args["level"])
	d, err := s.request(necCmdVolumeGet)
	if err != nil {
		return nil
	}
	g, ok := necParseGain(d)
	if !ok || g.status == 0x00 || g.status == 0xFF || g.upper <= g.lower {
		return nil
	}
	if level < g.lower || level > g.upper {
		return fmt.Errorf("volume %d is outside this projector's range %d–%d", level, g.lower, g.upper)
	}
	return nil
}

// explain turns a failure during a power transition into plain words.
func (a *NECProjectorAdapter) explain(name string, err error) error {
	var ne *NECError
	silent := errors.Is(err, errNECNoReply)
	refused := errors.As(err, &ne) && (ne.Err1 == 0x02 && (ne.Err2 == 0x0D || ne.Err2 == 0x0E || ne.Err2 == 0x03))
	if !silent && !refused {
		return err
	}
	switch a.transitionState() {
	case "warming":
		return fmt.Errorf("Projector is warming up; try again when it has finished starting (%w)", err)
	case "cooling":
		return fmt.Errorf("Projector is cooling down; try again when it has finished (%w)", err)
	}
	return err
}

func (a *NECProjectorAdapter) resolveCommand(req device.CommandRequest) ([]byte, error) {
	if tmpl, ok := a.Cfg.Commands[req.Name]; ok {
		for k, v := range req.Args {
			tmpl = strings.ReplaceAll(tmpl, "{"+k+"}", fmt.Sprintf("%v", v))
		}
		cmd, err := necParseHex(tmpl)
		if err != nil {
			return nil, fmt.Errorf("nec_projector: command %q: %w", req.Name, err)
		}
		if len(cmd) < 5 {
			return nil, fmt.Errorf("nec_projector: command %q: need at least the 5 header bytes", req.Name)
		}
		return cmd, nil
	}
	if code, ok := necInputCommands[req.Name]; ok {
		return necInputCommand(code), nil
	}
	switch req.Name {
	case "power_on":
		return necCmdPowerOn, nil
	case "power_off":
		return necCmdPowerOff, nil
	case "input_select":
		code, err := necInputCodeArg(req.Args["code"])
		if err != nil {
			return nil, err
		}
		return necInputCommand(code), nil
	case "picture_mute":
		return necCmdPictureMuteOn, nil
	case "picture_unmute":
		return necCmdPictureMuteOf, nil
	case "sound_mute":
		return necCmdSoundMuteOn, nil
	case "sound_unmute":
		return necCmdSoundMuteOff, nil
	case "shutter_close":
		return necCmdShutterClose, nil
	case "shutter_open":
		return necCmdShutterOpen, nil
	case "volume_up":
		return necVolumeCommand(true, 1), nil
	case "volume_down":
		return necVolumeCommand(true, -1), nil
	case "set_volume":
		level, err := necLevelArg(req.Args["level"])
		if err != nil {
			return nil, err
		}
		return necVolumeCommand(false, int16(level)), nil
	}
	return nil, fmt.Errorf("nec_projector: unknown command %q", req.Name)
}

// necCheckResult reads the execution result some successful replies carry.
func necCheckResult(cmd, d []byte) error {
	switch {
	case cmd[0] == 0x02 && cmd[1] == 0x03: // [018] DATA01: 00h ok, FFh error (no switch made)
		if len(d) >= 1 && d[0] != 0x00 {
			return errors.New("Projector didn't switch input (no signal switch was made)")
		}
	case cmd[0] == 0x03 && cmd[1] == 0x10: // [030-x] DATA01-02: 0000h ok
		if len(d) >= 2 && (d[0] != 0x00 || d[1] != 0x00) {
			return errors.New("Projector reported the adjustment failed")
		}
	}
	return nil
}

func necLevelArg(v any) (int, error) {
	var n int
	switch t := v.(type) {
	case nil:
		return 0, errors.New(`nec_projector: set_volume requires arg "level"`)
	case float64:
		n = int(t)
	case int:
		n = t
	case string:
		var err error
		if n, err = strconv.Atoi(strings.TrimSpace(t)); err != nil {
			return 0, fmt.Errorf("nec_projector: level %q is not a number", t)
		}
	default:
		return 0, fmt.Errorf("nec_projector: level arg type %T not supported", v)
	}
	if n < 0 || n > 100 {
		return 0, fmt.Errorf("nec_projector: level %d out of range 0–100", n)
	}
	return n, nil
}

// necInputCodeArg accepts an input terminal value as hex ("A1", "0xA1",
// "A1h"), or a friendly name (hdmi1, hdmi2, displayport, hdbaset, computer).
func necInputCodeArg(v any) (byte, error) {
	s := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", v)))
	if v == nil || s == "" {
		return 0, errors.New(`nec_projector: input_select requires arg "code", such as A1 (HDMI1)`)
	}
	if code, ok := necInputCommands["input_"+s]; ok {
		return code, nil
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "0x"), "h")
	n, err := strconv.ParseUint(s, 16, 8)
	if err != nil {
		return 0, fmt.Errorf("nec_projector: input code %q is not a hex byte or input name", v)
	}
	return byte(n), nil
}

func necParseHex(s string) ([]byte, error) {
	var out []byte
	for _, f := range strings.Fields(strings.NewReplacer(",", " ", "0x", "", "0X", "").Replace(s)) {
		f = strings.TrimSuffix(strings.TrimSuffix(f, "h"), "H")
		b, err := strconv.ParseUint(f, 16, 8)
		if err != nil {
			return nil, fmt.Errorf("%q is not a hex byte", f)
		}
		out = append(out, byte(b))
	}
	if len(out) == 0 || len(out) > 64 {
		return nil, fmt.Errorf("need 1–64 bytes, got %d", len(out))
	}
	return out, nil
}

// ── Power transition tracking ────────────────────────────────────────────

func (a *NECProjectorAdapter) notePower(p string) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.lastPower, a.lastPowerAt = p, time.Now()
	// A settled state ends the transition a power command started.
	if p == "on" || p == "standby" {
		a.powerCmd = ""
	}
}

func (a *NECProjectorAdapter) notePowerCommand(name string) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.powerCmd, a.powerCmdAt, a.lastPowerAt = name, time.Now(), time.Now()
	if name == "power_on" {
		a.lastPower = "warming"
	} else {
		a.lastPower = "cooling"
	}
}

// transitionState is "warming" or "cooling" while the projector is (or
// was last seen) mid power change, or "".
func (a *NECProjectorAdapter) transitionState() string {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.powerCmd != "" && time.Since(a.powerCmdAt) < necTransitionWindow {
		if a.powerCmd == "power_on" {
			return "warming"
		}
		return "cooling"
	}
	if (a.lastPower == "warming" || a.lastPower == "cooling") && time.Since(a.lastPowerAt) < necTransitionWindow {
		return a.lastPower
	}
	return ""
}

// ── Identity cache ───────────────────────────────────────────────────────

func (a *NECProjectorAdapter) storeInfo(info map[string]any) {
	a.infoMu.Lock()
	defer a.infoMu.Unlock()
	a.info, a.infoAt = info, time.Now()
}

func (a *NECProjectorAdapter) infoStale() bool {
	a.infoMu.Lock()
	defer a.infoMu.Unlock()
	return a.infoAt.IsZero() || len(a.info) == 0 || time.Since(a.infoAt) > necInfoEvery
}

func (a *NECProjectorAdapter) cachedInfo() map[string]any {
	a.infoMu.Lock()
	defer a.infoMu.Unlock()
	out := make(map[string]any, len(a.info))
	for k, v := range a.info {
		out[k] = v
	}
	return out
}

// ── Capabilities ─────────────────────────────────────────────────────────

var necProjectorCapabilities = device.Capabilities{
	Power: device.PowerCapability{On: true, Off: true},
	Commands: []string{
		"power_on", "power_off",
		"input_hdmi1", "input_hdmi2", "input_displayport", "input_hdbaset", "input_computer", "input_select",
		"picture_mute", "picture_unmute", "sound_mute", "sound_unmute",
		"set_volume", "volume_up", "volume_down",
		"shutter_close", "shutter_open",
	},
	Metrics: []string{
		"power_status", "operation_status",
		"current_input", "signal_present", "display_content",
		"picture_mute", "sound_mute", "onscreen_mute", "volume",
		"light_source_hours", "light_source_life_pct", "filter_hours",
		"fault_count", "faults",
		"model", "serial_number", "mac_address",
		"response_ms",
	},
}

func (a *NECProjectorAdapter) Capabilities() device.Capabilities {
	c := necProjectorCapabilities
	if len(a.Cfg.Commands) > 0 {
		c.Commands = append([]string{}, c.Commands...)
		for name := range a.Cfg.Commands {
			c.Commands = append(c.Commands, name)
		}
	}
	return c
}

// ── Session and framing ──────────────────────────────────────────────────

type necSession struct {
	conn   net.Conn
	id     byte
	silent bool
}

func (a *NECProjectorAdapter) withConn(ctx context.Context, fn func(*necSession) error) error {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	d := net.Dialer{Timeout: necDialTimeout}
	conn, err := d.DialContext(ctx, "tcp", a.address)
	if err != nil {
		return fmt.Errorf("dial %s: %w", a.address, err)
	}
	defer conn.Close()
	return fn(&necSession{conn: conn, id: a.controlID})
}

func (a *NECProjectorAdapter) frame(cmd []byte) []byte { return necFrame(a.controlID, cmd) }

// necChecksum is the low byte of the sum of every byte (manual §2.2).
func necChecksum(b []byte) byte {
	var sum byte
	for _, x := range b {
		sum += x
	}
	return sum
}

// necFrame copies cmd, puts a non-zero control ID in ID1, and appends the
// checksum.
func necFrame(id byte, cmd []byte) []byte {
	f := append(make([]byte, 0, len(cmd)+1), cmd...)
	if id != 0 && len(f) > 2 {
		f[2] = id
	}
	return append(f, necChecksum(f))
}

func isNECError(err error) bool {
	var ne *NECError
	return errors.As(err, &ne)
}

func (s *necSession) request(cmd []byte) ([]byte, error) { return s.exchange(necReplyWait, cmd) }

// exchange sends cmd and returns the data part of its reply, a *NECError
// for a failed-command reply, or errNECNoReply when nothing comes back.
func (s *necSession) exchange(wait time.Duration, cmd []byte) ([]byte, error) {
	if s.silent {
		// It already stopped answering on this connection; don't wait
		// out a timeout for every remaining request.
		return nil, errNECNoReply
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := s.conn.Write(necFrame(s.id, cmd)); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		hdr, data, err := s.readReply(deadline)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				s.silent = true
				return nil, errNECNoReply
			}
			if errors.Is(err, io.EOF) {
				return nil, errors.New("projector closed the connection (another control system may be connected)")
			}
			return nil, err
		}
		if hdr[1] != cmd[1] || hdr[0]&0x0F != cmd[0]&0x0F {
			continue // a reply to something else; keep reading
		}
		if hdr[0]&0x80 != 0 {
			if len(data) < 2 {
				return nil, fmt.Errorf("short error reply % X", hdr)
			}
			return nil, &NECError{Err1: data[0], Err2: data[1]}
		}
		return data, nil
	}
}

// readReply reads one reply: 5 header bytes, LEN data bytes, checksum.
func (s *necSession) readReply(deadline time.Time) ([]byte, []byte, error) {
	_ = s.conn.SetReadDeadline(deadline)
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(s.conn, hdr); err != nil {
		return nil, nil, err
	}
	if hdr[0]&0x20 == 0 {
		return nil, nil, fmt.Errorf("unexpected reply header % X", hdr)
	}
	rest := make([]byte, int(hdr[4])+1)
	if _, err := io.ReadFull(s.conn, rest); err != nil {
		return nil, nil, err
	}
	data := rest[:len(rest)-1]
	if necChecksum(append(append([]byte{}, hdr...), data...)) != rest[len(rest)-1] {
		return nil, nil, fmt.Errorf("bad checksum in reply % X % X", hdr, rest)
	}
	return hdr, data, nil
}

// ── Parsers ──────────────────────────────────────────────────────────────

type necRunning struct {
	power     string // on, standby, warming, cooling
	operation string // detail from DATA06
}

func (s *necSession) runningStatus() (necRunning, error) {
	d, err := s.request(necCmdRunningStatus)
	if err != nil {
		return necRunning{}, err
	}
	rs, ok := necParseRunningStatus(d)
	if !ok {
		return necRunning{}, fmt.Errorf("short running status reply (%d bytes)", len(d))
	}
	return rs, nil
}

// necOperation maps the [078-2] DATA06 / [305-3] DATA01 operation status.
var necOperation = map[byte]string{
	0x00: "standby_sleep",
	0x04: "power_on",
	0x05: "cooling",
	0x06: "standby_error",
	0x0F: "standby_power_saving",
	0x10: "network_standby",
}

// necParseRunningStatus reads [078-2]: DATA03 power status (00h standby,
// 01h on), DATA04 cooling (01h during), DATA05 power on/off process (01h
// during), DATA06 operation status.
func necParseRunningStatus(d []byte) (necRunning, bool) {
	if len(d) < 6 {
		return necRunning{}, false
	}
	power, cooling, process, op := d[2], d[3], d[4], d[5]
	rs := necRunning{operation: necOperation[op]}
	switch {
	case cooling == 0x01 || op == 0x05:
		rs.power = "cooling"
	case process == 0x01:
		// Power on/off in progress and not cooling: powering on.
		rs.power = "warming"
	case power == 0x01 || op == 0x04:
		rs.power = "on"
	case power == 0x00 || op == 0x00 || op == 0x06 || op == 0x0F || op == 0x10:
		rs.power = "standby"
	default:
		rs.power = fmt.Sprintf("unknown_0x%02x", op)
	}
	return rs, true
}

type necInput struct {
	label   string
	content byte // [078-3] DATA09 / [305-3] DATA02; 0xFF = not reported
}

func (in necInput) apply(m map[string]any) {
	if in.label != "" {
		m["current_input"] = in.label
	}
	switch in.content {
	case 0x00:
		m["signal_present"] = true
		m["display_content"] = "video"
	case 0x01:
		m["signal_present"] = false
		m["display_content"] = "no_signal"
	case 0x02:
		m["display_content"] = "viewer"
	case 0x03, 0x05:
		m["display_content"] = "test_pattern"
	case 0x04:
		m["display_content"] = "lan"
	case 0x10:
		m["display_content"] = "switching"
	}
}

// necInputLabel names the input from the selection signal types (appendix
// "Selection signal type"): type 2 is the terminal kind, type 1 its number.
func necInputLabel(t1, t2 byte) string {
	n := int(t1)
	num := func(base string) string {
		if n <= 1 {
			return base
		}
		return fmt.Sprintf("%s%d", base, n)
	}
	switch t2 {
	case 0x01:
		return num("computer")
	case 0x02:
		return num("video")
	case 0x03:
		return num("svideo")
	case 0x04:
		return num("component")
	case 0x07, 0x23:
		return num("viewer")
	case 0x20:
		return num("dvi")
	case 0x21:
		return fmt.Sprintf("hdmi%d", max(n, 1))
	case 0x22:
		return num("displayport")
	case 0x27:
		return "hdbaset"
	case 0x28:
		return num("sdi")
	case 0xFF:
		return "none"
	}
	return fmt.Sprintf("signal_%02x_%02x", t1, t2)
}

// necParseInputStatus reads [078-3]: DATA03 selection signal type 1,
// DATA04 selection signal type 2, DATA09 content displayed.
func necParseInputStatus(d []byte) (necInput, bool) {
	if len(d) < 4 {
		return necInput{}, false
	}
	in := necInput{label: necInputLabel(d[2], d[3]), content: 0xFF}
	if len(d) >= 9 {
		in.content = d[8]
	}
	return in, true
}

type necMute struct{ picture, sound, onscreen byte }

func (mu necMute) apply(m map[string]any) {
	for k, v := range map[string]byte{"picture_mute": mu.picture, "sound_mute": mu.sound, "onscreen_mute": mu.onscreen} {
		if v == 0x00 || v == 0x01 {
			m[k] = v == 0x01
		}
	}
}

// necParseMuteStatus reads [078-4]: DATA01 picture, DATA02 sound, DATA03
// onscreen mute (00h off, 01h on, FFh not supported).
func necParseMuteStatus(d []byte) (necMute, bool) {
	if len(d) < 3 {
		return necMute{}, false
	}
	return necMute{picture: d[0], sound: d[1], onscreen: d[2]}, true
}

type necBasic struct {
	operation string
	input     necInput
	mute      necMute
}

// necParseBasicInfo reads [305-3]. The data part starts with the 02h
// sub-command echo, then DATA01 operation status, DATA02 content
// displayed, DATA03/04 selection signal types 1/2, DATA05 display signal
// type, DATA06 video mute, DATA07 sound mute, DATA08 onscreen mute.
func necParseBasicInfo(d []byte) (necBasic, bool) {
	if len(d) < 9 || d[0] != 0x02 {
		return necBasic{}, false
	}
	return necBasic{
		operation: necOperation[d[1]],
		input:     necInput{label: necInputLabel(d[3], d[4]), content: d[2]},
		mute:      necMute{picture: d[6], sound: d[7], onscreen: d[8]},
	}, true
}

// necLampValue reads a [037-4] reply: DATA01 target, DATA02 content,
// DATA03-06 the value (little-endian, signed: remaining life goes
// negative past the replacement deadline).
func necLampValue(d []byte, content byte) (int, bool) {
	if len(d) < 6 || d[1] != content {
		return 0, false
	}
	return int(int32(binary.LittleEndian.Uint32(d[2:6]))), true
}

// necFilterSeconds reads [037-3]: DATA01-04 filter usage time in seconds
// (-1 when not defined).
func necFilterSeconds(d []byte) (int, bool) {
	if len(d) < 4 {
		return 0, false
	}
	return int(int32(binary.LittleEndian.Uint32(d[0:4]))), true
}

type necGain struct {
	status                byte
	upper, lower, current int
}

// necParseGain reads [060-1]: DATA01 status (02h adjustable), DATA02-03
// upper limit, DATA04-05 lower limit, DATA06-07 default, DATA08-09
// current value, all little-endian signed 16-bit.
func necParseGain(d []byte) (necGain, bool) {
	if len(d) < 9 {
		return necGain{}, false
	}
	le := func(i int) int { return int(int16(binary.LittleEndian.Uint16(d[i : i+2]))) }
	return necGain{status: d[0], upper: le(1), lower: le(3), current: le(7)}, true
}

// necFaultBits is the [009] error information list: data byte index, bit,
// fault type and description. "Lamp" is the light source on laser models.
var necFaultBits = []struct {
	idx, bit    int
	kind, descr string
}{
	{0, 0, "cover", "Cover error"},
	{0, 1, "temperature", "Temperature error (bi-metallic strip)"},
	{0, 3, "fan", "Fan error"},
	{0, 4, "fan", "Fan error"},
	{0, 5, "power", "Power error"},
	{0, 6, "light_source", "Lamp / light source off"},
	{0, 7, "light_source", "Lamp / light source in replacement moratorium"},
	{1, 0, "light_source", "Lamp / light source usage time exceeded the limit"},
	{1, 1, "formatter", "Formatter error"},
	{1, 2, "light_source", "Lamp 2 off"},
	{2, 1, "fpga", "FPGA error"},
	{2, 2, "temperature", "Temperature error (temperature sensor)"},
	{2, 3, "light_source", "Lamp / light source not present"},
	{2, 4, "light_source", "Lamp / light source data error"},
	{2, 5, "cover", "Mirror cover error"},
	{2, 6, "light_source", "Lamp 2 in replacement moratorium"},
	{2, 7, "light_source", "Lamp 2 usage time exceeded the limit"},
	{3, 0, "light_source", "Lamp 2 not present"},
	{3, 1, "light_source", "Lamp 2 data error"},
	{3, 2, "temperature", "Temperature error due to dust"},
	{3, 3, "sensor", "Foreign matter sensor error"},
	{3, 5, "light_source", "Ballast communication error"},
	{3, 6, "iris", "Iris calibration error"},
	{3, 7, "lens", "Lens not installed properly"},
	{8, 0, "cover", "Portrait cover side is up"},
	{8, 1, "interlock", "Interlock switch is open"},
	{8, 2, "system", "System error (slave CPU)"},
	{8, 3, "system", "System error (formatter)"},
}

// necDecodeErrors turns a [009] reply (DATA01-12) into the fault list, in
// the Tesira / Cisco shape. "Lamp off" is only a fault while the
// projector is meant to be on: in standby the light source is off anyway.
func necDecodeErrors(d []byte, poweredOn bool) []any {
	faults := []any{}
	for _, f := range necFaultBits {
		if f.idx >= len(d) || d[f.idx]&(1<<f.bit) == 0 {
			continue
		}
		if f.idx == 0 && f.bit == 6 && !poweredOn {
			continue
		}
		faults = append(faults, map[string]any{"level": "Error", "type": f.kind, "description": f.descr})
	}
	return faults
}

// identity reads model [078-5], serial [305-2] and MAC [097-155]; any the
// projector doesn't support are left out. The manual has no firmware
// version request, so none is reported.
func (s *necSession) identity() map[string]any {
	out := map[string]any{}
	if d, err := s.request(necCmdModelName); err == nil {
		if v := necString(d); v != "" {
			out["model"] = v
		}
	}
	if d, err := s.request(necCmdSerialNumber); err == nil && len(d) > 2 && d[0] == 0x01 && d[1] == 0x06 {
		if v := necString(d[2:]); v != "" {
			out["serial_number"] = v
		}
	}
	if d, err := s.request(necCmdMACAddress); err == nil && len(d) >= 8 && d[0] == 0x9A {
		mac := net.HardwareAddr(d[2:8])
		if mac.String() != "00:00:00:00:00:00" {
			out["mac_address"] = strings.ToUpper(mac.String())
		}
	}
	return out
}

// necString reads a NUL-terminated string.
func necString(d []byte) string {
	if i := strings.IndexByte(string(d), 0); i >= 0 {
		d = d[:i]
	}
	return strings.TrimSpace(string(d))
}
