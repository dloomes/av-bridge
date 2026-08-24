package portalapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// EntraVendorHandler owns the OIDC sign-in flow for helpdesk (vendor)
// staff. Single-tenant Entra app registration in the vendor's own Entra
// tenant. Portal users (customer sign-in) come later via a separate,
// multi-tenant app registration.
//
// Two routes are mounted outside the auth middleware in server.go:
//
//	GET /api/v1/auth/entra/vendor/authorize
//	GET /api/v1/auth/entra/vendor/callback
//
// The overall flow:
//
//  1. Portal SSO button links to `authorize`, which mints PKCE + state,
//     drops them into short-lived cookies, and 302s the browser to
//     Microsoft's authorize endpoint.
//
//  2. Microsoft authenticates the user, then redirects the browser back
//     to `callback` with an authorization code + our state.
//
//  3. `callback` verifies state, exchanges code+PKCE for an id_token,
//     verifies the id_token cryptographically (portalauth.EntraVerifier),
//     finds/links/creates the local user row, mints an `av_` session
//     token, and 302s to the portal's `/sign-in/callback?token=...`.
//
//  4. The portal reads the token from the URL, stores it in localStorage
//     (same shape as password login), calls whoami, and lands on `/`.
type EntraVendorHandler struct {
	store          *db.Store
	verifier       *portalauth.EntraVerifier
	tenantID       string
	clientID       string
	clientSecret   string
	redirectURI    string
	portalBaseURL  string // fallback: request scheme+host if empty
	vendorTenantID string // the vendor_tenants.id row this Entra tenant maps to
	log            *slog.Logger
	httpc          *http.Client
}

// NewEntraVendorHandler returns nil (and logs a warn) if any of the
// required config is missing. Callers should mount the routes only when
// this returns non-nil.
func NewEntraVendorHandler(
	store *db.Store,
	tenantID, clientID, clientSecret, redirectURI, portalBaseURL string,
	log *slog.Logger,
) *EntraVendorHandler {
	if tenantID == "" || clientID == "" || clientSecret == "" || redirectURI == "" {
		log.Warn("entra vendor SSO disabled — missing config",
			"tenant_id_set", tenantID != "",
			"client_id_set", clientID != "",
			"client_secret_set", clientSecret != "",
			"redirect_uri_set", redirectURI != "",
		)
		return nil
	}
	return &EntraVendorHandler{
		store:          store,
		verifier:       portalauth.NewEntraVerifier(tenantID, clientID),
		tenantID:       tenantID,
		clientID:       clientID,
		clientSecret:   clientSecret,
		redirectURI:    redirectURI,
		portalBaseURL:  portalBaseURL,
		vendorTenantID: db.PocVendorTenantUUID(), // single vendor tenant today
		log:            log,
		httpc:          &http.Client{Timeout: 10 * time.Second},
	}
}

