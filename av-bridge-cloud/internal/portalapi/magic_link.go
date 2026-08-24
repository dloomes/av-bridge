package portalapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// Magic-link (break-glass) sign-in tokens — M4.1.
//
// Vendor helpdesk admins mint a token targeting any user (customer or
// vendor tenant). The URL they hand back to the user drops them into an
// authenticated session for that user — bypassing password AND SSO-only.
// Intended for lockouts, not routine sign-in.
//
// Three endpoints across two packages:
//   * POST /api/v1/helpdesk/users/{id}/magic-link  (this file)
//     — Vendor-only, target must be a vendor-tenant row.
//   * POST /api/v1/users/{id}/magic-link           (this file)
//     — Vendor-only via IsVendor + X-Customer-Scope, target must be a
//       customer-scoped row in the acted-as customer.
//   * GET  /public/magic-link/consume?token=X      (publicapi package)
//     — Redeem. Mints an av_ session for the target user, revokes the
//       token, redirects to that user's branded /sign-in/callback so the
//       portal picks up the token exactly as the password / Entra path.

// magicLinkTTL — short by design. Vendor mints, sends to user, user
// clicks within a coffee break. Longer than a single OTP; shorter than a
// password reset. Adjust env-driven later if operators need it.
const magicLinkTTL = 15 * time.Minute

// magicLinkCooldown suppresses accidental double-mints. Second click
// inside 5 seconds silently re-uses the first token (which the caller
// still has in memory anyway). We could return the same URL again but
// the caller has already left the endpoint — so the second click just
// no-ops and returns the existing unexpired token if there is one.
const magicLinkCooldown = 5 * time.Second

type magicLinkResponse struct {
	// URL is the fully-qualified break-glass sign-in URL. Portal
	// displays this in a modal with a copy-to-clipboard button.
	URL string `json:"url"`
	// ExpiresAt is the RFC3339 wall time the token stops being
	// redeemable — portal renders a countdown to reset admin
	// expectations if they don't send the link quickly.
	ExpiresAt string `json:"expires_at"`
}

// IssueCustomerMagicLink — POST /api/v1/users/{id}/magic-link
//
// Vendor-only path (checked via IsVendor here rather than at the route
// so we can share the customer /users route pattern; the permission
// system doesn't yet have a "vendor bypass but only for this specific
// action" concept). Target user must belong to the acted-as customer.
func (h *Handler) IssueCustomerMagicLink(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	if !p.IsVendor {
		writeErr(w, http.StatusForbidden,
			"break-glass sign-in links can only be issued by vendor helpdesk")
		return
	}
	id := r.PathValue("id")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Look up the target user + verify they belong to the acted-as
	// customer AND are not disabled. Full-name is fetched for the
	// success log; slug for the redirect URL.
	var (
		email    string
		disabled bool
		slug     string
	)
	err := h.store.AdminPool().QueryRow(ctx, `
		SELECT lower(u.email),
		       u.disabled_at IS NOT NULL,
		       COALESCE(c.slug,'')
		  FROM users u
		  JOIN customers c ON c.id = u.customer_id
		 WHERE u.id = $1 AND u.customer_id = $2`,
		id, p.CustomerID).Scan(&email, &disabled, &slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "user not found")
			return
		}
		h.log.Error("magic link: customer lookup", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if disabled {
		writeErr(w, http.StatusBadRequest,
			"user is disabled — re-enable before issuing a sign-in link")
		return
	}

	link, expiresAt, err := h.mintMagicLink(ctx, id, slug, p, r)
	if err != nil {
		h.log.Error("magic link: mint failed",
			"user_id", id, "customer_id", p.CustomerID, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Audit — the vendor issuing this is an action worth capturing.
	// Records who issued it and for whom; the raw token is never
	// audited (only its ID is meaningful for lookup and even that we
	// don't include here to keep the row small).
	_ = h.store.WithTenantScoped(r.Context(), p.CustomerID, principalScope(p), func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "user.magic_link_issued",
			TargetKind: "user", TargetID: id,
			After: mustJSON(map[string]any{
				"target_email": email,
				"ttl_seconds":  int(magicLinkTTL.Seconds()),
			}),
		}))
	})
	h.log.Info("magic link issued (customer)",
		"customer_id", p.CustomerID, "user_id", id,
		"issued_by", p.UserID)

	writeJSON(w, http.StatusCreated, magicLinkResponse{
		URL:       link,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	})
}

