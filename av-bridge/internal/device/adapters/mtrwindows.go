package adapters

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/go-ntlmssp"
	"github.com/masterzen/winrm"
	"github.com/masterzen/winrm/soap"

	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// MTRWindowsAdapter monitors a Microsoft Teams Rooms on Windows PC over
// WinRM, read-only (protocol mtr_windows).
//
// Each poll opens a WinRM shell and runs one fixed PowerShell script
// (mtrwindows.ps1, embedded) that prints one JSON object: the Skype Room
// System event log (heartbeat, errors, peripheral health), whether the
// microphone or camera is in use, the Teams UDP media sockets, and the PC's
// OS, uptime and make/model/serial. mtrReadings turns that into the same
// readings Poly Lens Teams Rooms report, so the portal and alerts are shared.
//
// Connection:
//   - WinRM over HTTPS on 5986 by default; tags.port overrides. HTTP (5985)
//     only with tags.winrm_https: "false" — not recommended: it needs
//     AllowUnencrypted on the PC, and the WinRM traffic is then readable on
//     the network.
//   - NTLM sign-in as the device's username/password (a local account on the
//     PC: "Admin", ".\Admin" or "PCNAME\Admin").
//   - TLS: the certificate is verified unless tags.tls_skip_verify: "true",
//     as for the other device adapters. MTR WinRM listeners normally use a
//     self-signed certificate, so most sites set it. The device TLS profile
//     (tls_compat.go) is used, and no proxy: the PC is on the site network.
//
// No commands in v1. Everything Windows-side is unverified until tried on a
// real MTR; see the "unverified" notes here, in mtrwindows_parse.go and in
// mtrwindows.ps1.
type MTRWindowsAdapter struct {
	device.Base
	run mtrRunFunc
}

// mtrRunFunc runs a command line on the PC and returns its stdout and
// stderr. Swapped out in tests.
type mtrRunFunc func(ctx context.Context, command string) (stdout, stderr string, exitCode int, err error)

//go:embed mtrwindows.ps1
var mtrScript string

// errWinRMAuth is the error for a sign-in the PC refused (HTTP 401).
var errWinRMAuth = errors.New("WinRM sign-in rejected")

const (
	mtrDialTimeout = 10 * time.Second
	// mtrHTTPTimeout bounds one WinRM request. It must outlast the WS-Man
	// operation timeout (mtrOperationTimeout), for which the PC holds a
	// Receive open while the script has no output yet.
	mtrHTTPTimeout      = 45 * time.Second
	mtrOperationTimeout = "PT30S"
	// mtrPollTimeout bounds a whole poll: shell, script, output, close.
	mtrPollTimeout = 50 * time.Second
	// mtrMaxCommandLine: WinRS runs the command through cmd.exe, whose
	// command line is limited to 8191 characters.
	mtrMaxCommandLine = 8000
)

func NewMTRWindowsAdapter(cfg config.DeviceConfig) *MTRWindowsAdapter {
	a := &MTRWindowsAdapter{Base: device.NewBase(cfg)}
	a.run = a.winrmRun
	return a
}

// mtrEndpoint works out host, port and scheme from the address and tags.
// The address may be a host, host:port or an http(s):// URL.
func mtrEndpoint(cfg config.DeviceConfig) (host string, port int, https bool, err error) {
	https = !strings.EqualFold(strings.TrimSpace(cfg.Tags["winrm_https"]), "false")
	addr := strings.TrimSpace(cfg.Address)
	if i := strings.Index(addr, "://"); i >= 0 {
		u, perr := url.Parse(addr)
		if perr != nil {
			return "", 0, false, fmt.Errorf("address %q: %w", cfg.Address, perr)
		}
		addr = u.Host
	}
	addr = strings.TrimRight(addr, "/")
	host = addr
	if h, p, serr := net.SplitHostPort(addr); serr == nil {
		host = h
		if n, aerr := strconv.Atoi(p); aerr == nil {
			port = n
		}
	}
	if p := strings.TrimSpace(cfg.Tags["port"]); p != "" {
		n, aerr := strconv.Atoi(p)
		if aerr != nil || n <= 0 || n > 65535 {
			return "", 0, false, fmt.Errorf("tags.port %q is not a port number", p)
		}
		port = n
	}
	if port == 0 {
		port = 5986
		if !https {
			port = 5985
		}
	}
	if host == "" {
		return "", 0, false, errors.New("address is empty")
	}
	return host, port, https, nil
}

// ── Connection ────────────────────────────────────────────────────────────

// Connect proves the address and sign-in work with a trivial command. The
// adapter keeps no session: each poll opens its own WinRM shell.
func (a *MTRWindowsAdapter) Connect(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, mtrPollTimeout)
	defer cancel()
	if _, _, _, err := a.run(ctx, "hostname"); err != nil {
		a.SetStatus(device.StatusOffline)
		return fmt.Errorf("mtr_windows connect %s: %s", a.Cfg.ID, mtrErrorText(err))
	}
	a.SetStatus(device.StatusOnline)
	slog.Info("mtr_windows connected", "device", a.Cfg.ID, "address", a.Cfg.Address)
	return nil
}

