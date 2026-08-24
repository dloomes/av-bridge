package publicapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// GET /public/magic-link/consume?token=<64-hex>  (M4.1)
//
// Redeems a vendor-issued break-glass token, mints an av_ session for
// the target user, and 302s the browser to the user's branded
// /sign-in/callback so the portal picks up the token exactly like the
// vendor Entra callback does.
//
// All failure paths render the same error page via redirect — the same
// signed-out shape the sign-in form knows how to display, so a stale
// or invalid link doesn't leak information about what the token was.

// magicSessionTTL is how long the session minted by a magic-link
// consumption lives. Deliberately shorter than the regular 24h session
// (portalapi.sessionTTL) — a break-glass link is for immediate use and
// the resulting session shouldn't outlast normal working hours. If the
// user needs longer they can re-sign-in via the normal path once inside.
const magicSessionTTL = 8 * time.Hour

// ConsumeMagicLink — GET /public/magic-link/consume?token=X
func (h *Handler) ConsumeMagicLink(w http.ResponseWriter, r *http.Request) {
	rawToken := strings.TrimSpace(r.URL.Query().Get("token"))
	if rawToken == "" {
		h.redirectMagicError(w, r, "missing_token")
		return
	}
	// Length + charset sanity — the raw form is 64 hex chars. Anything
	// else can't have been minted by us so short-circuit before the DB.
	if len(rawToken) != 64 {
		h.redirectMagicError(w, r, "invalid_token")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Atomic redeem — same shape as password reset. WHERE gates every
	// "still redeemable" invariant so a concurrent second click can't
	// double-consume.
	var userID string
	err := h.store.AdminPool().QueryRow(ctx, `
		UPDATE magic_link_tokens
		   SET used_at = now()
		 WHERE token_hash = $1
		   AND used_at IS NULL
		   AND expires_at > now()
		 RETURNING user_id::text`,
		portalauth.HashToken(rawToken)).Scan(&userID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.log.Warn("magic link redeem: lookup failed", "error", err)
		}
		h.redirectMagicError(w, r, "expired_or_used")
		return
	}

	// Look up the target user's slug + disabled state. A row disabled
	// after the token was minted refuses redemption — the vendor's
	// intent was to sign in a live user, not a dead one.
	var (
		disabled bool
		slug     string
	)
	if err := h.store.AdminPool().QueryRow(ctx, `
		SELECT u.disabled_at IS NOT NULL, COALESCE(c.slug,'')
		  FROM users u
		  LEFT JOIN customers c ON c.id = u.customer_id
		 WHERE u.id = $1`,
		userID).Scan(&disabled, &slug); err != nil {
		h.log.Error("magic link redeem: user lookup", "error", err)
		h.redirectMagicError(w, r, "user_lookup_failed")
		return
	}
	if disabled {
		h.redirectMagicError(w, r, "user_disabled")
		return
	}

	// Mint an av_ session for the target user, matching the shape the
	// password-login handler uses. Shorter TTL than a normal login;
	// break-glass isn't the everyday path.
	sessionToken, err := mintMagicSession()
	if err != nil {
		h.log.Error("magic link redeem: token mint", "error", err)
		h.redirectMagicError(w, r, "session_mint_failed")
		return
	}
	ua := r.Header.Get("User-Agent")
	if len(ua) > 500 {
		ua = ua[:500]
	}
	if _, err := h.store.AdminPool().Exec(ctx, `
		INSERT INTO user_sessions (user_id, token_hash, expires_at, user_agent, ip_address)
		VALUES ($1, $2, now() + $3::interval, $4, $5)`,
		userID, portalauth.HashToken(sessionToken),
		magicSessionTTL.String(), ua, clientIP(r)); err != nil {
		h.log.Error("magic link redeem: session insert", "error", err)
		h.redirectMagicError(w, r, "session_mint_failed")
		return
	}
	// Best-effort last_login_at bump so the /helpdesk/users list
	// reflects the sign-in.
	_, _ = h.store.AdminPool().Exec(ctx,
		`UPDATE users SET last_login_at = now() WHERE id = $1`, userID)

	h.log.Info("magic link consumed", "user_id", userID)

	target := h.magicRedirectTarget(slug, sessionToken)
	http.Redirect(w, r, target, http.StatusFound)
}

// mintMagicSession returns a fresh av_-prefixed opaque token — same
// shape and prefix as the password login path so ChainResolver routes
// it to the LocalResolver on subsequent requests.
func mintMagicSession() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return portalauth.LocalTokenPrefix + hex.EncodeToString(b[:]), nil
}

// magicRedirectTarget composes the destination the browser lands on
// post-redeem. Same swap-first-host-label trick as the other
// customer-branded destinations. Slug empty => app.<env>.
func (h *Handler) magicRedirectTarget(slug, sessionToken string) string {
	tok := url.QueryEscape(sessionToken)
	base := strings.TrimRight(h.portalBaseURL, "/")
	if base == "" {
		return "/sign-in/callback?token=" + tok
	}
	if slug == "" {
		return base + "/sign-in/callback?token=" + tok
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return base + "/sign-in/callback?token=" + tok
	}
	hostParts := strings.SplitN(u.Host, ".", 2)
	if len(hostParts) < 2 {
		return base + "/sign-in/callback?token=" + tok
	}
	u.Host = slug + "." + hostParts[1]
	return strings.TrimRight(u.String(), "/") + "/sign-in/callback?token=" + tok
}

// redirectMagicError bounces the browser back to the base sign-in page
// with a ?magic_error= code the sign-in form can render as an inline
// banner. Uniform "we can't tell you why the link failed" behaviour so
// probes can't distinguish "expired" from "never existed" — the vendor
// audit log has the truth when it matters.
func (h *Handler) redirectMagicError(w http.ResponseWriter, r *http.Request, code string) {
	base := strings.TrimRight(h.portalBaseURL, "/")
	if base == "" {
		scheme := "https"
		if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
			scheme = p
		} else if r.TLS == nil {
			scheme = "http"
		}
		host := r.Host
		if hv := r.Header.Get("X-Forwarded-Host"); hv != "" {
			host = hv
		}
		base = scheme + "://" + host
	}
	http.Redirect(w, r,
		base+"/sign-in?magic_error="+url.QueryEscape(code),
		http.StatusFound)
}
