// Package bridgeauth is the shared HMAC + collector-lookup auth path used by
// every bridge-facing endpoint (poll, post-result, config-pull, future
// config-push). Centralised so the bridge has exactly one auth posture to
// maintain — sign the body bytes with the collector's secret, put
// "sha256=<hex>" in X-Signature, put collector_id in the body.
package bridgeauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/dloomes/av-bridge-cloud/internal/auth"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/jackc/pgx/v5"
)

// MaxBody caps the request body — bridge calls are auth tokens + small JSON
// payloads, never streaming data. 4 MiB is plenty for a few hundred devices'
// worth of config and command results.
const MaxBody = 4 << 20

// Authenticator resolves a bridge request to a Collector via HMAC. Holds the
// admin-pool store (for the cross-tenant collector lookup) and the cipher
// (for decrypting the stored secret).
type Authenticator struct {
	store  *db.Store
	cipher secrets.Cipher
	log    *slog.Logger
}

func New(store *db.Store, cipher secrets.Cipher, log *slog.Logger) *Authenticator {
	return &Authenticator{store: store, cipher: cipher, log: log}
}

// Authenticate reads the request body (within MaxBody), parses collector_id,
// looks up the collector, verifies X-Signature against the stored secret.
// Returns the raw body for the caller to parse further, the resolved
// collector, and ok=false (response already written) on any failure.
func (a *Authenticator) Authenticate(w http.ResponseWriter, r *http.Request) ([]byte, *db.Collector, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody))
	if err != nil {
		WriteErr(w, http.StatusBadRequest, "could not read body")
		return nil, nil, false
	}

	var idOnly struct {
		CollectorID string `json:"collector_id"`
	}
	if err := json.Unmarshal(body, &idOnly); err != nil || idOnly.CollectorID == "" {
		WriteErr(w, http.StatusBadRequest, "collector_id is required")
		return nil, nil, false
	}

	col, err := a.store.LookupCollectorByBridgeID(r.Context(), idOnly.CollectorID)
	if errors.Is(err, pgx.ErrNoRows) {
		WriteErr(w, http.StatusUnauthorized, "authentication failed")
		return nil, nil, false
	} else if err != nil {
		a.log.Error("collector lookup failed", "error", err)
		WriteErr(w, http.StatusInternalServerError, "internal error")
		return nil, nil, false
	}

	secret, err := a.cipher.Decrypt(col.SecretEnc)
	if err != nil {
		a.log.Error("secret decrypt failed", "collector", col.ID, "error", err)
		WriteErr(w, http.StatusInternalServerError, "internal error")
		return nil, nil, false
	}
	if !auth.VerifySignature(string(secret), r.Header.Get("X-Signature"), body) {
		WriteErr(w, http.StatusUnauthorized, "authentication failed")
		return nil, nil, false
	}
	// Stamp last-seen so portal can render real connection health. Failure
	// here is non-fatal — log and continue; the bridge call itself succeeded.
	if err := a.store.TouchCollector(r.Context(), col.ID); err != nil {
		a.log.Warn("touch collector failed", "collector", col.ID, "error", err)
	}
	return body, &col, true
}

// WriteJSON / WriteErr are exported so handlers that already authenticate
// here can use a consistent response shape without re-importing encoding/json.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteErr(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// tunnelSkew is how far a tunnel sign-in's timestamp may be from now.
const tunnelSkew = 5 * time.Minute

// AuthenticateTunnel checks a collector's remote-access tunnel sign-in. The
// tunnel is a websocket (a GET, with no body to sign), so the collector
// signs "tunnel:<collector id>:<unix time>" instead, sent as X-Collector-ID,
// X-Timestamp and X-Signature; a stamp more than tunnelSkew away is
// refused, so a captured sign-in can't be replayed later. Writes the error
// response itself when it returns ok=false.
func (a *Authenticator) AuthenticateTunnel(w http.ResponseWriter, r *http.Request) (*db.Collector, bool) {
	id := r.Header.Get("X-Collector-ID")
	ts, err := strconv.ParseInt(r.Header.Get("X-Timestamp"), 10, 64)
	if id == "" || err != nil {
		WriteErr(w, http.StatusBadRequest, "X-Collector-ID and X-Timestamp are required")
		return nil, false
	}
	if d := time.Since(time.Unix(ts, 0)); d > tunnelSkew || d < -tunnelSkew {
		WriteErr(w, http.StatusUnauthorized, "authentication failed (clock skew)")
		return nil, false
	}
	col, err := a.store.LookupCollectorByBridgeID(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		WriteErr(w, http.StatusUnauthorized, "authentication failed")
		return nil, false
	} else if err != nil {
		a.log.Error("collector lookup failed", "error", err)
		WriteErr(w, http.StatusInternalServerError, "internal error")
		return nil, false
	}
	secret, err := a.cipher.Decrypt(col.SecretEnc)
	if err != nil {
		a.log.Error("secret decrypt failed", "collector", col.ID, "error", err)
		WriteErr(w, http.StatusInternalServerError, "internal error")
		return nil, false
	}
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "tunnel:%s:%d", id, ts)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-Signature"))) {
		WriteErr(w, http.StatusUnauthorized, "authentication failed")
		return nil, false
	}
	return &col, true
}
