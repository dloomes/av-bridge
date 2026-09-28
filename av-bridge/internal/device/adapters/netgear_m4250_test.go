package adapters

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// Outputs follow the examples in the M4250/M4350 CLI Command Reference
// Manual; identifiers are made up.
var netgearOutputs = map[string]string{
	"show version": `Switch: 1

System Description............................. M4250-26G4F-PoE+ AV Line Managed Switch
Machine Type................................... M4250-26G4F-PoE+
Machine Model.................................. GSM4230P
Serial Number.................................. 6LR0000000001
Burned In MAC Address.......................... 00:00:5E:00:53:01
Software Version............................... 13.0.4.26
Boot Code Version.............................. 13.0.0.5`,

	"show sysinfo": `System Description............................. M4250-26G4F-PoE+ AV Line Managed Switch
System Name.................................... AV-Rack-1
System Location................................ Comms room
System Contact.................................
System Up Time................................. 2 days 3 hrs 24 mins 33 secs
Current SNTP Synchronized Time................. Not Synchronized`,

	"show environment": `Fan Control Mode............................... Quiet
Temp (C)....................................... 23
Temperature traps range: 0 to 90 degrees (Celsius)
Temperature Sensors:
Unit     Sensor  Description       Temp (C)    State              Max_Temp (C)
----     ------  ----------------  ----------  -----------------  --------------
1        1       sensor-1          23          Normal             53
Fans:
Unit Fan Description    Type      Speed         Duty level    State
---- --- -------------- --------- ------------- ------------- --------------
1    1   FAN-1          Fixed     2500          25%           Operational
1    2   FAN-2          Fixed     2500          25%           Operational
Power Modules:
Unit     Power supply   Description        Type          State
----     ------------   ----------------   ----------    --------------
1        1              PS-1               Fixed         Operational`,

	"show poe": `Firmware Version............................... 1.2.0.8
PSE Main Operational Status.................... ON
Total Power Available.......................... 125.0 Watts
Threshold Power................................ 112.5 Watts
Total Power Consumed........................... 12.3 Watts
Usage Threshold................................ 90
Power Management Mode.......................... Dynamic
Traps.......................................... Enable`,

	"show poe port info all": `         High     Max                      Output  Output
Intf    Power   Power     Class   Power   Current Voltage      Status Fault
                 (mW)              (mW)     (mA)   (V) Status
------ ------- -------- -------- -------  ------- -------  ----------------- ----------
0/1      Yes   32000    Unknown  0        0       0        Searching         No Error
0/2      Yes   32000    4        4000     74      54       Delivering Power  No Error
0/3      Yes   32000    Unknown  0        0       0        Searching         No Error
0/4      Yes   32000    Unknown  0        0       0        Searching         No Error
0/5      Yes   32000    3        3000     56      53       Delivering Power  No Error
0/6      Yes   32000    4        3800     72      53       Delivering Power  No Error
0/7      Yes   32000    3        1500     28      53       Delivering Power  No Error
0/8      Yes   32000    Unknown  0        0       0        Searching         No Error`,

	"show port all": `                 Admin     Physical   Physical   Link   Link    LACP   Actor
Intf      Type   Mode      Mode       Status     Status Trap    Mode   Timeout
--------- ------ --------- ---------- ---------- ------ ------- ------ --------
0/1              Enable    Auto       100 Full   Up     Enable  Enable long
0/2              Enable    Auto       1000 Full  Up     Enable  Enable long
0/3              Enable    Auto                  Down   Enable  Enable long
0/4              Enable    Auto       100 Full   Up     Enable  Enable long
0/5              Enable    Auto       100 Full   Up     Enable  Enable long
0/6              Disable   Auto                  Down   Enable  Enable long
0/7              Enable    Auto       100 Full   Up     Enable  Enable long
0/8              Enable    10G Full   10G Full   Up     Enable  Enable long
1/1              Enable                          Down   Disable N/A    N/A
1/2              Enable                          Down   Disable N/A    N/A`,
}

// fakeNetgear is a Telnet CLI that behaves like an M4250: login, User EXEC
// ">" then enable to "#", paged output, config modes and reload questions.
type fakeNetgear struct {
	ln      net.Listener
	outputs map[string]string
	pageAt  int // lines per page; 0 disables paging

	mu       sync.Mutex
	lines    []string // commands received after login
	answers  []string // (y/n) keypresses received
	sessions int
}

