package adapters

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// Console output in the style 3- and 4-Series processors print. Not
// captured from a unit: replace with real captures when there is one.
var crestronOutputs = map[string]string{
	"ver":      "CP4 Cntrl Eng [v2.8001.00066 (Mar 14 2024), #00F0C73C] @E-00107fe3c2a1",
	"hostname": "Host Name: CP4-COURT3",
	"uptime":   "The system has been running for 12 days, 3 hours, 46 minutes, 15 seconds.",
	"cpuload":  "CPU utilization: 4%",
	"ramfree":  "Total RAM: 1895 MB\r\nFree RAM: 1264 MB",
	"progcomments:1": "Program Boot Directory: \\simpl\\app01\r\nSource File: Court3_v12.smw\r\n" +
		"System Name: Court 3 AV\r\nProgrammer: A Programmer\r\nCompiled On: 3/14/2024 10:15 AM",
	"iptable -p:all -t": "IP Table:\r\n" +
		"CIP_ID |Type    |Status    |DevID   |Port   |IP Address/SiteName   |Model Name   |Description         |RoomId |Prog\r\n" +
		"     3 |Gway    |ONLINE    |        |00000  |10.20.1.31            |TSW-1070     |Judge Panel         |       |01\r\n" +
		"    A1 |Peer    |OFFLINE   |        |41794  |10.20.1.40            |DM-NVX-360   |Witness Display Rx  |       |01\r\n" +
		"    05 |Gway    |ONLINE    |        |41794  |10.20.1.50            |             |                    |       |02",
	"progreset -p:1": "Program(s) Stopped...",
}

func TestCrestronParsers(t *testing.T) {
	v := crestronParseVer(crestronOutputs["ver"])
	for k, want := range map[string]string{"model": "CP4", "firmware": "2.8001.00066", "firmware_date": "Mar 14 2024",
		"tsid": "00F0C73C", "mac": "00:10:7f:e3:c2:a1"} {
		if v[k] != want {
			t.Errorf("ver %s = %q, want %q", k, v[k], want)
		}
	}
	if v := crestronParseVer("PRO3 Cntrl Eng [v1.8001.0187 (Nov 18 2021), #7A3A0D3E] @E-00107f9b1c22"); v["model"] != "PRO3" || v["firmware"] != "1.8001.0187" {
		t.Errorf("3-Series ver: %v", v)
	}
	if s, ok := crestronUptime(crestronOutputs["uptime"]); !ok || s != 12*86400+3*3600+46*60+15 {
		t.Errorf("uptime = %d %v", s, ok)
	}
	if r := crestronRAM(crestronOutputs["ramfree"]); r["ram_free_mb"] != 1264.0 || r["ram_used_pct"] != 33.3 {
		t.Errorf("ramfree = %v", r)
	}
	if r := crestronRAM("Free RAM: 52428800 bytes"); r["ram_free_mb"] != 50.0 {
		t.Errorf("ramfree in bytes = %v", r)
	}
	p := crestronProgram(crestronOutputs["progcomments:1"])
	if p["program_file"] != "Court3_v12.smw" || p["program_name"] != "Court 3 AV" || p["program_compiled"] != "3/14/2024 10:15 AM" {
		t.Errorf("progcomments = %v", p)
	}
	for _, v := range p {
		if strings.Contains(v.(string), "A Programmer") {
			t.Error("the programmer's name was read")
		}
	}
	rows, ok := crestronIPTable(crestronOutputs["iptable -p:all -t"])
	if !ok || len(rows) != 3 {
		t.Fatalf("iptable: %v %v", rows, ok)
	}
	if r := rows[1]; r.ID != "A1" || r.Status != "offline" || r.Model != "DM-NVX-360" || r.Description != "Witness Display Rx" || r.Address != "10.20.1.40" {
		t.Errorf("row: %+v", r)
	}
	if rows[0].ID != "03" || rows[2].Program != "02" {
		t.Errorf("rows: %+v", rows)
	}
	if crestronTailPrompt("blah\r\nCP4>") != "CP4>" || crestronTailPrompt("Password:") != "" {
		t.Error("prompt detection")
	}
}

// fakeCrestron is a console over SSH or CTP that answers from outputs.
type fakeCrestron struct {
	ln       net.Listener
	mu       sync.Mutex
	received []string
}

func (f *fakeCrestron) got(cmd string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.received {
		if c == cmd {
			return true
		}
	}
	return false
}

// console answers lines on rw until it closes; reboot ends the session.
func (f *fakeCrestron) console(rw interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
}, outputs map[string]string) {
	rw.Write([]byte("Crestron console\r\nCP4>"))
	r := bufio.NewReader(rw)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		f.mu.Lock()
		f.received = append(f.received, cmd)
		f.mu.Unlock()
		if cmd == "reboot" {
			return
		}
		out, ok := outputs[cmd]
		if !ok && cmd != "" {
			out = "Bad or Incomplete Command"
		}
		rw.Write([]byte(cmd + "\r\n" + out + "\r\n\r\nCP4>"))
	}
}

