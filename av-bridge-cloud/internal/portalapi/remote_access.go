package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/remote"
)

// SetRemoteAccess wires remote access. Without it (or without an access
// domain) the remote-session endpoints answer 503.
func (h *Handler) SetRemoteAccess(svc *remote.Service) *Handler {
	h.remote = svc
	return h
}

// webPorts are the ports a session can use on any device; a device's own
// port and its web_port tag are allowed too. The collector enforces the
// same rule.
var webPorts = map[int]bool{80: true, 443: true, 8080: true, 8443: true}

// StartRemoteSession — POST /api/v1/devices/{id}/remote-sessions
// body (optional): { scheme: "http"|"https", port: N, path: "/login" }
//
// Starts a session on the device's web interface and returns its launch
// link, which opens once, within 5 minutes, in a new browser tab.
func (h *Handler) StartRemoteSession(w http.ResponseWriter, r *http.Request) {
	if !h.remote.Enabled() {
		writeErr(w, http.StatusServiceUnavailable, "remote access isn't set up on this server")
		return
	}
	var req struct {
		Scheme string `json:"scheme"`
		Port   int    `json:"port"`
		Path   string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	p, _ := portalauth.From(r.Context())
	deviceID := r.PathValue("id")

	var (
		reportedID, address, webPortTag, webPathTag, collectorID string
		found                                                    bool
	)
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT COALESCE(reported_id, ''), COALESCE(address, ''),
			       COALESCE(tags->>'web_port', ''), COALESCE(tags->>'web_path', ''), collector_id::text
			  FROM devices WHERE id = $1`, deviceID).Scan(&reportedID, &address, &webPortTag, &webPathTag, &collectorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		found = err == nil
		return err
	}) {
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}
	if reportedID == "" || address == "" {
		writeErr(w, http.StatusConflict, "this device has no network address on its Collector")
		return
	}

	scheme, port := remote.DefaultTarget(address, webPortTag)
	if req.Scheme != "" {
		scheme = strings.ToLower(req.Scheme)
		if req.Port == 0 {
			port = map[string]int{"http": 80, "https": 443}[scheme]
		}
	}
	if req.Port != 0 {
		port = req.Port
	}
	if scheme != "http" && scheme != "https" {
		writeErr(w, http.StatusBadRequest, "scheme must be http or https")
		return
	}
	if !remotePortAllowed(port, address, webPortTag) {
		writeErr(w, http.StatusBadRequest, "port "+strconv.Itoa(port)+" isn't a web port for this device (80, 443, 8080, 8443, its own port or its web_port tag)")
		return
	}

	// The page to open on: as asked, else the device's web_path tag, else "/".
	path := req.Path
	if strings.TrimSpace(path) == "" {
		path = webPathTag
	}
	startPath, err := remote.CleanStartPath(path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	machine, err := h.remote.Machine(r.Context(), collectorID)
	if err != nil {
		h.log.Error("remote access: resolving collector failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !h.remote.Tunnels().Connected(machine) {
		writeErr(w, http.StatusConflict, "the device's Collector isn't connected for remote access: it may be offline, or need updating to the latest version")
		return
	}

	label, code, codeHash := remote.NewCredentials()
	var (
		sessionID string
		expires   time.Time
	)
	var userID *string
	if p.UserID != "" {
		userID = &p.UserID
	}
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO remote_session
			       (customer_id, device_id, user_id, actor, host_label, scheme, port,
			        launch_code_hash, expires_at, start_path)
			VALUES ($1, $2, $3::uuid, $4, $5, $6, $7, $8, now() + make_interval(secs => $9), $10)
			RETURNING id::text, expires_at`,
			p.CustomerID, deviceID, userID, p.ActorLabel(), label, scheme, port,
			codeHash, remote.MaxDuration.Seconds(), startPath).Scan(&sessionID, &expires); err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "remote.session.start",
			TargetKind: "device", TargetID: deviceID,
			Metadata: map[string]any{"session_id": sessionID, "kind": "web", "scheme": scheme, "port": port, "path": startPath},
		}))
	}) {
		return
	}
	// The link carries the one-time code: never cache it.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         sessionID,
		"url":        h.remote.LaunchURL(label, code),
		"scheme":     scheme,
		"port":       port,
		"path":       startPath,
		"expires_at": expires,
	})
}

func remotePortAllowed(port int, address, webPortTag string) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	if webPorts[port] {
		return true
	}
	if _, own := remote.SplitHost(address); own != 0 && own == port {
		return true
	}
	p, err := strconv.Atoi(webPortTag)
	return err == nil && p == port
}

type remoteSession struct {
	ID             string     `json:"id"`
	DeviceID       string     `json:"device_id"`
	DeviceName     string     `json:"device_name"`
	Actor          string     `json:"actor"`
	Kind           string     `json:"kind"`
	Scheme         string     `json:"scheme"`
	Port           int        `json:"port"`
	CreatedAt      time.Time  `json:"created_at"`
	LaunchedAt     *time.Time `json:"launched_at"`
	LastActivityAt time.Time  `json:"last_activity_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	EndedAt        *time.Time `json:"ended_at"`
	EndReason      *string    `json:"end_reason"`
	BytesIn        int64      `json:"bytes_to_browser"`
	BytesOut       int64      `json:"bytes_to_device"`
}

// ListRemoteSessions — GET /api/v1/remote-sessions?device_id=&active=true
//
// Sessions on devices the caller can see, newest first. active=true (the
// default) returns only open sessions; active=false the last 100 of any.
func (h *Handler) ListRemoteSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	activeOnly := q.Get("active") != "false"
	var deviceID *string
	if v := q.Get("device_id"); v != "" {
		deviceID = &v
	}
	out := []remoteSession{}
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT rs.id::text, rs.device_id::text, COALESCE(d.name, d.reported_id, ''),
			       rs.actor, rs.kind, rs.scheme, rs.port, rs.created_at, rs.launched_at,
			       rs.last_activity_at, rs.expires_at, rs.ended_at, rs.end_reason,
			       rs.bytes_in, rs.bytes_out
			  FROM remote_session rs
			  JOIN devices d ON d.id = rs.device_id
			 WHERE ($1::uuid IS NULL OR rs.device_id = $1::uuid)
			   AND (NOT $2 OR rs.ended_at IS NULL)
			 ORDER BY rs.created_at DESC
			 LIMIT 100`, deviceID, activeOnly)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s remoteSession
			if err := rows.Scan(&s.ID, &s.DeviceID, &s.DeviceName, &s.Actor, &s.Kind, &s.Scheme, &s.Port,
				&s.CreatedAt, &s.LaunchedAt, &s.LastActivityAt, &s.ExpiresAt, &s.EndedAt, &s.EndReason,
				&s.BytesIn, &s.BytesOut); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// EndRemoteSession — DELETE /api/v1/remote-sessions/{id}
//
// Ends a session at once: its address stops working and the end is
// audited with who ended it.
func (h *Handler) EndRemoteSession(w http.ResponseWriter, r *http.Request) {
	if !h.remote.Enabled() {
		writeErr(w, http.StatusServiceUnavailable, "remote access isn't set up on this server")
		return
	}
	p, _ := portalauth.From(r.Context())
	id := r.PathValue("id")
	var found bool
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT true FROM remote_session rs JOIN devices d ON d.id = rs.device_id
			 WHERE rs.id = $1`, id).Scan(&found)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}) {
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "session not found")
		return
	}
	h.remote.End(r.Context(), id, "ended by "+p.ActorLabel())
	w.WriteHeader(http.StatusNoContent)
}