const fakeNetgearHost = "(M4250-26G4F-PoE+)"

func newFakeNetgear(t *testing.T) *fakeNetgear {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeNetgear{ln: ln, outputs: map[string]string{}, pageAt: 6}
	for k, v := range netgearOutputs {
		f.outputs[k] = v
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeNetgear) adapter(password string) *NetgearM4250Adapter {
	return NewNetgearM4250Adapter(config.DeviceConfig{
		ID: "sw1", Protocol: "netgear_m4250", Type: "control",
		Address: f.ln.Addr().String(), Username: "admin", Password: password,
	})
}

func (f *fakeNetgear) serve(c net.Conn) {
	defer c.Close()
	f.mu.Lock()
	f.sessions++
	f.mu.Unlock()
	r := bufio.NewReader(c)
	w := func(s string) { _, _ = c.Write([]byte(s)) }
	readLine := func() (string, bool) {
		s, err := r.ReadString('\n')
		return strings.TrimRight(s, "\r\n"), err == nil
	}

	w("\r\nUser:")
	if _, ok := readLine(); !ok {
		return
	}
	w("Password:")
	pw, ok := readLine()
	if !ok {
		return
	}
	if pw != "secret" {
		w("\r\nIncorrect user name or password\r\nUser:")
		return
	}
	mode := ""
	priv := false
	prompt := func() string {
		if !priv {
			return fakeNetgearHost + " >"
		}
		if mode != "" {
			return fakeNetgearHost + " (" + mode + ")#"
		}
		return fakeNetgearHost + " #"
	}
	w("\r\n" + prompt())
	for {
		line, ok := readLine()
		if !ok {
			return
		}
		f.mu.Lock()
		f.lines = append(f.lines, line)
		f.mu.Unlock()
		w(line + "\r\n") // echo
		switch {
		case line == "enable":
			w("Password:")
			if _, ok := readLine(); !ok {
				return
			}
			priv = true
			w("\r\n")
		case line == "configure":
			mode = "Config"
		case strings.HasPrefix(line, "interface "):
			mode = "Interface " + strings.TrimPrefix(line, "interface ")
		case line == "exit":
			if strings.HasPrefix(mode, "Interface") {
				mode = "Config"
			} else {
				mode = ""
			}
		case line == "poe reset", line == "poe", line == "no poe", line == "shutdown", line == "no shutdown":
		case line == "reload":
			for _, q := range []string{
				"The system has unsaved changes.\r\nWould you like to save them now? (y/n) ",
				"\r\nAre you sure you would like to reset the system? (y/n) ",
			} {
				w(q)
				b, err := r.ReadByte()
				if err != nil {
					return
				}
				f.mu.Lock()
				f.answers = append(f.answers, string(b))
				f.mu.Unlock()
			}
			w("\r\nSystem resetting...\r\n")
			return
		default:
			out, known := f.outputs[line]
			if !known {
				w("                  ^\r\n% Invalid input detected at '^' marker.\r\n\r\n" + prompt())
				continue
			}
			lines := strings.Split(out, "\n")
			for i, l := range lines {
				if f.pageAt > 0 && i > 0 && i%f.pageAt == 0 {
					w("--More-- or (q)uit")
					if b, err := r.ReadByte(); err != nil || b != ' ' {
						return
					}
					w("\r                  \r")
				}
				w(l + "\r\n")
			}
		}
		w("\r\n" + prompt())
	}
}

func (f *fakeNetgear) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.lines...)
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestNetgearPoll(t *testing.T) {
	f := newFakeNetgear(t)
	a := f.adapter("secret")
	if err := a.Connect(testCtx(t)); err != nil {
		t.Fatalf("connect: %v", err)
	}
	tel, err := a.Poll(testCtx(t))
	if err != nil || tel.Status != device.StatusOnline {
		t.Fatalf("poll: status %v err %v (%s)", tel.Status, err, tel.Error)
	}
	m := tel.Metrics
	want := map[string]any{
		"model":                "GSM4230P",
		"serial_number":        "6LR0000000001",
		"mac_address":          "00:00:5E:00:53:01",
		"software_version":     "13.0.4.26",
		"boot_version":         "13.0.0.5",
		"system_name":          "AV-Rack-1",
		"system_location":      "Comms room",
		"uptime_s":             2*86400 + 3*3600 + 24*60 + 33,
		"temperature_c":        23.0,
		"fan_count":            2,
		"psu_count":            1,
		"poe_status":           "on",
		"poe_budget_w":         125.0,
		"poe_consumed_w":       12.3,
		"poe_threshold_w":      112.5,
		"poe_ports_delivering": 4,
		"port_2_poe_status":    "delivering power",
		"port_2_poe_power_w":   4.0,
		"port_2_poe_class":     "4",
		"port_1_poe_status":    "searching",
		"port_1_link":          "up",
		"port_1_speed":         "100 Full",
		"port_2_speed":         "1000 Full",
		"port_8_speed":         "10G Full",
		"port_3_link":          "down",
		"port_6_admin":         "disabled",
		"port_1_admin":         "enabled",
		"ports_total":          8, // 1/x LAGs excluded; spans a page break
		"ports_up":             6,
		"fault_count":          0,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %#v, want %#v", k, m[k], v)
		}
	}
	for _, k := range []string{"port_1_poe_class", "port_3_speed", "faults", "port_1_1_link"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s should be absent, got %#v", k, m[k])
		}
	}

	caps := a.Capabilities()
	if !containsStr(caps.Metrics, "port_8_link") || !containsStr(caps.Commands, "poe_reset") {
		t.Errorf("capabilities missing per-port metric or poe_reset: %v %v", caps.Metrics, caps.Commands)
	}
	if got := f.received(); !containsStr(got, "enable") {
		t.Errorf("expected enable from User EXEC, got %v", got)
	}
}

