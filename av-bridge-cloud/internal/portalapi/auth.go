package portalapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// Absolute session lifetime. No rolling refresh — a user logs in once a day.
// Short enough that a leaked token has a bounded blast radius; long enough
// that operators aren't re-authenticating every hour on shift.
const sessionTTL = 24 * time.Hour

// Bcrypt cost. Default cost of 10 balances CPU (~65 ms/hash on a 2020 laptop)
// against brute-force resistance. Bump to 12 when moving off dev hardware.
const bcryptCost = 10

// mintToken returns a `av_<64-hex>` opaque session token. 32 bytes of
// crypto/rand ⇒ 256 bits of entropy; the prefix lets ChainResolver route
// quickly without a DB round-trip on the wrong tokens.
func mintToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return portalauth.LocalTokenPrefix + hex.EncodeToString(b[:]), nil
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Login — POST /api/v1/auth/login
//
// Unauthenticated. Verifies email+password against users, opens a session,
// returns the token. On any failure returns a generic 401 so probes can't
// distinguish "unknown user" from "wrong password".
//
// Deliberately runs against the admin pool — the caller has no Principal
// yet, so RLS isn't in play. Handler is registered outside the auth
// middleware chain in server.go.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "email and password are required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var (
		userID     string
		hash       string
		role       string
		disabledAt *time.Time
	)
	err := h.store.AdminPool().QueryRow(ctx, `
		SELECT id::text, password_hash, role, disabled_at
		  FROM users
		 WHERE lower(email) = $1
		 ORDER BY disabled_at NULLS FIRST
		 LIMIT 1`,
		req.Email).Scan(&userID, &hash, &role, &disabledAt)
	if err != nil || disabledAt != nil {
		// bcrypt.CompareHashAndPassword is expensive (~65 ms). Running it
		// even on the miss path keeps timing between "unknown user" and
		// "wrong password" comparable.
		_ = bcrypt.CompareHashAndPassword(
			[]byte("$2a$10$abcdefghijklmnopqrstuvCK0T7XZgB/CmKgQGXqZ7d9dbP7l1lIym"),
			[]byte(req.Password))
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token, err := mintToken()
	if err != nil {
		h.log.Error("login: token mint", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	ua := r.Header.Get("User-Agent")
	if len(ua) > 500 {
		ua = ua[:500]
	}
	if _, err := h.store.AdminPool().Exec(ctx, `
		INSERT INTO user_sessions (user_id, token_hash, expires_at, user_agent, ip_address)
		VALUES ($1, $2, now() + $3::interval, $4, $5)`,
		userID, portalauth.HashToken(token),
		sessionTTL.String(), ua, clientIP(r)); err != nil {
		h.log.Error("login: session insert", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Best-effort last_login_at bump — failure here doesn't fail the login.
	if _, err := h.store.AdminPool().Exec(ctx,
		`UPDATE users SET last_login_at = now() WHERE id = $1`, userID); err != nil {
		h.log.Warn("login: last_login_at update failed", "user_id", userID, "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_at": time.Now().Add(sessionTTL).UTC().Format(time.RFC3339),
		"role":       role,
	})
}

// Logout — POST /api/v1/auth/logout
//
// Revokes the caller's current session. Uses the bearer token from the
// request (not the Principal) because we need the raw token to hash + look
// up the row. Idempotent — a token that's already revoked / expired returns
// 204 anyway.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	token := extractBearer(r)
	if token == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	_, _ = h.store.AdminPool().Exec(ctx,
		`UPDATE user_sessions SET revoked_at = now()
		  WHERE token_hash = $1 AND revoked_at IS NULL`,
		portalauth.HashToken(token))
	w.WriteHeader(http.StatusNoContent)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword — POST /api/v1/auth/change-password
//
// Self-service: the caller must know their current password. Revokes all
// existing sessions on success so a compromised session can't outlive the
// rotation. Caller then re-logs in with the new password.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	p, ok := portalauth.From(r.Context())
	if !ok || p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "no principal")
		return
	}
	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		writeErr(w, http.StatusBadRequest, "current_password and new_password are required")
		return
	}
	if len(req.NewPassword) < 12 {
		writeErr(w, http.StatusBadRequest, "new password must be at least 12 characters")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var currentHash string
	if err := h.store.AdminPool().QueryRow(ctx,
		`SELECT password_hash FROM users WHERE id = $1`, p.UserID).Scan(&currentHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusUnauthorized, "user not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(req.CurrentPassword)); err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid current password")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcryptCost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if _, err := h.store.AdminPool().Exec(ctx,
		`UPDATE users SET password_hash = $1 WHERE id = $2`, string(newHash), p.UserID); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Revoke every existing session so any leaked token is invalidated.
	// The caller must sign in again after this.
	_, _ = h.store.AdminPool().Exec(ctx,
		`UPDATE user_sessions SET revoked_at = now()
		  WHERE user_id = $1 AND revoked_at IS NULL`, p.UserID)

	w.WriteHeader(http.StatusNoContent)
}

// extractBearer duplicates the middleware's header parsing so Logout can
// find the token without a Principal (the caller may already be expired).
func extractBearer(r *http.Request) string {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, prefix) {
		return strings.TrimPrefix(auth, prefix)
	}
	return r.URL.Query().Get("token")
}

// clientIP is a small helper for session provenance. Trusts X-Forwarded-For
// last-hop only when set — the cloud sits behind a compose network for now,
// so any header is either from the local reverse proxy or absent.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.LastIndex(xff, ","); idx >= 0 {
			return strings.TrimSpace(xff[idx+1:])
		}
		return strings.TrimSpace(xff)
	}
	return r.RemoteAddr
}