func newFakeCrestronSSH(t *testing.T, user, pass string, outputs map[string]string) *fakeCrestron {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if c.User() == user && string(p) == pass {
			return nil, nil
		}
		return nil, net.ErrClosed
	}}
	cfg.AddHostKey(signer)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	f := &fakeCrestron{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					c.Close()
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, creqs, _ := nc.Accept()
					go func() {
						for r := range creqs {
							r.Reply(r.Type == "shell" || r.Type == "pty-req", nil)
							if r.Type == "shell" {
								go func() { f.console(ch, outputs); ch.Close(); c.Close() }()
							}
						}
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func crestronDev(addr string, tags map[string]string) *CrestronAdapter {
	return NewCrestronAdapter(config.DeviceConfig{ID: "cp4-1", Name: "Court 3 processor", Type: "control",
		Protocol: "crestron", Address: addr, Username: "admin", Password: "secret", Tags: tags})
}

func TestCrestronPollOverSSH(t *testing.T) {
	f := newFakeCrestronSSH(t, "admin", "secret", crestronOutputs)
	a := crestronDev(f.ln.Addr().String(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	tel, _ := a.Poll(ctx)
	if tel.Status != device.StatusOnline {
		t.Fatalf("status %s: %s", tel.Status, tel.Error)
	}
	for k, want := range map[string]any{
		"model": "CP4", "hostname": "CP4-COURT3", "cpu_load_pct": 4.0, "uptime_s": 1050375,
		"program_name": "Court 3 AV", "ip_devices": 3, "ip_devices_online": 2, "ip_devices_offline": 1,
		"offline_ip_devices": "A1 Witness Display Rx (10.20.1.40)",
		"ipid_03_status":     "online", "ipid_a1_status": "offline", "p2_ipid_05_status": "online",
	} {
		if tel.Metrics[k] != want {
			t.Errorf("%s = %#v, want %#v", k, tel.Metrics[k], want)
		}
	}
	if k, _ := tel.Metrics["ssh_host_key"].(string); !strings.HasPrefix(k, "SHA256:") {
		t.Errorf("ssh_host_key = %q", k)
	}
	for k := range tel.Metrics {
		if strings.HasSuffix(k, "_reply") {
			t.Errorf("unexpected %s: %v", k, tel.Metrics[k])
		}
	}
	caps := a.Capabilities()
	if !strings.Contains(strings.Join(caps.Metrics, ","), "ipid_a1_status") {
		t.Errorf("capabilities miss the IP IDs: %v", caps.Metrics)
	}

	// restart_program goes to the slot; reboot's dropped session is success.
	if _, err := a.SendCommand(ctx, device.CommandRequest{Name: "restart_program"}); err != nil || !f.got("progreset -p:1") {
		t.Errorf("restart_program: %v", err)
	}
	if _, err := a.SendCommand(ctx, device.CommandRequest{Name: "reboot"}); err != nil || !f.got("reboot") {
		t.Errorf("reboot: %v", err)
	}
}

func TestCrestronOlderFirmware(t *testing.T) {
	// A 3-Series: no slots, plain iptable, a ramfree it can't read.
	out := map[string]string{
		"ver":          "PRO3 Cntrl Eng [v1.8001.0187 (Nov 18 2021), #7A3A0D3E] @E-00107f9b1c22",
		"progcomments": "Source File: Lobby.smw\r\nSystem Name: Lobby",
		"iptable":      "CIP_ID |Type |Status |DevID |Port |IP Address/SiteName\r\n  03 |Gway |ONLINE | |41794 |10.0.0.9",
		"ramfree":      "something new",
	}
	f := newFakeCrestronSSH(t, "admin", "secret", out)
	a := crestronDev(f.ln.Addr().String(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tel, _ := a.Poll(ctx)
	if tel.Status != device.StatusOnline || tel.Metrics["model"] != "PRO3" || tel.Metrics["program_name"] != "Lobby" || tel.Metrics["ip_devices_online"] != 1 {
		t.Fatalf("status %s, metrics %v", tel.Status, tel.Metrics)
	}
	if tel.Metrics["ramfree_reply"] != "something new" {
		t.Errorf("ramfree_reply = %v", tel.Metrics["ramfree_reply"])
	}
	if _, ok := tel.Metrics["hostname"]; ok {
		t.Error("hostname set from a command the firmware doesn't know")
	}
}

func TestCrestronBadLogin(t *testing.T) {
	f := newFakeCrestronSSH(t, "admin", "other", crestronOutputs)
	a := crestronDev(f.ln.Addr().String(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tel, _ := a.Poll(ctx)
	if tel.Status != device.StatusOffline || !strings.Contains(tel.Error, "login rejected") {
		t.Fatalf("status %s, error %q", tel.Status, tel.Error)
	}
}

func TestCrestronOverCTP(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { ln.Close() })
	f := &fakeCrestron{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				c.Write([]byte("Username: "))
				u, _ := r.ReadString('\n')
				c.Write([]byte("Password: "))
				p, _ := r.ReadString('\n')
				if strings.TrimSpace(u) != "admin" || strings.TrimSpace(p) != "secret" {
					c.Write([]byte("Login failed\r\nUsername: "))
					return
				}
				f.console(c, crestronOutputs)
			}()
		}
	}()
	a := crestronDev(ln.Addr().String(), map[string]string{"console": "ctp"})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tel, _ := a.Poll(ctx)
	if tel.Status != device.StatusOnline || tel.Metrics["model"] != "CP4" || tel.Metrics["ip_devices"] != 3 {
		t.Fatalf("status %s (%s), metrics %v", tel.Status, tel.Error, tel.Metrics)
	}
	if _, ok := tel.Metrics["ssh_host_key"]; ok {
		t.Error("ssh_host_key over CTP")
	}
}
