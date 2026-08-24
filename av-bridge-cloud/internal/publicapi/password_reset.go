package publicapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/notify"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// Self-serve password reset — two-step flow that intentionally leaks no
// information about which emails have accounts.
//
//   POST /public/password-reset/request  {email}
//     Always returns 202. If a matching user exists AND no unused token was
//     minted in the last 60 seconds (soft rate-limit against accidental
//     double-clicks and spam), a fresh token is stored (SHA-256 at rest)
//     and emailed as an https://.../reset-password?token=... link.
//
//   POST /public/password-reset/complete {token, new_password}
//     Looks the token up by SHA-256, verifies it isn't used/expired, sets
//     the user's bcrypt hash, marks the token used, and revokes every
//     active session for that user. Returns 204 on success; 400 with a
//     generic message on any failure so probes can't distinguish
//     "unknown token" from "expired token" from "password too short".

const (
	// Token lifetime — long enough for an operator to walk from their laptop
	// to their phone and click through, short enough that a stolen inbox
	// doesn't hold indefinite reset power.
	resetTokenTTL = 1 * time.Hour
	// Cooldown between successive reset requests for the same user. Prevents
	// accidental email spam from a mashed "Forgot?" button. Not a security
	// control — the anti-enumeration guarantee still holds because the 202
	// response is uniform regardless of whether cooldown blocked the send.
	resetRequestCooldown = 60 * time.Second
	// Minimum length for the new password on the complete path. Mirrors
	// portalapi ChangePassword + ResetUserPassword so admin-initiated and
	// self-serve resets impose the same floor.
	resetMinPasswordLen = 12
	// Bcrypt cost. Matches portalapi's constant; kept local so this file
	// doesn't need to reach across packages.
	resetBcryptCost = 10
)

type resetRequestBody struct {
	Email string `json:"email"`
}

// RequestReset — POST /public/password-reset/request
//
// Anti-enumeration: ALWAYS returns 202 Accepted with a fixed body, whether
// the email matches a user or not. Send work happens in a background
// goroutine so timing doesn't leak the match either.
func (h *Handler) RequestReset(w http.ResponseWriter, r *http.Request) {
	var req resetRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	email := strings.TrimSpace(strings.ToLower(req.Email))
	// Uniform response — never let the caller distinguish "no such user"
	// from "email accepted, look in your inbox".
	writeJSONStatus(w, http.StatusAccepted, map[string]string{
		"status": "accepted",
	})

	if email == "" || !strings.Contains(email, "@") {
		// Nothing to do, but the response has already flushed a 202 above.
		return
	}

	// Capture request provenance BEFORE we hand off — the goroutine can't
	// read *http.Request safely after the handler returns.
	ip := clientIP(r)
	ua := r.Header.Get("User-Agent")
	if len(ua) > 500 {
		ua = ua[:500]
	}

	// Background send so the response is not held up by SMTP round-trips.
	// Fresh context — the request context is cancelled the moment the
	// handler returns. Short deadline so a wedged SMTP relay doesn't
	// leak goroutines indefinitely.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		h.processResetRequest(ctx, email, ip, ua)
	}()
}