func (a *MTRWindowsAdapter) Disconnect() error {
	a.SetStatus(device.StatusOffline)
	return nil
}

// ── Poll ─────────────────────────────────────────────────────────────────

func (a *MTRWindowsAdapter) Poll(ctx context.Context) (*device.Telemetry, error) {
	t := a.BaseTelemetry()
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, mtrPollTimeout)
	defer cancel()

	cmd, err := mtrCommandLine(mtrScript)
	if err != nil {
		return nil, err
	}
	stdout, stderr, _, err := a.run(ctx, cmd)
	if err != nil {
		// Unreachable, timed out or sign-in refused: offline.
		a.SetStatus(device.StatusOffline)
		t.Status = device.StatusOffline
		t.Error = mtrErrorText(err)
		return t, nil
	}

	metrics, sectionErrs, perr := mtrReadings([]byte(stdout), time.Now().UTC())
	a.SetStatus(device.StatusOnline)
	t.Status = device.StatusOnline
	if perr != nil {
		// WinRM worked but the script printed nothing usable (PowerShell
		// blocked, say): the PC is up, its Teams state unknown.
		t.Error = perr.Error()
		if s := strings.TrimSpace(stderr); s != "" {
			slog.Warn("mtr_windows script failed", "device", a.Cfg.ID, "stderr", truncate(s, 500))
		}
		metrics = map[string]any{"call_state": "unknown", "active_calls": 0}
	}
	if len(sectionErrs) > 0 {
		slog.Debug("mtr_windows script sections unreadable", "device", a.Cfg.ID, "sections", sectionErrs)
	}
	metrics["response_ms"] = time.Since(start).Milliseconds()
	t.Metrics = metrics
	return t, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// mtrErrorText is the reading error for a failed WinRM run.
func mtrErrorText(err error) string {
	if mtrIsAuthError(err) {
		return errWinRMAuth.Error()
	}
	var ce mtrConfigError
	if errors.As(err, &ce) {
		return "mtr_windows settings: " + ce.Error()
	}
	var ne net.Error
	if (errors.As(err, &ne) && ne.Timeout()) || errors.Is(err, context.DeadlineExceeded) {
		return "WinRM timed out: " + err.Error()
	}
	return "WinRM unreachable: " + err.Error()
}

// mtrConfigError is a problem with the device's settings (no credentials, a
// bad address or port), not with reaching the PC.
type mtrConfigError struct{ error }

func (e mtrConfigError) Unwrap() error { return e.error }

// mtrIsAuthError: the WinRM library doesn't always wrap transport errors
// with %w, so match the text as well.
func mtrIsAuthError(err error) bool {
	return err != nil && (errors.Is(err, errWinRMAuth) || strings.Contains(err.Error(), errWinRMAuth.Error()))
}

// ── Commands ─────────────────────────────────────────────────────────────

// SendCommand: the adapter is read-only in v1.
func (a *MTRWindowsAdapter) SendCommand(_ context.Context, req device.CommandRequest) (*device.CommandResponse, error) {
	return nil, fmt.Errorf("mtr_windows: unsupported command %q (this adapter is read-only)", req.Name)
}

var mtrWindowsCapabilities = device.Capabilities{
	Metrics: []string{
		"teams_signed_in", "teams_health", "teams_app_version",
		"call_state", "active_calls", "call_signal_mic", "call_signal_udp", "udp_media_sockets",
		"peripherals", "peripherals_missing", "peripherals_linked",
		"software_version", "os_name", "uptime_s", "last_heartbeat", "last_error",
		"manufacturer", "model", "serial_number",
		"response_ms",
	},
}

func (a *MTRWindowsAdapter) Capabilities() device.Capabilities { return mtrWindowsCapabilities }

// ── Running the script ───────────────────────────────────────────────────

// mtrCommandLine wraps the script for WinRS. -EncodedCommand (UTF-16 then
// base64) would make the command line about 2.7 times the script's size,
// past cmd.exe's 8191-character limit, so the script travels gzipped and
// base64-encoded (only A-Z a-z 0-9 + / =, safe inside cmd quoting) and a
// short bootstrap unpacks and runs it in memory. Nothing is written to disk.
func mtrCommandLine(script string) (string, error) {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if _, err := zw.Write([]byte(script)); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	b64 := base64.StdEncoding.EncodeToString(buf.Bytes())
	boot := "$ProgressPreference='SilentlyContinue';" +
		"$m=New-Object IO.MemoryStream(,[Convert]::FromBase64String('" + b64 + "'));" +
		"$g=New-Object IO.Compression.GZipStream($m,[IO.Compression.CompressionMode]::Decompress);" +
		"$s=(New-Object IO.StreamReader($g,[Text.Encoding]::UTF8)).ReadToEnd();" +
		"& ([scriptblock]::Create($s))"
	cmd := `powershell.exe -NoLogo -NoProfile -NonInteractive -Command "` + boot + `"`
	if len(cmd) > mtrMaxCommandLine {
		return "", fmt.Errorf("mtr_windows: script command line is %d characters, over the %d limit", len(cmd), mtrMaxCommandLine)
	}
	return cmd, nil
}