// IssueVendorMagicLink — POST /api/v1/helpdesk/users/{id}/magic-link
//
// Vendor-only via RequireVendor at the route. Target must be a vendor-
// tenant row. Same shape as the customer path but scoped to
// vendor_tenant_id and no audit write (no per-customer audit target).
func (h *Handler) IssueVendorMagicLink(w http.ResponseWriter, r *http.Request) {
	p, _ := portalauth.From(r.Context())
	id := r.PathValue("id")
	vendorID := db.PocVendorTenantUUID()

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var (
		email    string
		disabled bool
	)
	err := h.store.AdminPool().QueryRow(ctx, `
		SELECT lower(email), disabled_at IS NOT NULL
		  FROM users
		 WHERE id = $1 AND vendor_tenant_id = $2`,
		id, vendorID).Scan(&email, &disabled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "user not found")
			return
		}
		h.log.Error("magic link: vendor lookup", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if disabled {
		writeErr(w, http.StatusBadRequest,
			"user is disabled — re-enable before issuing a sign-in link")
		return
	}

	// Vendor users always land on app.<env> (no customer slug).
	link, expiresAt, err := h.mintMagicLink(ctx, id, "", p, r)
	if err != nil {
		h.log.Error("magic link: mint failed",
			"user_id", id, "vendor", true, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.log.Info("magic link issued (vendor)",
		"user_id", id, "issued_by", p.UserID)

	writeJSON(w, http.StatusCreated, magicLinkResponse{
		URL:       link,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	})
}

// mintMagicLink is the shared token generation + storage + URL
// construction. Applies the short cooldown so a double-click reuses the
// first token instead of stacking rows.
func (h *Handler) mintMagicLink(ctx context.Context, userID, slug string, p portalauth.Principal, r *http.Request) (string, time.Time, error) {
	// Cooldown check: any unused, unexpired token minted within the
	// last cooldown window is the same intent (or an accidental
	// re-click). Rather than returning the same URL — which we can't
	// (we don't have the raw token) — we suppress net-new mints. The
	// caller still gets a fresh mint after the window closes.
	var recentCount int
	if err := h.store.AdminPool().QueryRow(ctx, `
		SELECT count(*)
		  FROM magic_link_tokens
		 WHERE user_id = $1
		   AND used_at IS NULL
		   AND expires_at > now()
		   AND created_at > now() - $2::interval`,
		userID, magicLinkCooldown.String()).Scan(&recentCount); err != nil {
		return "", time.Time{}, fmt.Errorf("cooldown lookup: %w", err)
	}
	if recentCount > 0 {
		// Deliberately treat rapid re-mints as an error the client can
		// backoff on rather than silently succeeding — the vendor
		// caller needs the actual URL to be useful, and we can't
		// resend the raw token.
		return "", time.Time{}, errors.New("just issued a link — wait a few seconds and try again")
	}

	rawToken, err := mintMagicRaw()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("random: %w", err)
	}

	ua := r.Header.Get("User-Agent")
	if len(ua) > 500 {
		ua = ua[:500]
	}
	expiresAt := time.Now().Add(magicLinkTTL)
	if _, err := h.store.AdminPool().Exec(ctx, `
		INSERT INTO magic_link_tokens
		    (user_id, token_hash, expires_at, issued_by, requester_ip, user_agent)
		VALUES ($1, $2, $3, NULLIF($4::text,'')::uuid, $5, $6)`,
		userID, portalauth.HashToken(rawToken), expiresAt,
		p.UserID, clientIP(r), ua); err != nil {
		return "", time.Time{}, fmt.Errorf("insert: %w", err)
	}

	// Redirect URL lands the user on their branded portal. For a
	// slugged customer, swap the first host label; for vendor / no
	// slug, use the base app.<env> origin. Same trick as reset URLs.
	origin := magicLinkOrigin(h.magicPortalBaseURL(), slug)
	link := origin + "/public/magic-link/consume?token=" + url.QueryEscape(rawToken)
	return link, expiresAt, nil
}

// magicPortalBaseURL returns the configured portal base URL. Extracted
// so the linker can reach it via the Handler; the value itself is
// injected into publicapi (which the token consumer lives in) via the
// existing customer-SSO plumbing. We look it up out of the environment
// through a fresh call because Handler doesn't currently carry the
// value — plumbing it through the constructor is a wider change than
// this feature needs.
func (h *Handler) magicPortalBaseURL() string {
	// The vendor Entra callback stores the same value as an env-derived
	// string; we replicate the read here to avoid threading a new
	// field into Handler. If unset (dev), the link uses a bare relative
	// URL and the admin has to fix up the host by hand — acceptable
	// for dev, the deploy path always sets it.
	return magicLinkPortalBaseURL
}

// magicLinkPortalBaseURL is populated once at startup from
// cfg.EntraPortalBaseURL (see main.go); kept as a package var so all
// magic-link paths pick it up without further plumbing.
var magicLinkPortalBaseURL string

// SetMagicLinkPortalBaseURL is called once from main.go to wire the
// portal base URL into this package. Kept as an explicit setter (not a
// New() ctor) so we don't restructure the existing Handler constructor
// for this small feature.
func SetMagicLinkPortalBaseURL(v string) { magicLinkPortalBaseURL = v }

// magicLinkOrigin composes the target origin for the redirect link.
// Slug empty => base portal (vendor path); slug present => swap the
// first host label.
func magicLinkOrigin(baseURL, slug string) string {
	if baseURL == "" {
		return ""
	}
	if slug == "" {
		return strings.TrimRight(baseURL, "/")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return strings.TrimRight(baseURL, "/")
	}
	hostParts := strings.SplitN(u.Host, ".", 2)
	if len(hostParts) < 2 {
		return strings.TrimRight(baseURL, "/")
	}
	u.Host = slug + "." + hostParts[1]
	return strings.TrimRight(u.String(), "/")
}

// mintMagicRaw returns 64 hex chars of crypto/rand — same shape as
// password reset tokens. No prefix; the token flows through URL params
// on the consume endpoint and is otherwise SHA-256'd for storage.
func mintMagicRaw() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// unused imports guard while iterating.
var _ = json.NewDecoder
