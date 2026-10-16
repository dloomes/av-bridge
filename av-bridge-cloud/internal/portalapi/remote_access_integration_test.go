package portalapi

// End-to-end test of remote access: a fake collector opens its tunnel, an
// admin starts a session, a browser opens the launch link and reaches the
// device's web page through the cloud and the tunnel. Runs only when
// TEST_DATABASE_URL points at a disposable Postgres 16 (see
// device_move_integration_test.go for the docker line).

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/bridgeauth"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/remote"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
)

func TestRemoteAccessEndToEnd(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping remote access test")
	}
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, dsn, quiet); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	su, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer su.Close(ctx)
	as := func(user, pass string) string {
		u, _ := url.Parse(dsn)
		u.User = url.UserPassword(user, pass)
		return u.String()
	}
	st, err := db.New(ctx, as("app_admin", "app_admin_dev"), as("app_tenant", "app_tenant_dev"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	q := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := su.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}

	// The device: a web page, a redirect to its own address, a cookie
	// scoped to its own domain.
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			if c, err := r.Cookie("marcus_rs"); err == nil {
				t.Errorf("session cookie reached the device: %v", c)
			}
			http.SetCookie(w, &http.Cookie{Name: "devsess", Value: "1", Domain: "127.0.0.1", Path: "/"})
			w.Header().Set("Strict-Transport-Security", "max-age=63072000")
			fmt.Fprintf(w, "device home host=%s", r.Host)
		case "/redir":
			http.Redirect(w, r, "http://"+r.Host+"/after", http.StatusFound)
		case "/login":
			io.WriteString(w, "login page")
		case "/after":
			if _, err := r.Cookie("devsess"); err != nil {
				t.Errorf("device cookie didn't come back: %v", err)
			}
			io.WriteString(w, "after")
		}
	}))
	defer device.Close()
	devHost := strings.TrimPrefix(device.URL, "http://")
	_, devPortStr, _ := net.SplitHostPort(devHost)
	devPort, _ := strconv.Atoi(devPortStr)

	cipher, _ := secrets.NewAESGCMFromHexKey(strings.Repeat("ab", 32))
	const secret = "tunnel-test-secret"
	enc, _ := cipher.Encrypt([]byte(secret))
	cust := q(`INSERT INTO customers (name) VALUES ('remote-access') RETURNING id::text`)
	bridgeID := "ra-" + cust
	col := q(`INSERT INTO collectors (customer_id, bridge_collector_id, name, hmac_secret_enc, last_seen_at)
	          VALUES ($1, $2, 'ra', $3, now()) RETURNING id::text`, cust, bridgeID, enc)
	dev := q(`INSERT INTO devices (customer_id, collector_id, reported_id, name, protocol, address)
	          VALUES ($1, $2, 'web-1', 'Web 1', 'ping', $3) RETURNING id::text`, cust, col, devHost)

	// The cloud: tunnel endpoint and session addresses.
	const domain = "access.test"
	tunnels := remote.NewTunnels(bridgeauth.New(st, cipher, quiet), quiet)
	svc := remote.NewService(st.AdminPool(), tunnels, domain, quiet)
	cloud := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc.IsAccessHost(r.Host) {
			svc.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/bridge/tunnel" {
			tunnels.Handle(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	defer cloud.Close()
	cloudAddr := strings.TrimPrefix(cloud.URL, "https://")

	all := map[string]struct{}{}
	for p := range portalauth.KnownPermissions {
		all[p] = struct{}{}
	}
	admin := portalauth.Principal{UserID: "00000000-0000-0000-0000-0000000000c1", Email: "admin@example.test", CustomerID: cust, Permissions: all}
	h := New(st, cipher, nil, nil, nil, quiet).SetRemoteAccess(svc)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /devices/{id}/remote-sessions", h.StartRemoteSession)
	mux.HandleFunc("GET /remote-sessions", h.ListRemoteSessions)
	mux.HandleFunc("DELETE /remote-sessions/{id}", h.EndRemoteSession)
	call := func(method, path string, body any) (int, []byte) {
		t.Helper()
		var rdr io.Reader = http.NoBody
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rdr)
		req = req.WithContext(portalauth.ContextWithPrincipal(req.Context(), admin))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}

	// No tunnel yet: a clear refusal.
	if code, body := call("POST", "/devices/"+dev+"/remote-sessions", nil); code != http.StatusConflict {
		t.Fatalf("start without tunnel = %d %s, want 409", code, body)
	}

	// The fake collector opens its tunnel and relays streams to the device.
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "tunnel:%s:%d", bridgeID, ts)
	hdr := http.Header{}
	hdr.Set("X-Collector-ID", bridgeID)
	hdr.Set("X-Timestamp", strconv.FormatInt(ts, 10))
	hdr.Set("X-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}

	// A wrong signature is refused.
	bad := hdr.Clone()
	bad.Set("X-Signature", "sha256=00")
	if _, resp, err := dialer.Dial("wss://"+cloudAddr+"/bridge/tunnel", bad); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad signature: err=%v resp=%v, want 401", err, resp)
	}

	ws, _, err := dialer.Dial("wss://"+cloudAddr+"/bridge/tunnel", hdr)
	if err != nil {
		t.Fatalf("tunnel dial: %v", err)
	}
	ysess, err := yamux.Server(remote.NewWSConn(ws), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ysess.Close()
	go func() {
		for {
			stream, err := ysess.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				line, _ := br.ReadString('\n')
				var req struct {
					Device string `json:"device"`
					Port   int    `json:"port"`
				}
				_ = json.Unmarshal([]byte(line), &req)
				if req.Device != "web-1" || req.Port != devPort {
					fmt.Fprintf(c, "error: unexpected %s\n", strings.TrimSpace(line))
					return
				}
				d, err := net.Dial("tcp", devHost)
				if err != nil {
					fmt.Fprintf(c, "error: %v\n", err)
					return
				}
				defer d.Close()
				io.WriteString(c, "ok\n")
				go io.Copy(d, br)
				io.Copy(c, d)
			}(stream)
		}
	}()
	waitFor(t, func() bool { return tunnels.Connected(col) })

	// A port that isn't a web port is refused.
	if code, _ := call("POST", "/devices/"+dev+"/remote-sessions", map[string]any{"port": 22}); code != http.StatusBadRequest {
		t.Fatalf("port 22 = %d, want 400", code)
	}

	// A bare host:port address is usually a control port, so the default
	// is HTTP on 80; the device's own port can be asked for.
	code, body := call("POST", "/devices/"+dev+"/remote-sessions", map[string]any{"port": devPort})
	if code != http.StatusCreated {
		t.Fatalf("start = %d %s", code, body)
	}
	var started struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		Scheme string `json:"scheme"`
		Port   int    `json:"port"`
	}
	_ = json.Unmarshal(body, &started)
	if started.Scheme != "http" || started.Port != devPort || !strings.Contains(started.URL, "."+domain+"/__marcus/launch?code=") {
		t.Fatalf("started = %+v", started)
	}

	// A browser: every session address resolves to the test cloud.
	browser := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{
			Jar: jar,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
				DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, cloudAddr)
				},
			},
		}
	}
	get := func(c *http.Client, u string) (*http.Response, string) {
		t.Helper()
		resp, err := c.Get(u)
		if err != nil {
			t.Fatalf("GET %s: %v", u, err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	b := browser()
	resp, page := get(b, started.URL)
	if resp.StatusCode != 200 || page != "device home host="+devHost {
		t.Fatalf("launch → %d %q", resp.StatusCode, page)
	}
	if resp.Header.Get("Strict-Transport-Security") != "" {
		t.Error("device HSTS passed through")
	}
	origin := strings.SplitN(started.URL, "/__marcus", 2)[0]
	// The device's redirect to its own address stays in the session, and
	// its cookie (Domain stripped) comes back to it.
	if resp, page = get(b, origin+"/redir"); resp.StatusCode != 200 || page != "after" {
		t.Fatalf("redirect → %d %q (url %s)", resp.StatusCode, page, resp.Request.URL)
	}

	// The launch link works once; another browser can't use the session.
	if resp, _ = get(browser(), started.URL); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("second launch = %d, want 403", resp.StatusCode)
	}
	if resp, _ = get(browser(), origin+"/"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no cookie = %d, want 403", resp.StatusCode)
	}

	code, body = call("GET", "/remote-sessions?device_id="+dev, nil)
	if code != 200 || !strings.Contains(string(body), started.ID) {
		t.Fatalf("list = %d %s", code, body)
	}

	// Ending it stops the address at once, and both ends are audited.
	if code, body = call("DELETE", "/remote-sessions/"+started.ID, nil); code != http.StatusNoContent {
		t.Fatalf("end = %d %s", code, body)
	}
	if resp, _ = get(b, origin+"/"); resp.StatusCode != http.StatusGone {
		t.Fatalf("after end = %d, want 410", resp.StatusCode)
	}
	if n := q(`SELECT count(*)::text FROM audit_log WHERE target_id = $1 AND action IN ('remote.session.start', 'remote.session.end')`, dev); n != "2" {
		t.Fatalf("audit rows = %s, want 2", n)
	}
	if out := q(`SELECT bytes_in::text FROM remote_session WHERE id = $1`, started.ID); out == "0" {
		t.Error("no traffic recorded")
	}

	// A session can open on a page other than "/", and a path that would
	// leave the session's address is refused.
	if code, _ := call("POST", "/devices/"+dev+"/remote-sessions", map[string]any{"port": devPort, "path": "//evil.example/"}); code != http.StatusBadRequest {
		t.Fatalf("path //evil.example/ = %d, want 400", code)
	}
	code, body = call("POST", "/devices/"+dev+"/remote-sessions", map[string]any{"port": devPort, "path": "login"})
	if code != http.StatusCreated {
		t.Fatalf("start with path = %d %s", code, body)
	}
	var withPath struct {
		URL  string `json:"url"`
		Path string `json:"path"`
	}
	_ = json.Unmarshal(body, &withPath)
	if withPath.Path != "/login" {
		t.Fatalf("path = %q, want /login", withPath.Path)
	}
	if resp, page = get(browser(), withPath.URL); resp.StatusCode != 200 || page != "login page" || resp.Request.URL.Path != "/login" {
		t.Fatalf("launch with path → %d %q at %s", resp.StatusCode, page, resp.Request.URL)
	}

	// The sweeper ends a link that was never opened.
	code, body = call("POST", "/devices/"+dev+"/remote-sessions", map[string]any{"scheme": "http", "port": devPort})
	if code != http.StatusCreated {
		t.Fatalf("second start = %d %s", code, body)
	}
	_ = json.Unmarshal(body, &started)
	if _, err := su.Exec(ctx, `UPDATE remote_session SET created_at = now() - interval '10 minutes' WHERE id = $1`, started.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if r := q(`SELECT COALESCE(end_reason, '') FROM remote_session WHERE id = $1`, started.ID); r != "not opened" {
		t.Fatalf("end reason = %q, want not opened", r)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting")
}