// winrmRun runs one command line on the PC over WinRM.
func (a *MTRWindowsAdapter) winrmRun(ctx context.Context, command string) (string, string, int, error) {
	if a.Cfg.Username == "" || a.Cfg.Password == "" {
		return "", "", 1, mtrConfigError{errors.New("username and password are required (a local account on the Teams Rooms PC)")}
	}
	host, port, https, err := mtrEndpoint(a.Cfg)
	if err != nil {
		return "", "", 1, mtrConfigError{err}
	}
	tr := newMTRTransport(a.Cfg.Username, a.Cfg.Password, a.Cfg.Tags["tls_skip_verify"] == "true")
	params := winrm.NewParameters(mtrOperationTimeout, "en-US", 153600)
	params.TransportDecorator = func() winrm.Transporter { return tr }
	ep := winrm.NewEndpoint(host, port, https, false, nil, nil, nil, mtrHTTPTimeout)
	client, err := winrm.NewClientWithParameters(ep, a.Cfg.Username, a.Cfg.Password, params)
	if err != nil {
		return "", "", 1, err
	}
	defer tr.close()

	// The library's HTTP requests don't follow ctx (only the remote command
	// does), so give up waiting at the deadline; the transport's own
	// timeouts end the abandoned request soon after.
	type result struct {
		stdout, stderr string
		code           int
		err            error
	}
	done := make(chan result, 1)
	go func() {
		o, e, c, rerr := client.RunCmdWithContext(ctx, command)
		done <- result{o, e, c, rerr}
	}()
	select {
	case r := <-done:
		return r.stdout, r.stderr, r.code, r.err
	case <-ctx.Done():
		return "", "", 1, ctx.Err()
	}
}

// mtrTransport is the WinRM HTTP transport: NTLM via go-ntlmssp over the
// device TLS profile, with no proxy (the PC is on the site network, while
// the Collector's HTTPS_PROXY is for the cloud) and one connection, which
// NTLM's per-connection handshake needs. The WinRM library builds and parses
// the WS-Management messages; this only carries them.
type mtrTransport struct {
	user, pass string
	url        string
	inner      *http.Transport
	client     *http.Client
}

func newMTRTransport(user, pass string, skipVerify bool) *mtrTransport {
	inner := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: mtrDialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       deviceTLSConfig(skipVerify),
		TLSHandshakeTimeout:   mtrDialTimeout,
		ResponseHeaderTimeout: mtrHTTPTimeout,
		MaxConnsPerHost:       1,
		MaxIdleConnsPerHost:   1,
	}
	return &mtrTransport{
		user:  user,
		pass:  pass,
		inner: inner,
		client: &http.Client{
			Timeout:   mtrHTTPTimeout,
			Transport: ntlmssp.Negotiator{RoundTripper: noBasicAuth{inner}},
		},
	}
}

// Transport implements winrm.Transporter.
func (t *mtrTransport) Transport(ep *winrm.Endpoint) error {
	scheme := "http"
	if ep.HTTPS {
		scheme = "https"
	}
	t.url = (&url.URL{Scheme: scheme, Host: net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port)), Path: "/wsman"}).String()
	return nil
}

// Post implements winrm.Transporter: send one SOAP message, return the
// SOAP reply.
func (t *mtrTransport) Post(_ *winrm.Client, msg *soap.SoapMessage) (string, error) {
	req, err := http.NewRequest(http.MethodPost, t.url, strings.NewReader(msg.String()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/soap+xml;charset=UTF-8")
	req.SetBasicAuth(t.user, t.pass) // converted to NTLM by the Negotiator
	resp, err := t.client.Do(req)
	if err != nil {
		if errors.Is(err, errBasicRefused) {
			return "", errWinRMAuth
		}
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return "", errWinRMAuth
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("WinRM HTTP %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(body)), 300))
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/soap+xml") {
		return "", fmt.Errorf("WinRM reply is %q, not SOAP", resp.Header.Get("Content-Type"))
	}
	return string(body), nil
}

func (t *mtrTransport) close() { t.inner.CloseIdleConnections() }

// errBasicRefused: the server offered only Basic authentication. The
// Negotiator would then send the password in the clear (base64), so the
// request is stopped instead; the PC should offer Negotiate (NTLM).
var errBasicRefused = errors.New("WinRM offered only Basic authentication; refusing to send the password")

// noBasicAuth refuses requests carrying Basic credentials. The Negotiator
// only sends Basic when the server offers nothing better.
type noBasicAuth struct{ rt http.RoundTripper }

func (n noBasicAuth) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(req.Header.Get("Authorization"), "Basic ") {
		return nil, errBasicRefused
	}
	return n.rt.RoundTrip(req)
}
