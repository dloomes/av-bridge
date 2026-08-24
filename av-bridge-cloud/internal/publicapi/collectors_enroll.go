package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// POST /public/collectors/enroll — on-site collector redeems its
// enrollment token and receives the credentials the bridge needs to
// phone home. Called by the install script (curl one-liner or Ansible
// task), NOT by a human directly. Runs unauthenticated because the
// token IS the auth material — same reasoning as password-reset
// consume and magic-link consume.

type enrollRequest struct {
	Token string `json:"token"`
	// UserAgent + Hostname are optional identity hints so the audit
	// log has a bit of on-site context beyond "some IP hit /enroll".
	Hostname string `json:"hostname"`
}

type enrollResponse struct {
	CollectorID       string `json:"collector_id"`
	BridgeCollectorID string `json:"bridge_collector_id"`
	CustomerID        string `json:"customer_id"`
	HMACSecret        string `json:"hmac_secret"`
	// CloudBaseURL is the /ingest + /bridge origin. The install script
	// writes CLOUD_WEBHOOK_URL=<CloudBaseURL>/ingest into env so the
	// bridge doesn't need to be hand-configured with our hostname.
	CloudBaseURL string `json:"cloud_base_url"`
}

// EnrollCollector — POST /public/collectors/enroll
//
// Atomic redeem: WHERE gates every "still redeemable" condition on the
// UPDATE so a concurrent second click can't double-consume. On success
// we decrypt the collector's stored HMAC secret and return the
// plaintext to the caller once; the raw value is not retained anywhere
// in the response cycle beyond this handler frame.
func (h *Handler) EnrollCollector(w http.ResponseWriter, r *http.Request) {
	var req enrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" || len(req.Token) != 64 {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid token"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Redeem + fetch collector row in one round-trip via CTE. Returning
	// clause emits the collector's id, bridge id, customer id, and
	// encrypted HMAC so we don't have to re-select after the UPDATE.
	var (
		collectorID string
		bridgeID    string
		customerID  string
		hmacEnc     []byte
	)
	err := h.store.AdminPool().QueryRow(ctx, `
		WITH consumed AS (
		    UPDATE collector_enrollment_tokens
		       SET used_at = now()
		     WHERE token_hash = $1
		       AND used_at IS NULL
		       AND expires_at > now()
		    RETURNING collector_id
		)
		SELECT c.id::text,
		       COALESCE(c.bridge_collector_id, ''),
		       c.customer_id::text,
		       c.hmac_secret_enc
		  FROM consumed
		  JOIN collectors c ON c.id = consumed.collector_id`,
		portalauth.HashToken(req.Token),
	).Scan(&collectorID, &bridgeID, &customerID, &hmacEnc)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.log.Warn("collector enroll: redeem failed", "error", err)
		}
		writeJSONStatus(w, http.StatusBadRequest,
			map[string]string{"error": "enrollment failed — token missing, expired, or already used"})
		return
	}
	if h.cipher == nil {
		h.log.Error("collector enroll: cipher not configured")
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	hmacPlaintext, err := h.cipher.Decrypt(hmacEnc)
	if err != nil {
		h.log.Error("collector enroll: hmac decrypt failed",
			"collector_id", collectorID, "error", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	// Best-effort hostname stash — captured on the token row after
	// redeem so the audit trail names the box the token was consumed
	// on. Not gating: a scrubbed row still succeeds.
	if req.Hostname != "" {
		if len(req.Hostname) > 200 {
			req.Hostname = req.Hostname[:200]
		}
		_, _ = h.store.AdminPool().Exec(ctx, `
			UPDATE collector_enrollment_tokens
			   SET user_agent = COALESCE(user_agent,'') || ' hostname=' || $2
			 WHERE token_hash = $1`,
			portalauth.HashToken(req.Token), req.Hostname)
	}

	h.log.Info("collector enrolled",
		"collector_id", collectorID,
		"bridge_collector_id", bridgeID,
		"customer_id", customerID,
		"remote", clientIP(r),
		"hostname", req.Hostname,
	)

	writeJSONStatus(w, http.StatusOK, enrollResponse{
		CollectorID:       collectorID,
		BridgeCollectorID: bridgeID,
		CustomerID:        customerID,
		HMACSecret:        string(hmacPlaintext),
		CloudBaseURL:      strings.TrimRight(h.cloudBaseURL, "/"),
	})
}