// Authorize — GET /api/v1/auth/entra/vendor/authorize
//
// Kicks off the OAuth authorization-code + PKCE flow. Generates two random
// nonces (state, code_verifier), sets them as HttpOnly cookies scoped to
// the callback path, and 302s the browser to Microsoft.
//
// PKCE + state together mitigate:
//   - CSRF on the callback (state must round-trip through Microsoft)
//   - Interception of the auth code (verifier known only to us; without
//     it a leaked code can't be exchanged)
func (h *EntraVendorHandler) Authorize(w http.ResponseWriter, r *http.Request) {
	state, err := randomToken(24)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "state gen failed")
		return
	}
	verifier, err := randomToken(48)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "verifier gen failed")
		return
	}
	// PKCE challenge is BASE64URL(SHA256(verifier)) — per RFC 7636 S256.
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	// Both cookies scoped to the callback path and lifetime-bounded to
	// 5min — long enough for a slow human, short enough that a stale
	// nonce won't hang around. SameSite=Lax so Microsoft's 302 back to us
	// carries them; None+Secure would work too but Lax is enough here.
	setShortCookie(w, "entra_state_v", state)
	setShortCookie(w, "entra_pkce_v", verifier)

	q := url.Values{}
	q.Set("client_id", h.clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", h.redirectURI)
	q.Set("response_mode", "query")
	q.Set("scope", "openid profile email User.Read offline_access")
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")

	authorizeURL := "https://login.microsoftonline.com/" + h.tenantID + "/oauth2/v2.0/authorize?" + q.Encode()
	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

// Callback — GET /api/v1/auth/entra/vendor/callback
//
// The full server side of the sign-in flow. Every failure path 302s the
// browser back to /sign-in with an ?error= param so the portal can render
// a message rather than showing a raw JSON error.
func (h *EntraVendorHandler) Callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// Microsoft returns ?error=... when the user cancels or consent fails.
	// Pass the error through to the portal untouched — the sign-in form
	// already knows how to render an inline banner.
	if e := q.Get("error"); e != "" {
		h.redirectSignInError(w, r, e)
		return
	}

	code := q.Get("code")
	returnedState := q.Get("state")
	if code == "" || returnedState == "" {
		h.redirectSignInError(w, r, "missing_code_or_state")
		return
	}

	// State cookie must match the query-string state exactly. Missing
	// cookie means someone hit the callback URL directly without going
	// through /authorize first — treat as CSRF and bounce.
	stateCookie, err := r.Cookie("entra_state_v")
	if err != nil || stateCookie.Value == "" || stateCookie.Value != returnedState {
		h.redirectSignInError(w, r, "state_mismatch")
		return
	}
	verifierCookie, err := r.Cookie("entra_pkce_v")
	if err != nil || verifierCookie.Value == "" {
		h.redirectSignInError(w, r, "missing_verifier")
		return
	}
	// Clear both cookies now that we've read them — belt-and-braces so a
	// double-navigation can't replay the same nonce.
	clearShortCookie(w, "entra_state_v")
	clearShortCookie(w, "entra_pkce_v")

	// Exchange the code for tokens. Microsoft's /token endpoint accepts
	// form-encoded parameters.
	idToken, err := h.exchangeCode(r.Context(), code, verifierCookie.Value)
	if err != nil {
		h.log.Warn("entra callback: code exchange failed", "error", err)
		h.redirectSignInError(w, r, "code_exchange_failed")
		return
	}

	claims, err := h.verifier.Verify(r.Context(), idToken)
	if err != nil {
		h.log.Warn("entra callback: id_token verify failed", "error", err)
		h.redirectSignInError(w, r, "token_verify_failed")
		return
	}

	// JIT-link or JIT-create the user row inside the vendor tenant, then
	// mint a session token so the portal can sign in with it exactly as
	// though the user had used a password.
	userID, err := h.linkOrCreateUser(r.Context(), claims)
	if err != nil {
		h.log.Error("entra callback: user link/create failed", "error", err)
		h.redirectSignInError(w, r, "user_provision_failed")
		return
	}
	sessionToken, err := h.mintSession(r.Context(), userID, r)
	if err != nil {
		h.log.Error("entra callback: session mint failed", "error", err)
		h.redirectSignInError(w, r, "session_mint_failed")
		return
	}
	h.log.Info("entra vendor sign-in",
		"user_id", userID,
		"email", claims.EmailOrUpn(),
		"sub", claims.Sub,
	)

	target := h.portalOrigin(r) + "/sign-in/callback?token=" + url.QueryEscape(sessionToken)
	http.Redirect(w, r, target, http.StatusFound)
}

// exchangeCode POSTs the authorization code + PKCE verifier to
// Microsoft's token endpoint and returns the id_token.
func (h *EntraVendorHandler) exchangeCode(ctx context.Context, code, verifier string) (string, error) {
	form := url.Values{}
	form.Set("client_id", h.clientID)
	form.Set("client_secret", h.clientSecret)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", h.redirectURI)
	form.Set("code_verifier", verifier)
	form.Set("scope", "openid profile email User.Read")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://login.microsoftonline.com/"+h.tenantID+"/oauth2/v2.0/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := h.httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint HTTP %d: %s", resp.StatusCode, string(body))
	}
	var out struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
		ErrDesc string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("token response parse: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("token endpoint error %s: %s", out.Error, out.ErrDesc)
	}
	if out.IDToken == "" {
		return "", errors.New("token response missing id_token")
	}
	return out.IDToken, nil
}

