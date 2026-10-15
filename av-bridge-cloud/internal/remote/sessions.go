package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
)

// Session limits.
const (
	IdleTimeout   = 15 * time.Minute // no traffic for this long ends a session
	MaxDuration   = 8 * time.Hour    // hard limit from when it was started
	LaunchTimeout = 5 * time.Minute  // the launch link must be used within this

	sweepInterval    = time.Minute
	activityInterval = 30 * time.Second // how often traffic is written back
)

// Service is remote access's cloud side: session addresses, the proxy to
// the device, and the sweeper that ends idle and expired sessions. Uses the
// admin pool: requests on a session address carry no portal sign-in, only
// the session's own cookie, so they resolve the session across tenants.
type Service struct {
	pool    *pgxpool.Pool
	tunnels *Tunnels
	domain  string // e.g. access.uat.involvecloud.com; "" = remote access off
	log     *slog.Logger

	mu         sync.Mutex
	transports map[string]*http.Transport // per session, reusing device connections
	traffic    map[string]*traffic        // per session, not yet written back
}

type traffic struct {
	in, out   int64
	lastWrite time.Time
}

func NewService(pool *pgxpool.Pool, tunnels *Tunnels, domain string, log *slog.Logger) *Service {
	return &Service{
		pool: pool, tunnels: tunnels, domain: strings.Trim(strings.ToLower(domain), "."), log: log,
		transports: map[string]*http.Transport{},
		traffic:    map[string]*traffic{},
	}
}

// Enabled reports whether remote access is set up (an access domain).
func (s *Service) Enabled() bool { return s != nil && s.domain != "" }

// Tunnels exposes the tunnel registry (the portal API checks a collector
// is connected before starting a session).
func (s *Service) Tunnels() *Tunnels { return s.tunnels }

// NewCredentials returns a fresh session address label, launch code and the
// code's hash (the only part stored).
func NewCredentials() (label, code string, codeHash []byte) {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	label = "s-" + strings.ToLower(strings.TrimRight(base32.StdEncoding.EncodeToString(b), "="))
	c := make([]byte, 32)
	_, _ = rand.Read(c)
	code = base64.RawURLEncoding.EncodeToString(c)
	h := sha256.Sum256([]byte(code))
	return label, code, h[:]
}

// LaunchURL is the link that opens a session in a browser tab.
func (s *Service) LaunchURL(label, code string) string {
	return "https://" + label + "." + s.domain + launchPath + "?code=" + code
}

// IsAccessHost reports whether a request is for a session address.
func (s *Service) IsAccessHost(host string) bool {
	if !s.Enabled() {
		return false
	}
	_, ok := s.labelOf(host)
	return ok
}

func (s *Service) labelOf(host string) (string, bool) {
	host = strings.ToLower(host)
	if i := strings.LastIndexByte(host, ':'); i > 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	label, ok := strings.CutSuffix(host, "."+s.domain)
	if !ok || label == "" || strings.Contains(label, ".") {
		return "", false
	}
	return label, true
}

// TargetSQL resolves the machine that serves a device's collector: the
// warm-standby lease holder when a pair has one, else the collector itself.
// "c" is the device's collectors row.
const TargetSQL = `CASE WHEN c.lease_holder IS NOT NULL AND c.lease_expires_at > now()
	                  THEN c.lease_holder ELSE c.id END`

// Run ends idle, expired and never-launched sessions until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Sweep(ctx); err != nil {
				s.log.Warn("remote access sweep failed", "error", err)
			}
		}
	}
}

// Sweep ends every session past its limits. Exposed for tests.
func (s *Service) Sweep(ctx context.Context) error {
	s.flushAll(ctx)
	rows, err := s.pool.Query(ctx, `
		UPDATE remote_session
		   SET ended_at = now(),
		       end_reason = CASE
		         WHEN launched_at IS NULL THEN 'not opened'
		         WHEN expires_at <= now() THEN 'time limit'
		         ELSE 'idle' END
		 WHERE ended_at IS NULL
		   AND (expires_at <= now()
		        OR last_activity_at < now() - make_interval(secs => $1)
		        OR (launched_at IS NULL AND created_at < now() - make_interval(secs => $2)))
		RETURNING id::text`, IdleTimeout.Seconds(), LaunchTimeout.Seconds())
	if err != nil {
		return err
	}
	var ended []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ended = append(ended, id)
	}
	rows.Close()
	for _, id := range ended {
		s.auditEnd(ctx, id)
		s.forget(id)
	}
	return rows.Err()
}