func TestNetgearFaults(t *testing.T) {
	f := newFakeNetgear(t)
	f.outputs["show environment"] = strings.Replace(netgearOutputs["show environment"],
		"FAN-2          Fixed     0             0%            Operational", "", 1)
	f.outputs["show environment"] = strings.Replace(f.outputs["show environment"],
		"1    2   FAN-2          Fixed     2500          25%           Operational",
		"1    2   FAN-2          Fixed     0             0%            Failed", 1)
	f.outputs["show poe port info all"] = strings.Replace(netgearOutputs["show poe port info all"],
		"0/7      Yes   32000    3        1500     28      53       Delivering Power  No Error",
		"0/7      Yes   32000    3        0        0       0        Fault             Overload", 1)

	tel, _ := f.adapter("secret").Poll(testCtx(t))
	if tel.Metrics["fault_count"] != 2 {
		t.Fatalf("fault_count = %v, faults %v", tel.Metrics["fault_count"], tel.Metrics["faults"])
	}
	got := []string{}
	for _, x := range tel.Metrics["faults"].([]any) {
		got = append(got, x.(map[string]any)["description"].(string))
	}
	if got[0] != "FAN-2: Failed" || got[1] != "Port 0/7: Overload" {
		t.Errorf("faults = %v", got)
	}
	if tel.Metrics["poe_ports_delivering"] != 3 {
		t.Errorf("poe_ports_delivering = %v", tel.Metrics["poe_ports_delivering"])
	}
}

func TestNetgearNonPoEModel(t *testing.T) {
	f := newFakeNetgear(t)
	delete(f.outputs, "show poe")
	delete(f.outputs, "show poe port info all")
	tel, _ := f.adapter("secret").Poll(testCtx(t))
	if tel.Status != device.StatusOnline {
		t.Fatalf("status %v: %s", tel.Status, tel.Error)
	}
	for _, k := range []string{"poe_status", "poe_budget_w", "poe_ports_delivering", "port_2_poe_status"} {
		if _, ok := tel.Metrics[k]; ok {
			t.Errorf("%s should be absent on a non-PoE model", k)
		}
	}
	if tel.Metrics["ports_total"] != 8 {
		t.Errorf("ports_total = %v", tel.Metrics["ports_total"])
	}
}

func TestNetgearLoginRejected(t *testing.T) {
	f := newFakeNetgear(t)
	err := f.adapter("wrong").Connect(testCtx(t))
	if err == nil || !strings.Contains(err.Error(), "login rejected") {
		t.Fatalf("err = %v", err)
	}
	tel, _ := f.adapter("wrong").Poll(testCtx(t))
	if tel.Status != device.StatusOffline || tel.Error == "" {
		t.Errorf("poll after bad login: %v %q", tel.Status, tel.Error)
	}
}