// linkOrCreateUser is the JIT provisioning logic. Order matters:
//
//  1. Look up by (vendor_tenant_id, entra_sub) — the fully-linked case.
//     A user who has signed in via Entra before matches here.
//  2. Look up by (vendor_tenant_id, lower(email)) — the first-sign-in
//     case for an existing local user. Link entra_sub onto the row so
//     future look-ups take path 1.
//  3. Otherwise INSERT a new provider='entra' user row with the given
//     entra_sub, no password_hash.
//
// Role assignment (M3.1 strict-sync): after ANY of the three paths
// resolves a user, vendor role_mappings are consulted. The highest-
// priority matching role wins (admin > operator > viewer). users.role
// + role_source are updated ONLY when:
//   * the row is a fresh JIT create, OR
//   * role_source='entra' (this user is under Entra control)
// A row with role_source='manual' — an admin promoted them via the
// vendor /users UI — keeps their role even if their group memberships
// change. That's the escape hatch for override-with-intent.
//
// When no group matches and the user is fresh, we insert with 'viewer'
// + role_source='entra'.
func (h *EntraVendorHandler) linkOrCreateUser(ctx context.Context, claims portalauth.EntraClaims) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	pool := h.store.AdminPool()
	email := strings.ToLower(strings.TrimSpace(claims.EmailOrUpn()))
	if email == "" {
		return "", errors.New("id_token has neither email nor preferred_username")
	}

	// Three lookup paths — after any of them resolves, strict-sync runs
	// so revoked group memberships propagate on next sign-in.
	var userID string
	err := pool.QueryRow(ctx, `
		SELECT id::text FROM users
		 WHERE vendor_tenant_id = $1 AND entra_sub = $2
		 LIMIT 1`,
		h.vendorTenantID, claims.Sub).Scan(&userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("lookup by sub: %w", err)
	}
	if userID == "" {
		err = pool.QueryRow(ctx, `
			UPDATE users
			   SET entra_sub = $2, provider = 'entra'
			 WHERE vendor_tenant_id = $1 AND lower(email) = lower($3) AND entra_sub IS NULL
			 RETURNING id::text`,
			h.vendorTenantID, claims.Sub, email).Scan(&userID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("link by email: %w", err)
		}
		if userID != "" {
			h.log.Info("entra: linked existing local user", "user_id", userID, "email", email)
		}
	}
	created := false
	if userID == "" {
		newRole := resolveVendorRoleFromGroups(ctx, pool, h.vendorTenantID, claims.Groups)
		name := strings.TrimSpace(claims.Name)
		if name == "" {
			name = email
		}
		// Fresh JIT row starts with role_source='entra' — mappings own
		// the role until a vendor admin explicitly overrides via the
		// /helpdesk/users UI (which will flip role_source to 'manual').
		if err := pool.QueryRow(ctx, `
			INSERT INTO users (email, full_name, vendor_tenant_id, role, provider, entra_sub, role_source)
			VALUES ($1, $2, $3, $5, 'entra', $4, 'entra')
			RETURNING id::text`,
			email, name, h.vendorTenantID, claims.Sub, newRole).Scan(&userID); err != nil {
			return "", fmt.Errorf("jit create: %w", err)
		}
		created = true
		h.log.Info("entra: JIT-created vendor user",
			"user_id", userID, "email", email, "role", newRole,
			"group_count", len(claims.Groups))
	}

	// M3.1 strict-sync: overwrite users.role from mappings only if the
	// row is entra-managed. Skip the write when the row is fresh (the
	// INSERT above already applied mappings). role_source='manual'
	// rows are left untouched — that's the admin override escape hatch.
	if !created {
		syncVendorRole(ctx, pool, h.vendorTenantID, userID, claims.Groups, h.log)
	}
	return userID, nil
}

