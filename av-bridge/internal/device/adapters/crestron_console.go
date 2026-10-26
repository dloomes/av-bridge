package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// CrestronAdapter monitors a Crestron 3-Series or 4-Series control
// processor through its text console, with no change to the room's
// program. The console is reached over SSH (port 22, the default), or over
// CTP (TCP 41795) with tags.console: ctp where SSH is off.
//
// Session model: one console session per poll or command, as on the
// NETGEAR switch. The console prompt is the model name and ">" ("CP4>").
//
// Poll reads:
//
//	ver                     model, firmware, TSID, MAC
//	hostname                host name
//	uptime                  time since boot
//	cpuload / ramfree       load and free memory
//	progcomments:<slot>     the program loaded in the slot (tags.program_slot, default 1)
//	iptable -p:all -t       every IP-ID device the program registers, online or not
//
// Console output differs between series and firmware. Each reading is
// parsed tolerantly; output a reading can't be found in is put on the
// device page as <command>_reply, so it can be seen and the parsing
// tightened. The program's "Programmer" comment is never read: it's a
// person's name.
type CrestronAdapter struct {
	device.Base
	address string
	ctp     bool
	slot    int

	sessMu sync.Mutex

	// ipids holds the IP IDs seen at the last poll, so Capabilities can
	// list the per-device readings this processor really has.
	ipMu  sync.RWMutex
	ipids []string
}

const (
	crestronSSHPort    = 22
	crestronCTPPort    = 41795
	crestronLoginWait  = 10 * time.Second
	crestronCmdTimeout = 20 * time.Second
)

func NewCrestronAdapter(cfg config.DeviceConfig) *CrestronAdapter {
	ctp := strings.EqualFold(strings.TrimSpace(cfg.Tags["console"]), "ctp")
	port := crestronSSHPort
	if ctp {
		port = crestronCTPPort
	}
	addr := cfg.Address
	if addr != "" && !strings.Contains(addr, ":") {
		addr = fmt.Sprintf("%s:%d", addr, port)
	}
	slot := 1
	if n, err := strconv.Atoi(strings.TrimSpace(cfg.Tags["program_slot"])); err == nil && n >= 1 && n <= 10 {
		slot = n
	}
	return &CrestronAdapter{Base: device.NewBase(cfg), address: addr, ctp: ctp, slot: slot}
}

// ── Connection ───────────────────────────────────────────────────────────

// Connect proves the address and login by reading `ver`. No session is
// kept between polls.
func (a *CrestronAdapter) Connect(ctx context.Context) error {
	var out string
	err := a.withConsole(ctx, func(c *crestronConsole) error {
		var err error
		out, err = c.run("ver")
		return err
	})
	if err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("crestron connect %s (%s): %w", a.Cfg.ID, a.address, err)
	}
	v := crestronParseVer(out)
	a.SetStatus(device.StatusOnline)
	slog.Info("crestron connected", "device", a.Cfg.ID, "address", a.address,
		"model", v["model"], "firmware", v["firmware"])
	return nil
}

func (a *CrestronAdapter) Disconnect() error {
	a.SetStatus(device.StatusOffline)
	return nil
}

// ── Poll ─────────────────────────────────────────────────────────────────

