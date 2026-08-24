package portalapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/registration"
	"github.com/jackc/pgx/v5"
)

// Collector pre-provisioning + enrollment tokens.
//
// Flow:
//
//   1. Portal user with collector.crud calls POST /api/v1/collectors with
//      {name, building_id, bridge_collector_id?}. Cloud creates the
//      collectors row (delegates to registration.Register — same helper
//      the legacy /admin/collectors endpoint uses), then mints a
//      collector_enrollment_tokens row bound to the new collector's id.
//      Response: {id, bridge_collector_id, enrollment_token, expires_at}.
//
//   2. Portal shows the token + a one-liner the engineer runs on the
//      target Linux box. That box calls POST /public/collectors/enroll
//      (see publicapi) which redeems the token and returns the HMAC
//      secret + cloud URL. The install script writes those to
//      /etc/av-bridge/env and starts the systemd service.
//
//   3. Bridge phones home; last_seen_at populates; the portal's
//      /collectors row lights up green.
//
// Re-mint: if the token is lost between step 1 and step 3, the vendor
// hits POST /api/v1/collectors/{id}/enrollment-token to get a fresh one
// bound to the same collector (which keeps its stable id + HMAC secret
// — nothing about the collector row changes).

// enrollmentTTL — 7 days by default. Longer than the reset/magic-link
// tokens because on-boarding a physical box has real-world lead time
// (shipping, site access, engineer schedules). Vendor can always
// re-mint.
const enrollmentTTL = 7 * 24 * time.Hour

type createCollectorReq struct {
	Name string `json:"name"`
	// BuildingID is optional — an unplaced collector can be tied to a
	// building later via the standard collector edit path. Nullable in
	// the DB (see 0001_schema.sql).
	BuildingID *string `json:"building_id"`
	// BridgeCollectorID is the stable text id the on-prem bridge
	// reports. Optional on this endpoint — if empty, we mint one from
	// a slug of the name + short random suffix. Kept optional so a
	// customer admin can just type a name and go; ops with a naming
	// convention can supply their own.
	BridgeCollectorID string `json:"bridge_collector_id"`
}

type createCollectorResp struct {
	ID                string `json:"id"`
	BridgeCollectorID string `json:"bridge_collector_id"`
	EnrollmentToken   string `json:"enrollment_token"`
	ExpiresAt         string `json:"expires_at"`
}

// CreateCollector — POST /api/v1/collectors
//
// collector.crud gated. Vendor cross-tenant editing rides the standard
// vendor-bypass + X-Customer-Scope path.
func (h *Handler) CreateCollector(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	var req createCollectorReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.Name) > 200 {
		writeErr(w, http.StatusBadRequest, "name is too long")
		return
	}

	// Default bridge_collector_id derived from name + short random tail
	// so an admin who doesn't care can save without picking one. The
	// bridge itself uses this string as its stable identity in
	// /bridge/poll payloads, so it needs to be URL-safe + unique.
	req.BridgeCollectorID = strings.TrimSpace(req.BridgeCollectorID)
	if req.BridgeCollectorID == "" {
		req.BridgeCollectorID = defaultBridgeCollectorID(req.Name)
	} else if !bridgeCollectorIDValid(req.BridgeCollectorID) {
		writeErr(w, http.StatusBadRequest,
			"bridge_collector_id must be lowercase alphanumerics + '-' / '_', 3-64 chars")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Delegate the actual collector INSERT + HMAC secret generation to
	// the existing registration package — the /admin/collectors handler
	// uses the same helper, so the on-wire shape and encryption path
	// stay consistent.
	regResult, err := registration.Register(ctx, h.store.AdminPool(), h.cipher, registration.Request{
		CustomerID:        p.CustomerID,
		BuildingID:        req.BuildingID,
		Name:              req.Name,
		BridgeCollectorID: req.BridgeCollectorID,
	})
	if err != nil {
		switch {
		case errors.Is(err, registration.ErrCustomerUnknown):
			writeErr(w, http.StatusBadRequest, "unknown customer")
		case errors.Is(err, registration.ErrAlreadyExists):
			writeErr(w, http.StatusConflict, "bridge_collector_id already in use")
		default:
			h.log.Error("create collector: register failed", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	// Discard the plaintext HMAC secret registration returned — the
	// enrollment token is what the engineer will hand to the cloud on
	// first-run, and the cloud returns the secret at that point (from
	// the encrypted row, decrypted just-in-time). The vendor never
	// needs the raw secret. See publicapi/collectors_enroll.go.
	regResult.HMACSecret = ""

	token, expiresAt, err := h.mintEnrollmentToken(ctx, regResult.ID, p, r)
	if err != nil {
		h.log.Error("create collector: mint token failed",
			"collector_id", regResult.ID, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Audit — the create is per-customer so a normal audit entry fits.
	_ = h.store.WithTenantScoped(r.Context(), p.CustomerID, principalScope(p), func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "collector.create",
			TargetKind: "collector", TargetID: regResult.ID,
			After: mustJSON(map[string]any{
				"name":                req.Name,
				"bridge_collector_id": req.BridgeCollectorID,
			}),
		}))
	})

	writeJSON(w, http.StatusCreated, createCollectorResp{
		ID:                regResult.ID,
		BridgeCollectorID: regResult.BridgeCollectorID,
		EnrollmentToken:   token,
		ExpiresAt:         expiresAt.UTC().Format(time.RFC3339),
	})
}

// ReissueCollectorEnrollmentToken — POST /api/v1/collectors/{id}/enrollment-token
//
// Fresh unused unexpired token for an existing collector. Doesn't
// change the collector row (id / HMAC secret stay stable). Useful when
// the original token was lost or expired before the engineer got to
// site. Older unused tokens for the same collector aren't invalidated
// here — a single-use redeem naturally makes them dead weight — but if
// we wanted zero risk we could wipe them; skipping that keeps the code
// smaller and matches the M4.1 magic-link pattern.
func (h *Handler) ReissueCollectorEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Confirm the collector belongs to the caller's tenant before we
	// mint. Vendor callers with X-Customer-Scope pass through because
	// p.CustomerID reflects the acted-as customer.
	var found int
	if err := h.store.AdminPool().QueryRow(ctx,
		`SELECT count(*) FROM collectors WHERE id = $1 AND customer_id = $2`,
		id, p.CustomerID).Scan(&found); err != nil {
		h.log.Error("reissue enrollment: lookup", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if found == 0 {
		writeErr(w, http.StatusNotFound, "collector not found")
		return
	}

	token, expiresAt, err := h.mintEnrollmentToken(ctx, id, p, r)
	if err != nil {
		h.log.Error("reissue enrollment: mint", "collector_id", id, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	_ = h.store.WithTenantScoped(r.Context(), p.CustomerID, principalScope(p), func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "collector.enrollment_token_reissued",
			TargetKind: "collector", TargetID: id,
		}))
	})

	writeJSON(w, http.StatusCreated, map[string]string{
		"enrollment_token": token,
		"expires_at":       expiresAt.UTC().Format(time.RFC3339),
	})
}