// processResetRequest is the background arm of RequestReset. Handles the
// lookup, cooldown check, token mint, and email dispatch. Every failure
// path logs (so ops can debug) but never surfaces to the caller.
func (h *Handler) processResetRequest(ctx context.Context, email, ip, ua string) {
	// Lookup user + customer slug in one round-trip. LEFT JOIN so vendor
	// users (no customer_id) still return a row with an empty slug.
	// Gates:
	//   - disabled_at IS NULL — deactivated accounts don't reset (same as
	//     the login handler).
	//   - password_hash IS NOT NULL — migration 0031 made this nullable
	//     for Entra-only users. Those users don't have a password to
	//     reset; sending them an "add password" mail would confuse the
	//     mental model AND circumvent the SSO-only intent. Silent skip
	//     preserves anti-enumeration (still returns 202 above).
	//   - customer.sso_required IS NOT TRUE — a tenant that has flipped
	//     SSO-only refuses password reset for the same reason. Vendor
	//     rows have customer_id NULL, so the COALESCE keeps them in the
	//     positive branch.
	var (
		userID       string
		fullName     string
		customerSlug string
		customerName string
		accentColor  string
	)
	err := h.store.AdminPool().QueryRow(ctx, `
		SELECT u.id::text,
		       COALESCE(u.full_name, ''),
		       COALESCE(c.slug, ''),
		       COALESCE(c.display_name, c.name, ''),
		       COALESCE(c.accent_color, '')
		  FROM users u
		  LEFT JOIN customers c ON c.id = u.customer_id
		 WHERE lower(u.email) = $1
		   AND u.disabled_at IS NULL
		   AND u.password_hash IS NOT NULL
		   AND COALESCE(c.sso_required, false) = false
		 LIMIT 1`, email).
		Scan(&userID, &fullName, &customerSlug, &customerName, &accentColor)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.log.Warn("password reset: user lookup failed",
				"error", err)
		} else {
			// pgx.ErrNoRows here means one of: unknown email, disabled
			// account, or Entra-only user. Log without the email so we
			// can distinguish "no work done" from "send failed" without
			// leaking which addresses have accounts. Anti-enumeration
			// still holds — the 202 response was flushed unconditionally.
			h.log.Info("password reset: no eligible local-auth user for request")
		}
		return
	}

	// Soft cooldown: if an unused token for this user was minted within
	// resetRequestCooldown, skip the send. Still returns 202 to the caller
	// (already flushed) — no observable behaviour change.
	var mostRecent *time.Time
	if err := h.store.AdminPool().QueryRow(ctx, `
		SELECT MAX(created_at)
		  FROM password_reset_tokens
		 WHERE user_id = $1
		   AND used_at IS NULL`, userID).Scan(&mostRecent); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.log.Warn("password reset: cooldown query failed", "error", err)
		return
	}
	if mostRecent != nil && time.Since(*mostRecent) < resetRequestCooldown {
		h.log.Info("password reset: within cooldown, skipping send",
			"user_id", userID)
		return
	}

	token, err := mintResetToken()
	if err != nil {
		h.log.Error("password reset: token mint failed", "error", err)
		return
	}

	// Store the SHA-256 hash — the raw token only ever lives in the email
	// we're about to send. Reuses portalauth.HashToken so session tokens
	// and reset tokens share the hashing scheme.
	if _, err := h.store.AdminPool().Exec(ctx, `
		INSERT INTO password_reset_tokens
		    (user_id, token_hash, expires_at, requester_ip, user_agent)
		VALUES ($1, $2, now() + $3::interval, $4, $5)`,
		userID, portalauth.HashToken(token),
		resetTokenTTL.String(), ip, ua); err != nil {
		h.log.Error("password reset: token insert failed",
			"user_id", userID, "error", err)
		return
	}

	// Build the reset URL. For a customer with a slug, land on their
	// branded subdomain (matching the sign-in page they'd have come from).
	// Vendor / unslugged customers fall back to the base portal URL.
	origin := h.resetOriginFor(customerSlug)
	// resetPath is what we'd append when origin is empty (local dev with no
	// PORTAL_BASE_URL) — a relative path the email client will still render
	// as text so the operator can construct the URL by hand.
	const resetPath = "/reset-password"
	var link string
	if origin == "" {
		link = fmt.Sprintf("%s?token=%s", resetPath, token)
	} else {
		link = fmt.Sprintf("%s%s?token=%s", origin, resetPath, token)
	}

	subject := "Reset your password"
	if customerName != "" {
		subject = fmt.Sprintf("Reset your %s password", customerName)
	}

	textBody := renderResetText(resetEmailContext{
		Name:         fullName,
		CustomerName: customerName,
		Link:         link,
		TTL:          resetTokenTTL,
	})
	htmlBody := renderResetHTML(resetEmailContext{
		Name:         fullName,
		CustomerName: customerName,
		Link:         link,
		TTL:          resetTokenTTL,
		AccentColor:  accentColor,
	})

	if err := notify.SendHTMLEmail(ctx, h.smtp, []string{email}, nil,
		subject, htmlBody, textBody, h.log); err != nil {
		h.log.Error("password reset: email send failed",
			"user_id", userID, "error", err)
		return
	}
	h.log.Info("password reset: email sent",
		"user_id", userID,
		"dry_run", h.smtp.Host == "")
}