// syncVendorRole is the M3.1 strict-sync entry point for vendor rows.
// Only mutates users.role when role_source='entra'. Errors are logged
// and swallowed.
func syncVendorRole(ctx context.Context, pool interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, vendorTenantID, userID string, groupIDs []string, log *slog.Logger) {
	newRole := resolveVendorRoleFromGroups(ctx, pool, vendorTenantID, groupIDs)
	// Only writes if the row is still under Entra control. A manual
	// promotion (role_source='manual') isn't overwritten.
	if _, err := pool.Exec(ctx, `
		UPDATE users
		   SET role = $2
		 WHERE id = $1
		   AND vendor_tenant_id = $3
		   AND role_source = 'entra'`,
		userID, newRole, vendorTenantID); err != nil {
		log.Warn("entra: vendor role sync failed",
			"user_id", userID, "error", err)
	}
}

// vendorRolePriority orders the three legacy vendor roles from highest
// (0) to lowest (2). Used by resolveVendorRoleFromGroups so a user in
// both an admins-group AND a viewers-group is promoted to admin, not
// demoted to viewer.
func vendorRolePriority(r string) int {
	switch r {
	case "admin":
		return 0
	case "operator":
		return 1
	case "viewer":
		return 2
	default:
		return 3
	}
}

// resolveVendorRoleFromGroups queries role_mappings for the given vendor
// tenant, filters by which groups the caller actually belongs to, and
// returns the highest-priority matching role. Falls back to 'viewer' when
// no groups or no matches. Any DB error is logged and treated as "no
// match" so a role_mappings hiccup can't block sign-in.
func resolveVendorRoleFromGroups(ctx context.Context, pool interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, vendorTenantID string, groupIDs []string) string {
	const fallback = "viewer"
	if len(groupIDs) == 0 {
		return fallback
	}
	rows, err := pool.Query(ctx, `
		SELECT role
		  FROM role_mappings
		 WHERE vendor_tenant_id = $1
		   AND group_id = ANY($2::text[])`,
		vendorTenantID, groupIDs)
	if err != nil {
		return fallback
	}
	defer rows.Close()
	best := fallback
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			continue
		}
		if vendorRolePriority(role) < vendorRolePriority(best) {
			best = role
		}
	}
	return best
}

// mintSession inserts a user_sessions row and returns the raw av_ token.
// Mirrors the password-login path — same TTL, same shape.
func (h *EntraVendorHandler) mintSession(ctx context.Context, userID string, r *http.Request) (string, error) {
	token, err := mintToken()
	if err != nil {
		return "", err
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
		return "", err
	}
	// Best-effort last_login_at bump.
	_, _ = h.store.AdminPool().Exec(ctx,
		`UPDATE users SET last_login_at = now() WHERE id = $1`, userID)
	return token, nil
}

// portalOrigin picks the origin the callback redirects the browser back
// to. Explicit config wins; otherwise fall back to the request's own
// scheme+host so a single-origin deploy works out of the box.
func (h *EntraVendorHandler) portalOrigin(r *http.Request) string {
	if h.portalBaseURL != "" {
		return strings.TrimRight(h.portalBaseURL, "/")
	}
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") == "" {
		scheme = "http"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}

func (h *EntraVendorHandler) redirectSignInError(w http.ResponseWriter, r *http.Request, code string) {
	target := h.portalOrigin(r) + "/sign-in?entra_error=" + url.QueryEscape(code)
	http.Redirect(w, r, target, http.StatusFound)
}

// randomToken returns a base64url-encoded n-byte random string. Used for
// PKCE verifier + state nonce.
func randomToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// setShortCookie writes a 5-minute HttpOnly cookie scoped to the callback
// path. SameSite=Lax so Microsoft's 302 back to us carries it; Secure
// because we always serve behind HTTPS in real deployments (and browsers
// tolerate Secure over localhost dev).
func setShortCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/api/v1/auth/entra/",
		MaxAge:   300,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearShortCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/api/v1/auth/entra/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}
