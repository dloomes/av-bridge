package adapters

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// fakeATEN mimics the PE6108G CLI: every connection is login → one
// command → reply → close. It counts logins and commands.
type fakeATEN struct {
	ln     net.Listener
	logins atomic.Int64
	mu     sync.Mutex
	cmds   map[string]int
	// busyFor: a connection arriving this soon after the previous
	// session closed is dropped at once, as a PE6108G in the field did.
	busyFor   time.Duration
	refuseAll bool
	password  string // when set, a different password is rejected
	// hangUpOnBadPassword: reject by closing the session, not re-prompting.
	hangUpOnBadPassword bool
	// junk: logged-in sessions reply with text that holds no readings.
	junk bool
	// refuseNames: answer the name read as some firmwares do.
	refuseNames bool
	// legacySwitch: accept only the older "sw o01 on imme" form.
	legacySwitch bool
	lastClose    time.Time
	refused      atomic.Int64
}

func newFakeATEN(t *testing.T) *fakeATEN {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeATEN{ln: ln, cmds: map[string]int{}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeATEN) serve(c net.Conn) {
	f.mu.Lock()
	busy := f.refuseAll || (!f.lastClose.IsZero() && time.Since(f.lastClose) < f.busyFor)
	f.mu.Unlock()
	if busy {
		f.refused.Add(1)
		c.Close()
		return
	}
	defer func() {
		c.Close()
		f.mu.Lock()
		f.lastClose = time.Now()
		f.mu.Unlock()
	}()
	r := bufio.NewReader(c)
	c.Write([]byte("\r\nATEN PE6108G\r\nLogin: "))
	if _, err := r.ReadString('\n'); err != nil {
		return
	}
	c.Write([]byte("Password: "))
	pw, err := r.ReadString('\n')
	if err != nil {
		return
	}
	if f.password != "" && strings.TrimSpace(pw) != f.password {
		if f.hangUpOnBadPassword {
			return
		}
		// Like the real PDU: reject and ask again. Whatever is sent next
		// is taken as a username.
		c.Write([]byte("\r\nLogin incorrect\r\nLogin: "))
		if _, err := r.ReadString('\n'); err != nil {
			return
		}
		c.Write([]byte("Password: "))
		r.ReadString('\n')
		return
	}
	f.logins.Add(1)
	c.Write([]byte("\r\n> "))
	line, err := r.ReadString('\n')
	if err != nil {
		return
	}
	cmd := strings.TrimSpace(line)
	f.mu.Lock()
	f.cmds[cmd]++
	f.mu.Unlock()
	var reply string
	switch {
	case strings.HasSuffix(cmd, " name") && f.refuseNames:
		reply = "Invalid command or exceed max command length"
	case strings.HasSuffix(cmd, " name"):
		reply = "Display"
	case strings.HasPrefix(cmd, "read status"):
		reply = "on"
	// read meter, as ATEN's Telnet guide documents it: a bare value.
	case strings.HasPrefix(cmd, "read meter olt ") && strings.HasSuffix(cmd, " curr simple"):
		reply = "0.5"
	case strings.HasPrefix(cmd, "read meter olt ") && strings.HasSuffix(cmd, " pow simple"):
		reply = "110"
	case cmd == "read meter dev volt simple":
		reply = "230.1"
	case cmd == "read meter dev pow simple":
		reply = "880"
	case cmd == "read meter dev curr simple":
		reply = "4.2"
	case strings.HasPrefix(cmd, "sw ") && f.legacySwitch && strings.HasSuffix(cmd, " imme"):
		reply = "Outlet01 command is setting"
	case strings.HasPrefix(cmd, "sw ") && !f.legacySwitch && strings.Contains(cmd, " imme "):
		reply = "Outlet01 command is setting"
	default:
		reply = "Invalid command or exceed max command length"
	}
	if f.junk {
		reply = "Main Menu"
	}
	c.Write([]byte(reply + "\r\n> "))
}

func (f *fakeATEN) count(prefix, suffix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for c, k := range f.cmds {
		if strings.HasPrefix(c, prefix) && strings.HasSuffix(c, suffix) {
			n += k
		}
	}
	return n
}

func newTestATEN(f *fakeATEN, tags map[string]string) *ATENPDUAdapter {
	if tags == nil {
		tags = map[string]string{}
	}
	tags["outlet_count"] = "2"
	return NewATENPDUAdapter(config.DeviceConfig{
		ID: "pdu", Address: f.ln.Addr().String(), Username: "admin", Password: "pw", Tags: tags,
	})
}

// Outlet names cost a login each, so they're read on the first poll and
// then reused until atenNameRefresh, not re-read every poll.
func TestATENOutletNamesAreCached(t *testing.T) {
	f := newFakeATEN(t)
	a := newTestATEN(f, nil)
	ctx := context.Background()

	tel, _ := a.Poll(ctx)
	if tel.Metrics["outlet_1_name"] != "Display" {
		t.Fatalf("first poll should read names, got %v", tel.Metrics["outlet_1_name"])
	}
	first := f.logins.Load()
	if f.count("read status", " name") != 2 {
		t.Fatalf("want 2 name reads on the first poll, got %d", f.count("read status", " name"))
	}

	tel, _ = a.Poll(ctx)
	if tel.Metrics["outlet_1_name"] != "Display" {
		t.Error("cached name should still be reported")
	}
	if n := f.count("read status", " name"); n != 2 {
		t.Errorf("names re-read on the second poll (%d reads)", n)
	}
	if second := f.logins.Load() - first; second != first-2 {
		t.Errorf("second poll used %d logins, want %d (first poll minus the name reads)", second, first-2)
	}

	// After the refresh interval the names are read again.
	a.namesMu.Lock()
	a.namesRead = time.Now().Add(-2 * atenNameRefresh)
	a.namesMu.Unlock()
	a.Poll(ctx)
	if n := f.count("read status", " name"); n != 4 {
		t.Errorf("names should refresh after atenNameRefresh, got %d reads", n)
	}
}

// A PDU that refuses the name read gets no names (not its error text as
// every outlet's name), keeps its other readings, and isn't asked again.
func TestATENRefusedNameReadIsNotAName(t *testing.T) {
	f := newFakeATEN(t)
	f.refuseNames = true
	a := newTestATEN(f, nil)
	ctx := context.Background()

	tel, _ := a.Poll(ctx)
	if v, ok := tel.Metrics["outlet_1_name"]; ok {
		t.Errorf("outlet_1_name = %q, want none", v)
	}
	if tel.Metrics["outlet_1_state"] != "on" || tel.Error != "" {
		t.Errorf("other readings should be unaffected: %v (error %q)", tel.Metrics, tel.Error)
	}
	if n := f.count("read status", " name"); n != 2 {
		t.Fatalf("want 2 name reads on the first poll, got %d", n)
	}

	a.namesMu.Lock()
	a.namesRead = time.Now().Add(-2 * atenNameRefresh)
	a.namesMu.Unlock()
	a.Poll(ctx)
	if n := f.count("read status", " name"); n != 2 {
		t.Errorf("a PDU that refused names was asked again (%d reads)", n)
	}
}

// A name set as a tag is used as-is and never read from the PDU.
func TestATENTagNameSkipsRead(t *testing.T) {
	f := newFakeATEN(t)
	a := newTestATEN(f, map[string]string{"outlet_1_name": "Codec", "outlet_2_name": "Screen"})
	tel, _ := a.Poll(context.Background())
	if tel.Metrics["outlet_1_name"] != "Codec" || f.count("read status", " name") != 0 {
		t.Fatalf("tag names: got %v with %d reads", tel.Metrics["outlet_1_name"], f.count("read status", " name"))
	}
}

// No heartbeat (it would cost a login every 30s for no benefit), and a
// PDU without a poll rate is polled every 5 minutes.
func TestATENNoHeartbeatAndSlowDefaultPoll(t *testing.T) {
	a := NewATENPDUAdapter(config.DeviceConfig{ID: "pdu", Address: "127.0.0.1"})
	if _, ok := any(a).(device.Heartbeater); ok {
		t.Error("ATEN adapter must not implement Heartbeater")
	}
	if r := device.PollRateFor(a); r != 5*time.Minute {
		t.Errorf("default poll rate = %v, want 5m", r)
	}
	a.Cfg.PollRate = 2 * time.Minute
	if r := device.PollRateFor(a); r != 2*time.Minute {
		t.Errorf("an explicit poll rate must win, got %v", r)
	}
}

// A device saved without a poll rate must not get a zero ticker interval,
// which panics and takes the whole collector down.
func TestPollRateNeverZero(t *testing.T) {
	d := NewPingAdapter(config.DeviceConfig{ID: "p"})
	if r := device.PollRateFor(d); r != device.DefaultPollRate {
		t.Fatalf("unset poll rate = %v, want %v", r, device.DefaultPollRate)
	}
}

// A PDU that needs a moment between sessions: every reading must still
// succeed (the adapter paces sessions and retries once).
func TestATENPacesSessionsForBusyPDU(t *testing.T) {
	f := newFakeATEN(t)
	f.busyFor = 250 * time.Millisecond
	a := newTestATEN(f, nil)
	tel, _ := a.Poll(context.Background())
	if tel.Status != device.StatusOnline || tel.Error != "" {
		t.Fatalf("status %s, error %q — want every reading to succeed", tel.Status, tel.Error)
	}
	if tel.Metrics["outlet_1_power_w"] == nil || tel.Metrics["voltage_v"] == nil {
		t.Errorf("readings missing: %v", tel.Metrics)
	}
}

// Meter readings use ATEN's documented syntax and its bare-value replies.
func TestATENMeterReadings(t *testing.T) {
	f := newFakeATEN(t)
	a := newTestATEN(f, nil)
	tel, _ := a.Poll(context.Background())
	m := tel.Metrics
	if m["outlet_1_current_a"] != 0.5 || m["outlet_2_power_w"] != 110.0 ||
		m["voltage_v"] != 230.1 || m["total_power_w"] != 880.0 || m["total_current_a"] != 4.2 {
		t.Errorf("readings = %v", m)
	}
	if tel.Error != "" {
		t.Errorf("error = %q; every documented command should be accepted", tel.Error)
	}
	if n := f.count("read meter olt o01", " curr simple"); n != 1 {
		t.Errorf("outlet current reads = %d", n)
	}
}

// Switching sends the PE/PG guide's form; a PDU that only takes the older
// form gets it, and the adapter remembers.
func TestATENSwitchForms(t *testing.T) {
	ctx := context.Background()
	f := newFakeATEN(t)
	a := newTestATEN(f, nil)
	if _, err := a.SendCommand(ctx, device.CommandRequest{Name: "outlet_off", Args: map[string]any{"outlet": 2}}); err != nil {
		t.Fatal(err)
	}
	if f.count("sw o02 imme off", "") != 1 {
		t.Errorf("want the newer form, got %v", f.cmds)
	}

	old := newFakeATEN(t)
	old.legacySwitch = true
	b := newTestATEN(old, nil)
	if _, err := b.SendCommand(ctx, device.CommandRequest{Name: "outlet_on", Args: map[string]any{"outlet": 1}}); err != nil {
		t.Fatalf("older firmware: %v", err)
	}
	if old.count("sw o01 imme on", "") != 1 || old.count("sw o01 on imme", "") != 1 {
		t.Errorf("want newer form refused then older form, got %v", old.cmds)
	}
	b.SendCommand(ctx, device.CommandRequest{Name: "outlet_reboot", Args: map[string]any{"outlet": 1}})
	if old.count("sw o01 imme reboot", "") != 0 || old.count("sw o01 reboot imme", "") != 1 {
		t.Errorf("second command should go straight to the older form, got %v", old.cmds)
	}
}

func TestParseMeterValue(t *testing.T) {
	cases := []struct {
		resp string
		want float64
		ok   bool
	}{
		{"0.5\r\n> ", 0.5, true},
		{"read meter olt o01 curr simple\r\n0.52\r\n> ", 0.52, true}, // echo's "o01" isn't the value
		{"C:1.25", 1.25, true},
		{"POW:-3", -3, true},
		{"current: 0.5 A", 0.5, true},
		{"Not Support", 0, false},
		{"Command incorrect", 0, false},
		{"Invalid command or exceed max command length", 0, false},
		{"> ", 0, false},
	}
	for _, c := range cases {
		got, ok := parseMeterValue(c.resp)
		if ok != c.ok || got != c.want {
			t.Errorf("parseMeterValue(%q) = %v, %v; want %v, %v", c.resp, got, ok, c.want, c.ok)
		}
	}
}

// A PDU that refuses every session is offline, with the reason shown —
// not "online" with an empty Device Information panel.
func TestATENAllReadsFailingIsOfflineWithReason(t *testing.T) {
	f := newFakeATEN(t)
	f.refuseAll = true
	a := newTestATEN(f, nil)
	tel, _ := a.Poll(context.Background())
	if tel.Status != device.StatusOffline {
		t.Errorf("status %s, want offline", tel.Status)
	}
	if !strings.Contains(tel.Error, "readings failed") || tel.Metrics["failed_reads"] == nil {
		t.Errorf("want the failure reported, got error %q metrics %v", tel.Error, tel.Metrics)
	}
}

// Wrong Telnet credentials: the PDU re-prompts for the login. That must
// be reported as a rejected login and the PDU shown offline — not a
// "Password:" prompt turning up as an outlet name.
func TestATENRejectedLoginIsReported(t *testing.T) {
	f := newFakeATEN(t)
	f.password = "telpwd"
	a := newTestATEN(f, nil) // sends "pw"
	if err := a.Connect(context.Background()); err == nil || !strings.Contains(err.Error(), "login rejected") {
		t.Fatalf("connect with wrong password: %v, want login rejected", err)
	}
	tel, _ := a.Poll(context.Background())
	if tel.Status != device.StatusOffline || !strings.Contains(tel.Error, "login rejected") {
		t.Fatalf("status %s error %q, want offline with login rejected", tel.Status, tel.Error)
	}
	for k, v := range tel.Metrics {
		if s, ok := v.(string); ok && strings.Contains(strings.ToLower(s), "password") {
			t.Errorf("prompt reported as a reading: %s = %q", k, s)
		}
	}
}

// Some firmware rejects a bad login by hanging up. Empty sessions must not
// count as readings and leave the PDU online.
func TestATENHangUpOnBadPasswordIsOffline(t *testing.T) {
	f := newFakeATEN(t)
	f.password = "telpwd"
	f.hangUpOnBadPassword = true
	a := newTestATEN(f, nil)
	tel, _ := a.Poll(context.Background())
	if tel.Status != device.StatusOffline || !strings.Contains(tel.Error, "login rejected") {
		t.Fatalf("status %s error %q, want offline with login rejected", tel.Status, tel.Error)
	}
}

// Replies that hold no readings at all: offline with a reason, not online
// with only outlet_count and response_ms.
func TestATENNoReadingsIsOffline(t *testing.T) {
	f := newFakeATEN(t)
	f.junk = true
	a := newTestATEN(f, nil)
	tel, _ := a.Poll(context.Background())
	if tel.Status != device.StatusOffline || !strings.Contains(tel.Error, "no readings") {
		t.Fatalf("status %s error %q, want offline with no readings", tel.Status, tel.Error)
	}
}
