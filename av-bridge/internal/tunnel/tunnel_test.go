package tunnel

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"github.com/dloomes/av-bridge/internal/config"
)

type fakeDevices map[string]config.DeviceConfig

func (f fakeDevices) DeviceConfig(id string) (config.DeviceConfig, bool) {
	d, ok := f[id]
	return d, ok
}

func TestTargetAllowList(t *testing.T) {
	c := &Client{devices: fakeDevices{
		"plain":  {Address: "10.0.0.5"},
		"ctl":    {Address: "10.0.0.6:23"},
		"url":    {Address: "https://10.0.0.7:4443/api"},
		"tagged": {Address: "10.0.0.8", Tags: map[string]string{"web_port": "8000"}},
		"none":   {Address: ""},
	}}
	cases := []struct {
		dev  string
		port int
		want string // "" = refused
	}{
		{"plain", 80, "10.0.0.5:80"},
		{"plain", 443, "10.0.0.5:443"},
		{"plain", 8443, "10.0.0.5:8443"},
		{"plain", 22, ""},
		{"plain", 3389, ""},
		{"ctl", 23, "10.0.0.6:23"}, // its own port
		{"ctl", 80, "10.0.0.6:80"},
		{"url", 4443, "10.0.0.7:4443"},
		{"tagged", 8000, "10.0.0.8:8000"},
		{"tagged", 8001, ""},
		{"none", 80, ""},
		{"unknown", 80, ""},
	}
	for _, tc := range cases {
		got, err := c.target(request{Device: tc.dev, Port: tc.port})
		if tc.want == "" {
			if err == nil {
				t.Errorf("%s:%d allowed (%s), want refused", tc.dev, tc.port, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s:%d = %q, %v; want %q", tc.dev, tc.port, got, err, tc.want)
		}
	}
}

// A fake cloud accepts the tunnel and fetches a device page through it.
func TestTunnelRelaysToDevice(t *testing.T) {
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello from "+r.URL.Path)
	}))
	defer device.Close()
	devAddr := strings.TrimPrefix(device.URL, "http://")
	_, portStr, _ := net.SplitHostPort(devAddr)
	port, _ := strconv.Atoi(portStr)

	const secret = "s3cret"
	sessions := make(chan *yamux.Session, 1)
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts, _ := strconv.ParseInt(r.Header.Get("X-Timestamp"), 10, 64)
		if r.Header.Get("X-Signature") != Sign(secret, "col-1", ts) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s, err := yamux.Client(NewWSConn(ws), nil)
		if err != nil {
			return
		}
		sessions <- s
		<-s.CloseChan()
	}))
	defer cloud.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(cloud.URL, "col-1", secret, false, fakeDevices{"web": {Address: devAddr}})
	go c.Run(ctx)

	var sess *yamux.Session
	select {
	case sess = <-sessions:
	case <-time.After(5 * time.Second):
		t.Fatal("collector didn't connect")
	}

	open := func(dev string, port int) (net.Conn, *bufio.Reader, string) {
		t.Helper()
		st, err := sess.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(request{Device: dev, Port: port})
		st.Write(append(b, '\n'))
		br := bufio.NewReader(st)
		reply, _ := br.ReadString('\n')
		return st, br, strings.TrimSpace(reply)
	}

	// Refused: a port that isn't a web port, and a device it doesn't manage.
	if st, _, reply := open("web", 22); !strings.HasPrefix(reply, "error:") {
		t.Errorf("port 22 reply = %q", reply)
	} else {
		st.Close()
	}
	if st, _, reply := open("other", 80); !strings.HasPrefix(reply, "error:") {
		t.Errorf("unknown device reply = %q", reply)
	} else {
		st.Close()
	}

	st, br, reply := open("web", port)
	if reply != "ok" {
		t.Fatalf("reply = %q", reply)
	}
	defer st.Close()
	io.WriteString(st, "GET /status HTTP/1.1\r\nHost: dev\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from /status" {
		t.Fatalf("body = %q", body)
	}
}
