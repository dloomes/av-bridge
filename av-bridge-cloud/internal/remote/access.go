package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// launchPath swaps a session's one-time launch code for its cookie.
// Reserved: a device page at this path can't be reached through a session.
const launchPath = "/__marcus/launch"

// cookieName holds the session's cookie on its own address. Never passed on
// to the device.
const cookieName = "marcus_rs"

// sessionTarget is what a request on a session address needs.
type sessionTarget struct {
	id, customerID, label string
	cookieHash            []byte
	ended                 bool
	expires, lastActivity time.Time
	scheme                string
	port                  int
	reportedID, address   string
	machine               string // collector serving the device now
}

func (s *Service) loadSession(ctx context.Context, label string) (sessionTarget, error) {
	var t sessionTarget
	var ended *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT rs.id::text, rs.customer_id::text, rs.host_label, rs.cookie_hash,
		       rs.ended_at, rs.expires_at, rs.last_activity_at, rs.scheme, rs.port,
		       d.reported_id, COALESCE(d.address, ''), (`+TargetSQL+`)::text
		  FROM remote_session rs
		  JOIN devices d    ON d.id = rs.device_id
		  JOIN collectors c ON c.id = d.collector_id
		 WHERE rs.host_label = $1`, label).Scan(
		&t.id, &t.customerID, &t.label, &t.cookieHash,
		&ended, &t.expires, &t.lastActivity, &t.scheme, &t.port,
		&t.reportedID, &t.address, &t.machine)
	t.ended = ended != nil
	return t, err
}

// ServeHTTP handles every request on a session address.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	label, ok := s.labelOf(r.Host)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == launchPath {
		s.launch(w, r, label)
		return
	}
	t, err := s.loadSession(r.Context(), label)
	if errors.Is(err, pgx.ErrNoRows) {
		page(w, http.StatusNotFound, "Session not found", "This remote access link isn't valid. Start a new session from the device's page in the portal.")
		return
	}
	if err != nil {
		s.log.Error("remote access: loading session failed", "error", err)
		page(w, http.StatusInternalServerError, "Something went wrong", "Try again, or start a new session from the portal.")
		return
	}
	c, err := r.Cookie(cookieName)
	if err != nil || t.cookieHash == nil || !matches(c.Value, t.cookieHash) {
		page(w, http.StatusForbidden, "Open this session from the portal",
			"This session belongs to the browser that opened it. Start your own from the device's page in the portal.")
		return
	}
	now := time.Now()
	switch {
	case t.ended:
		page(w, http.StatusGone, "Session ended", "Start a new session from the device's page in the portal.")
		return
	case now.After(t.expires):
		s.End(r.Context(), t.id, "time limit")
		page(w, http.StatusGone, "Session ended", "Sessions last at most 8 hours. Start a new one from the portal.")
		return
	case now.Sub(t.lastActivity) > IdleTimeout:
		s.End(r.Context(), t.id, "idle")
		page(w, http.StatusGone, "Session ended", "The session was idle for 15 minutes. Start a new one from the portal.")
		return
	}

	// Device pages can be slow, large or long-lived (a websocket): lift
	// the server's per-request timeouts, bounded by the session's limit.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(t.expires)
	_ = rc.SetWriteDeadline(t.expires)

	in := &countingBody{ReadCloser: r.Body}
	r.Body = in
	cw := &countingWriter{ResponseWriter: w}
	s.proxy(t).ServeHTTP(cw, r)
	s.count(context.WithoutCancel(r.Context()), t.id, cw.n, in.n)
}

// launch swaps the one-time code for a cookie on this address, then goes
// to the device's home page. The link works once, within LaunchTimeout.
func (s *Service) launch(w http.ResponseWriter, r *http.Request, label string) {
	code := r.URL.Query().Get("code")
	sum := sha256.Sum256([]byte(code))
	tok := make([]byte, 32)
	_, _ = rand.Read(tok)
	token := base64.RawURLEncoding.EncodeToString(tok)
	cookieSum := sha256.Sum256([]byte(token))
	var id, startPath string
	err := s.pool.QueryRow(r.Context(), `
		UPDATE remote_session
		   SET launched_at = now(), last_activity_at = now(), cookie_hash = $3
		 WHERE host_label = $1 AND launch_code_hash = $2
		   AND launched_at IS NULL AND ended_at IS NULL
		   AND created_at > now() - make_interval(secs => $4)
		RETURNING id::text, start_path`, label, sum[:], cookieSum[:], LaunchTimeout.Seconds()).Scan(&id, &startPath)
	if err != nil {
		page(w, http.StatusForbidden, "This link has already been used or has expired",
			"Each remote access link opens once, within 5 minutes. Start a new session from the device's page in the portal.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	// The page the session was started on (checked by CleanStartPath and
	// the table's constraint, so it stays on this address).
	http.Redirect(w, r, startPath, http.StatusSeeOther)
}

func matches(value string, hash []byte) bool {
	sum := sha256.Sum256([]byte(value))
	return subtle.ConstantTimeCompare(sum[:], hash) == 1
}

// proxy returns the session's reverse proxy to its device. Connections to
// the device go through the collector's tunnel and are reused within the
// session.
func (s *Service) proxy(t sessionTarget) http.Handler {
	host, _ := SplitHost(t.address)
	deviceHost := net.JoinHostPort(host, strconv.Itoa(t.port))
	if (t.scheme == "http" && t.port == 80) || (t.scheme == "https" && t.port == 443) {
		deviceHost = host
	}
	sessionOrigin := "https://" + t.label + "." + s.domain

	s.mu.Lock()
	tr := s.transports[t.id]
	if tr == nil {
		machine, reported, port := t.machine, t.reportedID, t.port
		tr = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return s.tunnels.Dial(ctx, machine, reported, port)
			},
			// Devices nearly all use self-signed certificates. The hop
			// from the cloud to the device runs inside the collector's
			// authenticated tunnel.
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
			MaxIdleConnsPerHost:   6,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		}
		s.transports[t.id] = tr
	}
	s.mu.Unlock()

	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = t.scheme
			pr.Out.URL.Host = deviceHost
			pr.Out.Host = deviceHost
			stripCookie(pr.Out, cookieName)
			pr.Out.Header.Del("X-Forwarded-For")
			// Same-origin checks on the device expect its own address.
			if pr.Out.Header.Get("Origin") != "" {
				pr.Out.Header.Set("Origin", t.scheme+"://"+deviceHost)
			}
			if ref := pr.Out.Header.Get("Referer"); ref != "" {
				pr.Out.Header.Set("Referer", strings.Replace(ref, sessionOrigin, t.scheme+"://"+deviceHost, 1))
			}
		},
		Transport: tr,
		ModifyResponse: func(resp *http.Response) error {
			// Redirects to the device's own address stay in the session.
			if loc := resp.Header.Get("Location"); loc != "" {
				if u, err := url.Parse(loc); err == nil && u.IsAbs() && strings.EqualFold(u.Hostname(), host) {
					resp.Header.Set("Location", sessionOrigin+u.RequestURI())
				}
			}
			// Cookies belong to the session address, not the device's.
			if cs := resp.Header.Values("Set-Cookie"); len(cs) > 0 {
				resp.Header.Del("Set-Cookie")
				for _, c := range cs {
					resp.Header.Add("Set-Cookie", stripCookieDomain(c))
				}
			}
			// A device's HSTS would apply to the session address.
			resp.Header.Del("Strict-Transport-Security")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.log.Info("remote access: device unreachable", "session", t.id, "error", err)
			msg := err.Error()
			if errors.Is(err, ErrNotConnected) {
				msg = "The device's Collector isn't connected for remote access. It may be offline, or need updating."
			}
			page(w, http.StatusBadGateway, "Can't reach the device", msg)
		},
	}
}

func stripCookie(r *http.Request, name string) {
	cs := r.Cookies()
	if len(cs) == 0 {
		return
	}
	r.Header.Del("Cookie")
	for _, c := range cs {
		if c.Name != name {
			r.AddCookie(c)
		}
	}
}

func stripCookieDomain(setCookie string) string {
	parts := strings.Split(setCookie, ";")
	out := parts[:1]
	for _, p := range parts[1:] {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p)), "domain=") {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, ";")
}

// SplitHost returns a device address's host and port, if any. Addresses may
// be a bare host, host:port or a URL.
func SplitHost(addr string) (string, int) {
	addr = strings.TrimSpace(addr)
	if strings.Contains(addr, "://") {
		if u, err := url.Parse(addr); err == nil {
			p, _ := strconv.Atoi(u.Port())
			return u.Hostname(), p
		}
	}
	if h, p, err := net.SplitHostPort(addr); err == nil {
		port, _ := strconv.Atoi(p)
		return h, port
	}
	return strings.Trim(addr, "[]"), 0
}

// DefaultTarget is the scheme and port a session uses unless asked for
// another: the device address's own scheme and port when it's a URL, its
// web_port tag, else HTTP on 80.
func DefaultTarget(address, webPortTag string) (string, int) {
	if strings.HasPrefix(strings.ToLower(address), "https://") {
		if _, p := SplitHost(address); p > 0 {
			return "https", p
		}
		return "https", 443
	}
	if strings.HasPrefix(strings.ToLower(address), "http://") {
		if _, p := SplitHost(address); p > 0 {
			return "http", p
		}
		return "http", 80
	}
	if p, err := strconv.Atoi(webPortTag); err == nil && p > 0 {
		if p == 443 || p == 8443 {
			return "https", p
		}
		return "http", p
	}
	return "http", 80
}

func page(w http.ResponseWriter, status int, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title>
<style>body{font-family:system-ui,sans-serif;background:#f5f6f8;color:#0f172a;margin:0;display:flex;min-height:100vh;align-items:center;justify-content:center}
main{background:#fff;border-radius:12px;padding:32px;max-width:440px;margin:16px;box-shadow:0 1px 3px rgba(15,23,42,.08)}
h1{font-size:18px;margin:0 0 8px}p{font-size:14px;color:#475569;margin:0;line-height:1.5}small{display:block;margin-top:16px;color:#94a3b8;font-size:12px}</style></head>
<body><main><h1>%s</h1><p>%s</p><small>Remote access</small></main></body></html>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(msg))
}

type countingBody struct {
	io.ReadCloser
	n int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n += int64(n)
	return n, err
}

type countingWriter struct {
	http.ResponseWriter
	n int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.n += int64(n)
	return n, err
}

func (w *countingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *countingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// CleanStartPath checks the page a session opens on: a path on the device,
// with an optional query, such as "/login" or "index.html". "" is "/".
// Anything that could leave the session's own address ("//host", a scheme,
// backslashes) is refused.
func CleanStartPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/", nil
	}
	if strings.Contains(p, "://") {
		return "", errors.New("enter just the path on the device, such as /login, not a full web address")
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	switch {
	case len(p) > 512:
		return "", errors.New("path is too long")
	case strings.HasPrefix(p, "//"), strings.ContainsAny(p, `\#`):
		return "", errors.New("path must be a page on the device, such as /login")
	case strings.HasPrefix(p, launchPath):
		return "", errors.New("that path is reserved")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return "", errors.New("path can't contain spaces or control characters")
		}
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "", errors.New("path must be a page on the device, such as /login")
	}
	return p, nil
}