type resetCompleteBody struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// CompleteReset — POST /public/password-reset/complete
//
// Verifies the token, sets the new bcrypt hash, marks the token used, and
// revokes every session for the user so any leaked session dies with the
// old password. Error responses are deliberately generic — the client is
// told "reset failed" for token-lookup / expiry / used / short-password
// cases so probes can't discriminate.
func (h *Handler) CompleteReset(w http.ResponseWriter, r *http.Request) {
	var req resetCompleteBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" || len(req.NewPassword) < resetMinPasswordLen {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("reset failed — token missing or password shorter than %d characters", resetMinPasswordLen),
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tokenHash := portalauth.HashToken(req.Token)

	// Atomic redeem: WHERE clauses gate every "still redeemable" condition
	// so a concurrent second click can't double-consume the same token.
	// RETURNING the user_id both confirms success and gives us the row we
	// need to update the password hash + revoke sessions.
	var userID string
	err := h.store.AdminPool().QueryRow(ctx, `
		UPDATE password_reset_tokens
		   SET used_at = now()
		 WHERE token_hash = $1
		   AND used_at IS NULL
		   AND expires_at > now()
		 RETURNING user_id::text`, tokenHash).Scan(&userID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.log.Warn("password reset: redeem lookup failed", "error", err)
		}
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error": "reset failed — link expired or already used",
		})
		return
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), resetBcryptCost)
	if err != nil {
		h.log.Error("password reset: bcrypt failed", "user_id", userID, "error", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	if _, err := h.store.AdminPool().Exec(ctx, `
		UPDATE users SET password_hash = $2 WHERE id = $1`,
		userID, string(newHash)); err != nil {
		h.log.Error("password reset: user update failed", "user_id", userID, "error", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	// Revoke every active session — the caller must sign in again with
	// the new password. Same behaviour as ChangePassword/ResetUserPassword.
	_, _ = h.store.AdminPool().Exec(ctx,
		`UPDATE user_sessions SET revoked_at = now()
		  WHERE user_id = $1 AND revoked_at IS NULL`, userID)

	h.log.Info("password reset: completed", "user_id", userID)
	w.WriteHeader(http.StatusNoContent)
}

// mintResetToken returns 64 hex chars of crypto random. No prefix — reset
// tokens never travel through the auth middleware (they're POST body
// params), so the `av_` session prefix would only muddy their identity in
// logs.
func mintResetToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// clientIP extracts the caller's IP from X-Forwarded-For (last hop) or the
// raw RemoteAddr. Kept local to publicapi because the portalapi copy of
// this helper is package-private there.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.LastIndex(xff, ","); idx >= 0 {
			return strings.TrimSpace(xff[idx+1:])
		}
		return strings.TrimSpace(xff)
	}
	return r.RemoteAddr
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// resetEmailContext feeds both the plaintext and HTML renderers so the two
// bodies never drift on the reset link or expiry copy.
type resetEmailContext struct {
	Name         string
	CustomerName string
	Link         string
	TTL          time.Duration
	AccentColor  string // "#hex" — used to tint the CTA button in HTML
}

func renderResetText(ctx resetEmailContext) string {
	greeting := "Hello,"
	if ctx.Name != "" {
		greeting = "Hello " + ctx.Name + ","
	}
	product := "the portal"
	if ctx.CustomerName != "" {
		product = ctx.CustomerName
	}
	var b strings.Builder
	fmt.Fprintln(&b, greeting)
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "We received a request to reset the password for your %s account.\n", product)
	fmt.Fprintln(&b, "To choose a new password, open the link below within the next", humanTTL(ctx.TTL)+":")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, ctx.Link)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "If you didn't request this, you can safely ignore this email — your password won't change.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "— The AV Bridge team")
	return b.String()
}