// End ends a session (someone ended it, or it was found past its limits).
func (s *Service) End(ctx context.Context, id, reason string) {
	s.flush(ctx, id, true)
	tag, err := s.pool.Exec(ctx,
		`UPDATE remote_session SET ended_at = now(), end_reason = $2 WHERE id = $1 AND ended_at IS NULL`, id, reason)
	if err == nil && tag.RowsAffected() > 0 {
		s.auditEnd(ctx, id)
	}
	s.forget(id)
}

func (s *Service) auditEnd(ctx context.Context, id string) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var (
			customerID, deviceID, reason, actor string
			created                             time.Time
			ended                               time.Time
			in, out                             int64
		)
		if err := tx.QueryRow(ctx, `
			SELECT customer_id::text, device_id::text, COALESCE(end_reason, ''), actor,
			       created_at, ended_at, bytes_in, bytes_out
			  FROM remote_session WHERE id = $1`, id).Scan(
			&customerID, &deviceID, &reason, &actor, &created, &ended, &in, &out); err != nil {
			return err
		}
		return audit.Record(ctx, tx, customerID, audit.Entry{
			Actor:      "system:remote-access",
			Action:     "remote.session.end",
			TargetKind: "device", TargetID: deviceID,
			Metadata: map[string]any{
				"session_id": id, "opened_by": actor, "reason": reason,
				"duration_seconds": int(ended.Sub(created).Seconds()),
				"bytes_to_browser": in, "bytes_to_device": out,
			},
		})
	})
	if err != nil {
		s.log.Warn("remote access: audit of session end failed", "session", id, "error", err)
	}
}

// count adds a request's traffic to its session, writing it back (with the
// activity time that keeps the session alive) at most every
// activityInterval.
func (s *Service) count(ctx context.Context, id string, in, out int64) {
	s.mu.Lock()
	t := s.traffic[id]
	if t == nil {
		t = &traffic{}
		s.traffic[id] = t
	}
	t.in += in
	t.out += out
	due := time.Since(t.lastWrite) >= activityInterval
	s.mu.Unlock()
	if due {
		s.flush(ctx, id, false)
	}
}

func (s *Service) flush(ctx context.Context, id string, final bool) {
	s.mu.Lock()
	t := s.traffic[id]
	var in, out int64
	if t != nil {
		in, out = t.in, t.out
		t.in, t.out = 0, 0
		t.lastWrite = time.Now()
	}
	s.mu.Unlock()
	if t == nil && !final {
		return
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE remote_session
		   SET bytes_in = bytes_in + $2, bytes_out = bytes_out + $3,
		       last_activity_at = CASE WHEN $4 THEN last_activity_at ELSE now() END
		 WHERE id = $1`, id, in, out, final); err != nil {
		s.log.Warn("remote access: writing traffic failed", "session", id, "error", err)
	}
}

func (s *Service) flushAll(ctx context.Context) {
	s.mu.Lock()
	var ids []string
	for id, t := range s.traffic {
		if t.in > 0 || t.out > 0 {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.flush(ctx, id, false)
	}
}

func (s *Service) forget(id string) {
	s.mu.Lock()
	if tr := s.transports[id]; tr != nil {
		tr.CloseIdleConnections()
	}
	delete(s.transports, id)
	delete(s.traffic, id)
	s.mu.Unlock()
}

// Machine is the collector machine that serves a collector's devices now:
// its warm-standby lease holder when it has one, else itself.
func (s *Service) Machine(ctx context.Context, collectorID string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT (`+TargetSQL+`)::text FROM collectors c WHERE c.id = $1`, collectorID).Scan(&id)
	return id, err
}