// mintEnrollmentToken inserts a new SHA-256 row and returns the raw
// token + expiry. Shared by create + reissue.
func (h *Handler) mintEnrollmentToken(ctx context.Context, collectorID string, p portalauth.Principal, r *http.Request) (string, time.Time, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("random: %w", err)
	}
	raw := hex.EncodeToString(b[:])
	expiresAt := time.Now().Add(enrollmentTTL)
	ua := r.Header.Get("User-Agent")
	if len(ua) > 500 {
		ua = ua[:500]
	}
	if _, err := h.store.AdminPool().Exec(ctx, `
		INSERT INTO collector_enrollment_tokens
		    (collector_id, token_hash, expires_at, issued_by, requester_ip, user_agent)
		VALUES ($1, $2, $3, NULLIF($4::text,'')::uuid, $5, $6)`,
		collectorID, portalauth.HashToken(raw), expiresAt,
		p.UserID, clientIP(r), ua); err != nil {
		return "", time.Time{}, fmt.Errorf("insert: %w", err)
	}
	return raw, expiresAt, nil
}

// defaultBridgeCollectorID composes a plausible id from a name plus a
// short random suffix. Handles the common case where the admin doesn't
// care what the on-wire id is. Truncated to a manageable length; the
// random tail keeps collisions rare across tenants.
func defaultBridgeCollectorID(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	// Replace anything non-alphanumeric with '-'. Keep it URL-ish.
	var b strings.Builder
	prevDash := false
	for _, c := range slug {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	root := strings.Trim(b.String(), "-")
	if len(root) > 32 {
		root = root[:32]
	}
	if root == "" {
		root = "collector"
	}
	var suf [4]byte
	_, _ = rand.Read(suf[:])
	return root + "-" + hex.EncodeToString(suf[:])
}

// DeleteCollector — DELETE /api/v1/collectors/{id}
//
// Refuses if any live devices (deleted_at IS NULL) still reference this
// collector — the schema has ON DELETE CASCADE all the way down through
// devices → telemetry/events/commands/alerts, so a raw delete would
// silently wipe a lot of history. Soft-deleted device rows don't block
// (they're already tombstoned from the user's point of view; letting the
// cascade take them out is the whole point of "delete forever").
func (h *Handler) DeleteCollector(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// One round-trip: confirm the collector belongs to this tenant AND
	// count its live devices. count > 0 → 409, count = 0 (or NULL row)
	// → we know it's safe to delete OR that the row doesn't exist.
	var (
		exists     bool
		liveDevices int
		name       string
	)
	err := h.store.AdminPool().QueryRow(ctx, `
		SELECT c.name,
		       (SELECT count(*) FROM devices d
		         WHERE d.collector_id = c.id
		           AND d.deleted_at IS NULL)::int
		  FROM collectors c
		 WHERE c.id = $1 AND c.customer_id = $2`,
		id, p.CustomerID,
	).Scan(&name, &liveDevices)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "collector not found")
			return
		}
		h.log.Error("delete collector: lookup", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	exists = true
	_ = exists

	if liveDevices > 0 {
		writeErr(w, http.StatusConflict, fmt.Sprintf(
			"collector still has %d device(s) — delete or move them first, then retry",
			liveDevices,
		))
		return
	}

	tag, err := h.store.AdminPool().Exec(ctx,
		`DELETE FROM collectors WHERE id = $1 AND customer_id = $2`,
		id, p.CustomerID)
	if err != nil {
		h.log.Error("delete collector: exec", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusNotFound, "collector not found")
		return
	}

	_ = h.store.WithTenantScoped(r.Context(), p.CustomerID, principalScope(p), func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "collector.delete",
			TargetKind: "collector", TargetID: id,
			Before: mustJSON(map[string]any{"name": name}),
		}))
	})
	w.WriteHeader(http.StatusNoContent)
}

// bridgeCollectorIDValid enforces the same shape defaultBridgeCollectorID
// produces so a caller who supplies their own id can't sneak whitespace
// or slashes through to the bridge's persistent identifier.
func bridgeCollectorIDValid(s string) bool {
	if len(s) < 3 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}