func TestNetgearPortCommands(t *testing.T) {
	cases := []struct {
		name string
		port any
		want []string
	}{
		{"poe_reset", "5", []string{"configure", "interface 0/5", "poe reset", "exit", "exit"}},
		{"poe_off", float64(12), []string{"configure", "interface 0/12", "no poe", "exit", "exit"}},
		{"poe_on", "0/3", []string{"configure", "interface 0/3", "poe", "exit", "exit"}},
		{"port_disable", 7, []string{"configure", "interface 0/7", "shutdown", "exit", "exit"}},
		{"port_enable", "7", []string{"configure", "interface 0/7", "no shutdown", "exit", "exit"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeNetgear(t)
			resp, err := f.adapter("secret").SendCommand(testCtx(t),
				device.CommandRequest{Name: c.name, Args: map[string]any{"port": c.port}})
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if resp.Parsed["success"] != true {
				t.Errorf("success = %v", resp.Parsed["success"])
			}
			got := f.received()
			if len(got) > 0 && got[0] == "enable" {
				got = got[1:] // the enable password line is read by the fake, not logged
			}
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("lines = %q, want %q", got, c.want)
			}
		})
	}
}

func TestNetgearCommandErrors(t *testing.T) {
	f := newFakeNetgear(t)
	a := f.adapter("secret")
	for _, req := range []device.CommandRequest{
		{Name: "poe_reset"}, // no port
		{Name: "poe_reset", Args: map[string]any{"port": "x"}}, // not a port
		{Name: "poe_reset", Args: map[string]any{"port": 0}},
		{Name: "factory_reset"},
	} {
		if _, err := a.SendCommand(testCtx(t), req); err == nil {
			t.Errorf("%s %v: expected an error", req.Name, req.Args)
		}
	}
	if f.sessions != 0 {
		t.Errorf("invalid requests should not open a session (%d opened)", f.sessions)
	}
	_, err := a.SendCommand(testCtx(t), device.CommandRequest{Name: "show bogus"})
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("show bogus: err = %v", err)
	}
}

func TestNetgearShowPassthrough(t *testing.T) {
	f := newFakeNetgear(t)
	resp, err := f.adapter("secret").SendCommand(testCtx(t), device.CommandRequest{Name: "show sysinfo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Raw, "AV-Rack-1") || strings.Contains(resp.Raw, "#") {
		t.Errorf("raw = %q", resp.Raw)
	}
}

func TestNetgearReboot(t *testing.T) {
	f := newFakeNetgear(t)
	if _, err := f.adapter("secret").SendCommand(testCtx(t), device.CommandRequest{Name: "reboot"}); err != nil {
		t.Fatalf("reboot: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Join(f.answers, ",") != "n,y" {
		t.Errorf("answers = %v, want don't save, then confirm", f.answers)
	}
}

func TestNetgearConfigCommandOverride(t *testing.T) {
	f := newFakeNetgear(t)
	a := NewNetgearM4250Adapter(config.DeviceConfig{
		ID: "sw1", Address: f.ln.Addr().String(), Username: "admin", Password: "secret",
		Commands: map[string]string{"poe_cycle_panel": "configure; interface 0/{port}; poe reset; exit; exit"},
	})
	if _, err := a.SendCommand(testCtx(t), device.CommandRequest{Name: "poe_cycle_panel", Args: map[string]any{"port": 9}}); err != nil {
		t.Fatal(err)
	}
	if got := f.received(); !containsStr(got, "interface 0/9") {
		t.Errorf("lines = %v", got)
	}
}

func TestNetgearParsers(t *testing.T) {
	if n, ok := netgearUptime("0 days 2 hrs 9 mins 16 secs"); !ok || n != 2*3600+9*60+16 {
		t.Errorf("uptime = %d %v", n, ok)
	}
	if _, ok := netgearUptime(""); ok {
		t.Error("empty uptime should not parse")
	}
	for in, want := range map[string]string{"0/5": "5", "1/0/5": "1_0_5", "0/12": "12"} {
		if got := netgearPortKey(in); got != want {
			t.Errorf("port key %s = %s, want %s", in, got, want)
		}
	}
	a := NewNetgearM4250Adapter(config.DeviceConfig{Address: "10.0.0.1", Tags: map[string]string{"port_prefix": "1/0"}})
	if a.address != "10.0.0.1:23" {
		t.Errorf("address = %s", a.address)
	}
	if p, err := a.portArg(device.CommandRequest{Args: map[string]any{"port": "4"}}); err != nil || p != "1/0/4" {
		t.Errorf("port_prefix: %s %v", p, err)
	}
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