func (a *CrestronAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	t := a.BaseTelemetry()
	start := time.Now()
	m := map[string]any{}

	err := a.withConsole(ctx, func(c *crestronConsole) error {
		// ver first: it's the one every processor answers.
		out, err := c.run("ver")
		if err != nil {
			return err
		}
		if v := crestronParseVer(out); v["model"] != "" {
			for k, val := range v {
				m[k] = val
			}
		} else {
			crestronUnread(m, "ver", out)
		}
		if c.hostKey != "" {
			m["ssh_host_key"] = c.hostKey
		}

		// The rest are best-effort. A command the firmware doesn't know
		// answers "Bad or Incomplete Command" and the reading is left out.
		if out, ok := c.try("hostname"); ok {
			if v := crestronHostname(out); v != "" {
				m["hostname"] = v
			} else {
				crestronUnread(m, "hostname", out)
			}
		}
		if out, ok := c.try("uptime"); ok {
			if s, ok := crestronUptime(out); ok {
				m["uptime_s"] = s
			} else {
				crestronUnread(m, "uptime", out)
			}
		}
		if out, ok := c.try("cpuload"); ok {
			if v, ok := crestronPercent(out); ok {
				m["cpu_load_pct"] = v
			} else {
				crestronUnread(m, "cpuload", out)
			}
		}
		if out, ok := c.try("ramfree"); ok {
			if r := crestronRAM(out); len(r) > 0 {
				for k, v := range r {
					m[k] = v
				}
			} else {
				crestronUnread(m, "ramfree", out)
			}
		}
		// 4-Series takes the slot; 3-Series has one program and no slot.
		if out, ok := c.try(fmt.Sprintf("progcomments:%d", a.slot), "progcomments"); ok {
			if p := crestronProgram(out); len(p) > 0 {
				for k, v := range p {
					m[k] = v
				}
			} else {
				crestronUnread(m, "progcomments", out)
			}
		}
		if out, ok := c.try("iptable -p:all -t", "iptable -t", "iptable"); ok {
			if rows, ok := crestronIPTable(out); ok {
				a.applyIPTable(m, rows)
			} else {
				crestronUnread(m, "iptable", out)
			}
		}
		return nil
	})
	if err != nil {
		a.SetStatus(device.StatusOffline)
		t.Status = device.StatusOffline
		t.Error = err.Error()
		return t, nil
	}
	a.SetStatus(device.StatusOnline)
	t.Status = device.StatusOnline
	m["response_ms"] = time.Since(start).Milliseconds()
	t.Metrics = m
	return t, nil
}

// applyIPTable turns the IP table into counts, a list of what's offline,
// and per-device status readings (ipid_03_status and so on).
func (a *CrestronAdapter) applyIPTable(m map[string]any, rows []crestronIPRow) {
	online, offline := 0, []string{}
	ids := []string{}
	for _, r := range rows {
		key := "ipid_" + strings.ToLower(r.ID)
		if r.Program != "" && r.Program != "1" && !strings.EqualFold(r.Program, "01") {
			key = fmt.Sprintf("p%s_%s", strings.TrimLeft(r.Program, "0"), key)
		}
		ids = append(ids, key)
		m[key+"_status"] = r.Status
		if r.Model != "" {
			m[key+"_model"] = r.Model
		}
		if r.Address != "" {
			m[key+"_address"] = r.Address
		}
		if r.Description != "" {
			m[key+"_description"] = r.Description
		}
		if r.Status == "online" {
			online++
			continue
		}
		label := r.ID
		switch {
		case r.Description != "":
			label += " " + r.Description
		case r.Model != "":
			label += " " + r.Model
		}
		if r.Address != "" {
			label += " (" + r.Address + ")"
		}
		offline = append(offline, label)
	}
	m["ip_devices"] = len(rows)
	m["ip_devices_online"] = online
	m["ip_devices_offline"] = len(offline)
	if len(offline) > 0 {
		m["offline_ip_devices"] = strings.Join(offline, ", ")
	}
	sort.Strings(ids)
	a.ipMu.Lock()
	a.ipids = ids
	a.ipMu.Unlock()
}

// crestronUnread puts console output a reading couldn't be found in on
// the device page.
func crestronUnread(m map[string]any, cmd, out string) {
	s := collapse(out)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	m[strings.ReplaceAll(strings.Fields(cmd)[0], ":", "_")+"_reply"] = s
}

// ── Parsing ──────────────────────────────────────────────────────────────