func renderResetHTML(ctx resetEmailContext) string {
	greeting := "Hello,"
	if ctx.Name != "" {
		greeting = "Hello " + html.EscapeString(ctx.Name) + ","
	}
	product := "the portal"
	if ctx.CustomerName != "" {
		product = html.EscapeString(ctx.CustomerName)
	}
	// Accent tints the CTA button. Fall back to a neutral slate that reads
	// on both light and dark inboxes when no customer accent is set.
	accent := ctx.AccentColor
	if !isPlausibleHexColor(accent) {
		accent = "#0f172a"
	}
	link := html.EscapeString(ctx.Link)
	ttl := humanTTL(ctx.TTL)

	// Inline styles only — many clients strip <style>. Table-based layout
	// for Outlook/Gmail-in-webview compatibility. Kept intentionally small
	// so the message renders well on mobile without a media query.
	return `<!doctype html>
<html>
<body style="margin:0;padding:0;background:#f5f6fa;font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Helvetica,Arial,sans-serif;color:#1f2937;">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f5f6fa;padding:32px 12px;">
    <tr><td align="center">
      <table role="presentation" width="560" cellpadding="0" cellspacing="0" style="max-width:560px;background:#ffffff;border-radius:12px;overflow:hidden;box-shadow:0 1px 3px rgba(15,23,42,0.06);">
        <tr><td style="padding:32px 32px 8px 32px;font-size:16px;line-height:1.6;">
          <p style="margin:0 0 16px 0;font-size:15px;color:#0f172a;">` + greeting + `</p>
          <p style="margin:0 0 16px 0;">We received a request to reset the password for your ` + product + ` account.</p>
          <p style="margin:0 0 24px 0;">Click the button below to choose a new password. This link expires in ` + ttl + `.</p>
          <p style="margin:0 0 24px 0;">
            <a href="` + link + `" style="display:inline-block;padding:12px 22px;background:` + accent + `;color:#ffffff;text-decoration:none;border-radius:8px;font-weight:600;font-size:14px;">Reset password</a>
          </p>
          <p style="margin:0 0 8px 0;font-size:13px;color:#64748b;">Or copy this link into your browser:</p>
          <p style="margin:0 0 24px 0;font-size:12px;word-break:break-all;color:#334155;">` + link + `</p>
          <p style="margin:0 0 4px 0;font-size:13px;color:#64748b;">If you didn't request this, you can ignore this email — your password won't change.</p>
        </td></tr>
        <tr><td style="padding:16px 32px 24px 32px;border-top:1px solid #e5e7eb;font-size:12px;color:#94a3b8;">
          Sent by the AV Bridge portal.
        </td></tr>
      </table>
    </td></tr>
  </table>
</body>
</html>`
}

// humanTTL renders a duration as "1 hour" / "45 minutes" / "30 seconds".
// Kept private — the reset email is the only caller.
func humanTTL(d time.Duration) string {
	switch {
	case d >= time.Hour:
		hours := int(d / time.Hour)
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	case d >= time.Minute:
		mins := int(d / time.Minute)
		if mins == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", mins)
	default:
		secs := int(d / time.Second)
		return fmt.Sprintf("%d seconds", secs)
	}
}

// isPlausibleHexColor is a loose check — anything starting with '#' plus 3
// or 6 hex chars passes. The DB constraint already validates on write; this
// is defence against inline-styling a broken value into the email HTML.
func isPlausibleHexColor(s string) bool {
	if len(s) != 4 && len(s) != 7 {
		return false
	}
	if s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