var (
	// "CP4 Cntrl Eng [v2.8001.00066 (Mar 14 2024), #00F0C73C] @E-00107fe3c2a1"
	crestronVerRE  = regexp.MustCompile(`(?i)([A-Z][A-Z0-9-]*)\s+Cntrl\s+Eng\s*\[\s*v?([0-9][0-9.]*)(?:\s*\(([^)]*)\))?(?:,\s*#([0-9A-F]+))?`)
	crestronMACRE  = regexp.MustCompile(`(?i)@E-([0-9A-F]{12})`)
	crestronHostRE = regexp.MustCompile(`(?im)^\s*host\s*name\s*[:.]*\s*(\S+)`)
	crestronPctRE  = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*%`)
	crestronUpRE   = map[string]*regexp.Regexp{
		"day":    regexp.MustCompile(`(?i)(\d+)\s*days?`),
		"hour":   regexp.MustCompile(`(?i)(\d+)\s*hours?`),
		"minute": regexp.MustCompile(`(?i)(\d+)\s*min(?:ute)?s?`),
		"second": regexp.MustCompile(`(?i)(\d+)\s*sec(?:ond)?s?`),
	}
	crestronBadCmdRE = regexp.MustCompile(`(?i)(bad or incomplete command|unknown command|invalid command|command not found)`)
)

func crestronParseVer(out string) map[string]string {
	v := map[string]string{}
	if mm := crestronVerRE.FindStringSubmatch(out); mm != nil {
		v["model"] = strings.ToUpper(mm[1])
		v["firmware"] = mm[2]
		if mm[3] != "" {
			v["firmware_date"] = strings.TrimSpace(mm[3])
		}
		if mm[4] != "" {
			v["tsid"] = strings.ToUpper(mm[4])
		}
	}
	if mm := crestronMACRE.FindStringSubmatch(out); mm != nil {
		h := strings.ToLower(mm[1])
		parts := make([]string, 0, 6)
		for i := 0; i < 12; i += 2 {
			parts = append(parts, h[i:i+2])
		}
		v["mac"] = strings.Join(parts, ":")
	}
	if len(v) > 0 && v["model"] == "" {
		return map[string]string{}
	}
	return v
}

func crestronHostname(out string) string {
	if mm := crestronHostRE.FindStringSubmatch(out); mm != nil {
		return mm[1]
	}
	return ""
}

// crestronUptime reads "The system has been running for 12 days, 3 hours,
// 46 minutes, 15 seconds." as seconds.
func crestronUptime(out string) (int, bool) {
	mult := map[string]int{"day": 86400, "hour": 3600, "minute": 60, "second": 1}
	total, found := 0, false
	for unit, re := range crestronUpRE {
		if mm := re.FindStringSubmatch(out); mm != nil {
			n, _ := strconv.Atoi(mm[1])
			total += n * mult[unit]
			found = true
		}
	}
	return total, found
}

func crestronPercent(out string) (float64, bool) {
	mm := crestronPctRE.FindStringSubmatch(out)
	if mm == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(mm[1], 64)
	return f, err == nil
}

var crestronRAMLineRE = regexp.MustCompile(`(?i)(total|free|used|available)[^:\n]*?:?\s*([0-9][0-9,.]*)\s*(bytes|kb|kib|mb|mib|gb|gib|k|m|g)?\b`)

// crestronRAM reads free (and, where shown, total) memory in MB, and a
// used percentage when both are there.
func crestronRAM(out string) map[string]any {
	mb := map[string]float64{}
	for _, mm := range crestronRAMLineRE.FindAllStringSubmatch(out, -1) {
		n, err := strconv.ParseFloat(strings.ReplaceAll(mm[2], ",", ""), 64)
		if err != nil {
			continue
		}
		switch strings.ToLower(mm[3]) {
		case "bytes", "":
			n /= 1 << 20
		case "kb", "kib", "k":
			n /= 1 << 10
		case "gb", "gib", "g":
			n *= 1 << 10
		}
		key := strings.ToLower(mm[1])
		if key == "available" {
			key = "free"
		}
		if _, seen := mb[key]; !seen {
			mb[key] = n
		}
	}
	r := map[string]any{}
	round := func(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
	if f, ok := mb["free"]; ok {
		r["ram_free_mb"] = round(f)
		if t, ok := mb["total"]; ok && t > 0 {
			r["ram_total_mb"] = round(t)
			r["ram_used_pct"] = round((t - f) / t * 100)
		}
	}
	return r
}

// crestronProgram reads the program's comments: which file is loaded,
// its system name and when it was compiled. Not the programmer's name.
func crestronProgram(out string) map[string]any {
	r := map[string]any{}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		switch {
		case k == "program file" || k == "source file" || k == "program name":
			if _, ok := r["program_file"]; !ok {
				r["program_file"] = v
			}
		case k == "system name" || k == "friendly name":
			if _, ok := r["program_name"]; !ok {
				r["program_name"] = v
			}
		case strings.HasPrefix(k, "compiled on") || k == "compile time" || k == "compiled":
			r["program_compiled"] = v
		case k == "program tool" || k == "compiler revision" || k == "cres db":
			if k == "program tool" {
				r["program_tool"] = v
			}
		}
	}
	return r
}

// crestronIPRow is one row of the IP table.
type crestronIPRow struct {
	ID, Status, Address, Model, Description, Program string
}

// crestronIPTable reads `iptable` output: a header row naming the columns,
// then one row per IP ID, columns separated by "|". Column positions are
// taken from the header, since -t and -p:all add columns.
func crestronIPTable(out string) ([]crestronIPRow, bool) {
	lines := strings.Split(strings.ReplaceAll(out, "\r", ""), "\n")
	col := map[string]int{}
	rows := []crestronIPRow{}
	header := false
	for _, line := range lines {
		if !strings.Contains(line, "|") {
			continue
		}
		cells := strings.Split(line, "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if !header {
			for i, c := range cells {
				lc := strings.ToLower(c)
				switch {
				case strings.Contains(lc, "cip") || lc == "ip id" || lc == "ipid" || lc == "id":
					col["id"] = i
				case strings.Contains(lc, "status"):
					col["status"] = i
				case strings.Contains(lc, "ip address") || strings.Contains(lc, "sitename"):
					col["address"] = i
				case strings.Contains(lc, "model"):
					col["model"] = i
				case strings.Contains(lc, "description"):
					col["description"] = i
				case lc == "prog" || strings.Contains(lc, "program"):
					col["program"] = i
				}
			}
			if _, ok := col["id"]; ok {
				if _, ok := col["status"]; ok {
					header = true
				}
			}
			continue
		}
		get := func(name string) string {
			if i, ok := col[name]; ok && i < len(cells) {
				return cells[i]
			}
			return ""
		}
		id := strings.ToUpper(get("id"))
		if id == "" || strings.Trim(id, "-=") == "" {
			continue
		}
		if len(id) == 1 {
			id = "0" + id
		}
		status := strings.ToLower(get("status"))
		switch {
		case strings.HasPrefix(status, "online"):
			status = "online"
		case strings.HasPrefix(status, "offline"):
			status = "offline"
		}
		rows = append(rows, crestronIPRow{
			ID: id, Status: status, Address: get("address"),
			Model: get("model"), Description: get("description"), Program: get("program"),
		})
	}
	return rows, header
}

// ── Commands ─────────────────────────────────────────────────────────────

const (
	crestronCmdReboot         = "reboot"
	crestronCmdRestartProgram = "restart_program"
)

func (a *CrestronAdapter) SendCommand(ctx context.Context, req device.CommandRequest) (*device.CommandResponse, error) {
	var line string
	switch req.Name {
	case crestronCmdReboot:
		line = "reboot"
	case crestronCmdRestartProgram:
		// 3-Series has one program and no slot argument; slot 1 there is
		// the same thing.
		line = fmt.Sprintf("progreset -p:%d", a.slot)
	default:
		return nil, fmt.Errorf("crestron: unsupported command %q", req.Name)
	}
	start := time.Now()
	var out string
	err := a.withConsole(ctx, func(c *crestronConsole) error {
		var err error
		if req.Name == crestronCmdReboot {
			// The processor drops the session as it goes down; that's the
			// command working, not failing.
			out, err = c.runLeaving(line)
			return err
		}
		out, err = c.run(line)
		if err == nil && crestronBadCmdRE.MatchString(out) && a.slot == 1 {
			out, err = c.run("progreset")
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if crestronBadCmdRE.MatchString(out) {
		return nil, fmt.Errorf("crestron: %s: %s", line, collapse(out))
	}
	return &device.CommandResponse{Raw: out, Latency: time.Since(start)}, nil
}

// ── Capabilities ─────────────────────────────────────────────────────────

var crestronBaseMetrics = []string{
	"model", "firmware", "firmware_date", "tsid", "mac", "hostname", "ssh_host_key",
	"uptime_s", "cpu_load_pct", "ram_free_mb", "ram_total_mb", "ram_used_pct",
	"program_file", "program_name", "program_compiled", "program_tool",
	"ip_devices", "ip_devices_online", "ip_devices_offline", "offline_ip_devices",
	"response_ms",
}

func (a *CrestronAdapter) Capabilities() device.Capabilities {
	a.ipMu.RLock()
	ids := append([]string(nil), a.ipids...)
	a.ipMu.RUnlock()
	metrics := append([]string(nil), crestronBaseMetrics...)
	for _, id := range ids {
		metrics = append(metrics, id+"_status")
	}
	return device.Capabilities{
		Commands: []string{crestronCmdRestartProgram, crestronCmdReboot},
		Metrics:  metrics,
	}
}

// ── Console session ──────────────────────────────────────────────────────

// crestronConsole is one logged-in console session, over SSH or CTP.
type crestronConsole struct {
	w       io.Writer
	out     chan []byte
	buf     strings.Builder
	prompt  string
	hostKey string // SHA256 fingerprint of the SSH host key; "" over CTP
	ctx     context.Context
	quit    chan struct{} // closed with the session: stops the reader
	broken  bool          // a command failed: the session can't be trusted
}

func (c *crestronConsole) close() { close(c.quit) }

// crestronSend writes one console line.
func crestronSend(w io.Writer, s string) error {
	_, err := io.WriteString(w, s+"\r\n")
	return err
}

func (a *CrestronAdapter) withConsole(ctx context.Context, fn func(*crestronConsole) error) error {
	a.sessMu.Lock()
	defer a.sessMu.Unlock()
	var (
		c       *crestronConsole
		closeFn func()
		err     error
	)
	if a.ctp {
		c, closeFn, err = a.openCTP(ctx)
	} else {
		c, closeFn, err = a.openSSH(ctx)
	}
	if err != nil {
		return err
	}
	defer closeFn()
	return fn(c)
}

// crestronSSHConfig accepts the older key exchanges and host key types
// 3-Series firmware offers, as device TLS does for older codecs.
func crestronSSHConfig(user, pass string, hostKey *string) *ssh.ClientConfig {
	algs := ssh.SupportedAlgorithms()
	old := ssh.InsecureAlgorithms()
	cfg := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(pass),
			ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
				ans := make([]string, len(qs))
				for i := range qs {
					ans[i] = pass
				}
				return ans, nil
			}),
		},
		// Processors carry self-generated host keys and no PKI. The key's
		// fingerprint is recorded as a reading so a change can be seen.
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			sum := sha256.Sum256(key.Marshal())
			*hostKey = "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
			return nil
		},
		HostKeyAlgorithms: append(append([]string{}, algs.HostKeys...), old.HostKeys...),
		Timeout:           10 * time.Second,
	}
	cfg.KeyExchanges = append(append([]string{}, algs.KeyExchanges...), old.KeyExchanges...)
	cfg.Ciphers = append(append([]string{}, algs.Ciphers...), old.Ciphers...)
	cfg.MACs = append(append([]string{}, algs.MACs...), old.MACs...)
	return cfg
}

func (a *CrestronAdapter) openSSH(ctx context.Context) (*crestronConsole, func(), error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", a.address)
	if err != nil {
		return nil, nil, fmt.Errorf("dial %s: %w", a.address, err)
	}
	var hostKey string
	_ = raw.SetDeadline(time.Now().Add(crestronLoginWait))
	sc, chans, reqs, err := ssh.NewClientConn(raw, a.address, crestronSSHConfig(a.Cfg.Username, a.Cfg.Password, &hostKey))
	if err != nil {
		raw.Close()
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, nil, errors.New("login rejected — check username and password")
		}
		return nil, nil, fmt.Errorf("ssh: %w", err)
	}
	_ = raw.SetDeadline(time.Time{})
	client := ssh.NewClient(sc, chans, reqs)
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, nil, fmt.Errorf("ssh session: %w", err)
	}
	stdin, err1 := sess.StdinPipe()
	stdout, err2 := sess.StdoutPipe()
	if err := errors.Join(err1, err2); err != nil {
		client.Close()
		return nil, nil, err
	}
	_ = sess.RequestPty("vt100", 400, 250, ssh.TerminalModes{ssh.ECHO: 0})
	if err := sess.Shell(); err != nil {
		client.Close()
		return nil, nil, fmt.Errorf("ssh shell: %w", err)
	}
	c := newCrestronConsole(ctx, stdin, stdout)
	c.hostKey = hostKey
	closeFn := func() { c.close(); sess.Close(); client.Close() }
	if err := c.awaitPrompt(); err != nil {
		closeFn()
		return nil, nil, err
	}
	return c, closeFn, nil
}

func (a *CrestronAdapter) openCTP(ctx context.Context) (*crestronConsole, func(), error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", a.address)
	if err != nil {
		return nil, nil, fmt.Errorf("dial %s: %w", a.address, err)
	}
	c := newCrestronConsole(ctx, conn, newTelnetFilter(conn))
	closeFn := func() { c.close(); conn.Close() }

	// With authentication on, CTP asks for a login first.
	seen, err := c.readUntil(crestronLoginWait, func(b string) bool {
		return crestronTailPrompt(b) != "" || endsWithAny(b, []string{"username:", "login:", "user name:"})
	})
	if err == nil && crestronTailPrompt(seen) == "" {
		_ = crestronSend(conn, a.Cfg.Username)
		if _, err = c.readUntil(crestronLoginWait, func(b string) bool { return endsWithAny(b, []string{"password:"}) }); err == nil {
			_ = crestronSend(conn, a.Cfg.Password)
			seen, err = c.readUntil(crestronLoginWait, func(b string) bool {
				return crestronTailPrompt(b) != "" || endsWithAny(b, []string{"username:", "login:"})
			})
			if err == nil && crestronTailPrompt(seen) == "" {
				err = errors.New("login rejected — check username and password")
			}
		}
	}
	if err != nil {
		closeFn()
		return nil, nil, fmt.Errorf("ctp login: %w (saw %q)", err, collapse(seen))
	}
	c.prompt = crestronTailPrompt(seen)
	return c, closeFn, nil
}

func newCrestronConsole(ctx context.Context, w io.Writer, r io.Reader) *crestronConsole {
	c := &crestronConsole{w: w, out: make(chan []byte, 64), ctx: ctx, quit: make(chan struct{})}
	go func() {
		defer close(c.out)
		b := make([]byte, 4096)
		for {
			n, err := r.Read(b)
			if n > 0 {
				select {
				case c.out <- append([]byte(nil), b[:n]...):
				case <-c.quit:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return c
}

// awaitPrompt waits for the first prompt, nudging with a newline when a
// banner arrives without one.
func (c *crestronConsole) awaitPrompt() error {
	seen, err := c.readUntil(3*time.Second, func(b string) bool { return crestronTailPrompt(b) != "" })
	if err != nil {
		_ = crestronSend(c.w, "")
		seen, err = c.readUntil(crestronLoginWait, func(b string) bool { return crestronTailPrompt(b) != "" })
	}
	if err != nil {
		return fmt.Errorf("console prompt: %w (saw %q)", err, collapse(seen))
	}
	c.prompt = crestronTailPrompt(seen)
	return nil
}

// run sends one command and returns its output, without the echoed
// command or the prompt after it.
func (c *crestronConsole) run(cmd string) (string, error) {
	if err := crestronSend(c.w, cmd); err != nil {
		return "", fmt.Errorf("write %q: %w", cmd, err)
	}
	seen, err := c.readUntil(crestronCmdTimeout, func(b string) bool { return crestronTailPrompt(b) != "" })
	if err != nil {
		c.broken = true
		return "", fmt.Errorf("%s: %w", cmd, err)
	}
	return crestronStrip(seen, cmd), nil
}

// try runs the first of cmds the firmware knows, and says whether one was.
// A session that fails part-way ends the tries: the reading is left out.
func (c *crestronConsole) try(cmds ...string) (string, bool) {
	for _, cmd := range cmds {
		if c.broken {
			return "", false
		}
		out, err := c.run(cmd)
		if err != nil {
			return "", false
		}
		if !crestronBadCmdRE.MatchString(out) {
			return out, true
		}
	}
	return "", false
}

// runLeaving sends a command that ends the session (reboot): the session
// closing, or no prompt coming back, is the command working.
func (c *crestronConsole) runLeaving(cmd string) (string, error) {
	if err := crestronSend(c.w, cmd); err != nil {
		return "", fmt.Errorf("write %q: %w", cmd, err)
	}
	seen, _ := c.readUntil(3*time.Second, func(b string) bool { return crestronTailPrompt(b) != "" })
	out := crestronStrip(seen, cmd)
	if crestronBadCmdRE.MatchString(out) {
		return out, nil
	}
	if out == "" {
		out = "reboot sent; the processor closed the session"
	}
	return out, nil
}

func (c *crestronConsole) readUntil(d time.Duration, done func(string) bool) (string, error) {
	c.buf.Reset()
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case b, ok := <-c.out:
			if !ok {
				return c.buf.String(), io.EOF
			}
			c.buf.Write(b)
			if done(c.buf.String()) {
				return c.buf.String(), nil
			}
		case <-timer.C:
			return c.buf.String(), errors.New("timed out")
		case <-c.ctx.Done():
			return c.buf.String(), c.ctx.Err()
		}
	}
}

var crestronPromptRE = regexp.MustCompile(`(?:^|\n)\s*([A-Za-z][A-Za-z0-9_\-]*>)\s*$`)

// crestronTailPrompt returns the console prompt ("CP4>") ending b, if it does.
func crestronTailPrompt(b string) string {
	b = strings.ReplaceAll(b, "\r", "")
	if mm := crestronPromptRE.FindStringSubmatch(b); mm != nil {
		return mm[1]
	}
	return ""
}

// crestronStrip removes the echoed command and the trailing prompt.
func crestronStrip(seen, cmd string) string {
	s := strings.ReplaceAll(seen, "\r", "")
	if loc := crestronPromptRE.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	s = strings.TrimLeft(s, "\n ")
	if first, rest, ok := strings.Cut(s, "\n"); ok && strings.EqualFold(strings.TrimSpace(first), cmd) {
		s = rest
	} else if strings.EqualFold(strings.TrimSpace(s), cmd) {
		s = ""
	}
	return strings.TrimSpace(s)
}
