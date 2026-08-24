package portalapi

import (
	"context"
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
	"sync"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// EntraCustomerHandler owns the OIDC sign-in flow for CUSTOMER users. Sits
// alongside EntraVendorHandler (helpdesk staff) but is fundamentally
// different in shape:
//
//   - **Multi-tenant Entra app registration.** ONE app registered in the
//     vendor's own tenant, marked multi-tenant. Every customer admin
//     consents to that same client_id once; their users then sign in via
//     login.microsoftonline.com/{their_tid}/... with the SAME client_id.
//     We store only the customer's entra_tenant_id per row (already
//     scaffolded on customers.entra_tenant_id).
//
//   - **Slug-scoped authorize.** The sign-in page hosted at
//     <slug>.<env>.involvecloud.com links to an ABSOLUTE authorize URL on
//     the app.<env> origin with ?slug=<slug>. That keeps authorize +
//     callback same-origin (cookies work) while still letting the
//     landing page live under the customer's brand.
//
//   - **Cross-tenant token safety.** Because our multi-tenant app accepts
//     tokens from any tenant, the callback MUST assert
//     token.tid == customer.entra_tenant_id or the sign-in is rejected.
//     Without that, a user from any Entra tenant could sign in as some
//     other customer by tampering with cookies.
//
// Routes mounted outside the auth middleware (they run pre-session):
//
//	GET /api/v1/auth/entra/customer/authorize?slug=<slug>
//	GET /api/v1/auth/entra/customer/callback
type EntraCustomerHandler struct {
	store         *db.Store
	clientID      string
	clientSecret  string
	redirectURI   string
	portalBaseURL string // app.<env> — origin the callback lands on
	log           *slog.Logger
	httpc         *http.Client

	// verifiers is a lazy per-tenant cache of portalauth.EntraVerifier
	// instances. Each verifier owns its own JWKS cache (5m TTL). N grows
	// with active customers, expected order of magnitude 10-50 — nothing
	// worth an eviction policy for. Guarded by mu so two concurrent
	// callbacks against a cold tenant don't race the map write.
	mu        sync.Mutex
	verifiers map[string]*portalauth.EntraVerifier
}

// NewEntraCustomerHandler returns nil (and logs a warn) if any required
// config is missing. Callers must nil-check before mounting the routes.
func NewEntraCustomerHandler(
	store *db.Store,
	clientID, clientSecret, redirectURI, portalBaseURL string,
	log *slog.Logger,
) *EntraCustomerHandler {
	if clientID == "" || clientSecret == "" || redirectURI == "" {
		log.Warn("entra customer SSO disabled — missing config",
			"client_id_set", clientID != "",
			"client_secret_set", clientSecret != "",
			"redirect_uri_set", redirectURI != "",
		)
		return nil
	}
	return &EntraCustomerHandler{
		store:         store,
		clientID:      clientID,
		clientSecret:  clientSecret,
		redirectURI:   redirectURI,
		portalBaseURL: portalBaseURL,
		log:           log,
		httpc:         &http.Client{Timeout: 10 * time.Second},
		verifiers:     make(map[string]*portalauth.EntraVerifier),
	}
}

// verifierFor returns the per-tenant verifier, constructing on first use.
// Each verifier is fixed to its (tid, our_client_id) pair, matching the
// invariants the verifier already enforces.
func (h *EntraCustomerHandler) verifierFor(tid string) *portalauth.EntraVerifier {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.verifiers[tid]
	if !ok {
		v = portalauth.NewEntraVerifier(tid, h.clientID)
		h.verifiers[tid] = v
	}
	return v
}

// Authorize — GET /api/v1/auth/entra/customer/authorize?slug=<slug>
//
// Slug is looked up in customers to find the entra_tenant_id. If the
// customer has no configured tenant, we redirect back to the sign-in
// page with an inline error so the user isn't left staring at a JSON
// response.
func (h *EntraCustomerHandler) Authorize(w http.ResponseWriter, r *http.Request) {
	slug := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("slug")))
	if slug == "" {
		h.redirectSignInError(w, r, slug, "missing_slug")
		return
	}

	// Lookup customer → tenant_id + slug (round-trip so we know both the
	// signing tenant AND the return-to origin without an extra query).
	var (
		customerID, tenantID string
	)
	err := h.store.AdminPool().QueryRow(r.Context(), `
		SELECT id::text, COALESCE(entra_tenant_id,'')
		  FROM customers
		 WHERE lower(slug) = $1
		 LIMIT 1`, slug).Scan(&customerID, &tenantID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.log.Warn("entra customer authorize: slug lookup failed",
				"slug", slug, "error", err)
		}
		h.redirectSignInError(w, r, slug, "unknown_customer")
		return
	}
	if tenantID == "" {
		h.redirectSignInError(w, r, slug, "sso_not_configured")
		return
	}

	state, err := randomToken(24)
	if err != nil {
		h.redirectSignInError(w, r, slug, "state_gen_failed")
		return
	}
	verifier, err := randomToken(48)
	if err != nil {
		h.redirectSignInError(w, r, slug, "verifier_gen_failed")
		return
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	// Three cookies. All scoped to /api/v1/auth/entra/ so both the vendor
	// and customer callbacks can read theirs. Prefixed with entra_c_ to
	// avoid colliding with the vendor path's entra_state_v / entra_pkce_v.
	//
	// entra_c_cust carries the customer_id (not the slug) so the callback
	// re-looks-up the row by primary key — an operator renaming a slug
	// mid-flow doesn't lose the session.
	setShortCookie(w, "entra_c_state", state)
	setShortCookie(w, "entra_c_pkce", verifier)
	setShortCookie(w, "entra_c_cust", customerID)

	q := url.Values{}
	q.Set("client_id", h.clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", h.redirectURI)
	q.Set("response_mode", "query")
	q.Set("scope", "openid profile email User.Read offline_access")
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")

	authorizeURL := "https://login.microsoftonline.com/" + tenantID + "/oauth2/v2.0/authorize?" + q.Encode()
	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

// Callback — GET /api/v1/auth/entra/customer/callback
//
// Reads the three cookies, exchanges the code with the customer's tenant,
// verifies the token AGAINST THAT tenant (not a global one), asserts the
// token's tid matches the customer we started with, and JIT-links or
// JIT-creates a user row under the customer.
func (h *EntraCustomerHandler) Callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		// Best-effort slug from the cust cookie so the redirect lands on
		// the customer's branded sign-in page rather than app.<env>.
		slug := h.slugFromCookie(r)
		h.redirectSignInError(w, r, slug, e)
		return
	}
	code := q.Get("code")
	returnedState := q.Get("state")
	if code == "" || returnedState == "" {
		h.redirectSignInError(w, r, h.slugFromCookie(r), "missing_code_or_state")
		return
	}

	stateCookie, err := r.Cookie("entra_c_state")
	if err != nil || stateCookie.Value == "" || stateCookie.Value != returnedState {
		h.redirectSignInError(w, r, h.slugFromCookie(r), "state_mismatch")
		return
	}
	verifierCookie, err := r.Cookie("entra_c_pkce")
	if err != nil || verifierCookie.Value == "" {
		h.redirectSignInError(w, r, h.slugFromCookie(r), "missing_verifier")
		return
	}
	custCookie, err := r.Cookie("entra_c_cust")
	if err != nil || custCookie.Value == "" {
		h.redirectSignInError(w, r, "", "missing_customer")
		return
	}
	// Clear cookies now so a double-navigation can't replay nonces.
	clearShortCookie(w, "entra_c_state")
	clearShortCookie(w, "entra_c_pkce")
	clearShortCookie(w, "entra_c_cust")

	customerID := custCookie.Value

	// Re-look-up customer BY ID (cookie carried it) so a rename doesn't
	// break the flow. We need both the current slug (for redirect) and
	// entra_tenant_id (to select the right verifier + token exchange).
	var (
		slug     string
		tenantID string
	)
	err = h.store.AdminPool().QueryRow(r.Context(), `
		SELECT COALESCE(slug,''), COALESCE(entra_tenant_id,'')
		  FROM customers
		 WHERE id = $1`, customerID).Scan(&slug, &tenantID)
	if err != nil {
		h.log.Warn("entra customer callback: customer lookup failed",
			"customer_id", customerID, "error", err)
		h.redirectSignInError(w, r, "", "unknown_customer")
		return
	}
	if tenantID == "" {
		h.redirectSignInError(w, r, slug, "sso_not_configured")
		return
	}

	idToken, err := h.exchangeCode(r.Context(), tenantID, code, verifierCookie.Value)
	if err != nil {
		h.log.Warn("entra customer callback: code exchange failed",
			"customer_id", customerID, "error", err)
		h.redirectSignInError(w, r, slug, "code_exchange_failed")
		return
	}

	verifier := h.verifierFor(tenantID)
	claims, err := verifier.Verify(r.Context(), idToken)
	if err != nil {
		h.log.Warn("entra customer callback: id_token verify failed",
			"customer_id", customerID, "error", err)
		h.redirectSignInError(w, r, slug, "token_verify_failed")
		return
	}
	// Belt-and-braces: the verifier already enforces tid == its own
	// tenantID, but we constructed that verifier from the cookie-derived
	// customer_id and re-derived tenantID from the DB. This check reasserts
	// the chain (cookie → DB → verifier → token) is unbroken end-to-end.
	if claims.Tid != tenantID {
		h.log.Error("entra customer callback: tid mismatch after verify",
			"customer_id", customerID, "claim_tid", claims.Tid, "want_tid", tenantID)
		h.redirectSignInError(w, r, slug, "tid_mismatch")
		return
	}

	userID, err := h.linkOrCreateUser(r.Context(), customerID, claims)
	if err != nil {
		h.log.Error("entra customer callback: user link/create failed",
			"customer_id", customerID, "error", err)
		h.redirectSignInError(w, r, slug, "user_provision_failed")
		return
	}
	sessionToken, err := h.mintSession(r.Context(), userID, r)
	if err != nil {
		h.log.Error("entra customer callback: session mint failed",
			"customer_id", customerID, "error", err)
		h.redirectSignInError(w, r, slug, "session_mint_failed")
		return
	}
	h.log.Info("entra customer sign-in",
		"user_id", userID,
		"customer_id", customerID,
		"email", claims.EmailOrUpn(),
		"sub", claims.Sub,
	)

	// Return to the customer's branded sign-in callback page so the
	// portal picks up the token and lands the user on /. If we don't
	// have a portalBaseURL configured OR the customer has no slug, fall
	// back to app.<env> for the callback landing.
	target := h.customerCallbackURL(slug, sessionToken)
	http.Redirect(w, r, target, http.StatusFound)
}

// exchangeCode POSTs to the specific customer's tenant token endpoint.
// Symmetric with the vendor path except the tenant URL varies.
func (h *EntraCustomerHandler) exchangeCode(ctx context.Context, tenantID, code, pkceVerifier string) (string, error) {
	form := url.Values{}
	form.Set("client_id", h.clientID)
	form.Set("client_secret", h.clientSecret)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", h.redirectURI)
	form.Set("code_verifier", pkceVerifier)
	form.Set("scope", "openid profile email User.Read")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://login.microsoftonline.com/"+tenantID+"/oauth2/v2.0/token",
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

// linkOrCreateUser is the customer-scoped JIT provisioning logic. Order
// matches the vendor path except every lookup is scoped by customer_id
// rather than vendor_tenant_id:
//
//  1. by (customer_id, entra_sub) — fully-linked user
//  2. by (customer_id, lower(email)) with entra_sub NULL — first Entra
//     sign-in for an existing local user; link entra_sub onto the row
//  3. INSERT a new provider='entra' row scoped to the customer with
//     role='viewer' (kept on users.role for display / legacy fallback).
//
// M3: after INSERT, the caller's Entra groups are resolved against
// customer-scoped role_mappings. Any matching role NAMES are looked up
// in the customer's roles table and joined to the new user via
// user_roles. Unmapped groups and non-existent role names are silently
// skipped (an admin renaming a role shouldn't lock users out); if no
// role gets granted at all, the fallback users.role='viewer' still
// leaves them with the seeded viewer role via the audit path.
func (h *EntraCustomerHandler) linkOrCreateUser(ctx context.Context, customerID string, claims portalauth.EntraClaims) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	pool := h.store.AdminPool()
	email := strings.ToLower(strings.TrimSpace(claims.EmailOrUpn()))
	if email == "" {
		return "", errors.New("id_token has neither email nor preferred_username")
	}

	var userID string
	err := pool.QueryRow(ctx, `
		SELECT id::text FROM users
		 WHERE customer_id = $1 AND entra_sub = $2
		 LIMIT 1`,
		customerID, claims.Sub).Scan(&userID)
	if err == nil {
		return userID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("lookup by sub: %w", err)
	}

	err = pool.QueryRow(ctx, `
		UPDATE users
		   SET entra_sub = $2, provider = 'entra'
		 WHERE customer_id = $1 AND lower(email) = lower($3) AND entra_sub IS NULL
		 RETURNING id::text`,
		customerID, claims.Sub, email).Scan(&userID)
	if err == nil {
		h.log.Info("entra customer: linked existing local user",
			"user_id", userID, "customer_id", customerID, "email", email)
		return userID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("link by email: %w", err)
	}

	name := strings.TrimSpace(claims.Name)
	if name == "" {
		name = email
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO users (email, full_name, customer_id, role, provider, entra_sub)
		VALUES ($1, $2, $3, 'viewer', 'entra', $4)
		RETURNING id::text`,
		email, name, customerID, claims.Sub).Scan(&userID)
	if err != nil {
		return "", fmt.Errorf("jit create: %w", err)
	}

	// M3: resolve Entra groups → customer-scoped role_mappings → roles
	// and grant them via user_roles. Silent on unmapped groups; log a
	// warn on DB errors so an ops mis-config surfaces without breaking
	// sign-in. New user with no mappings ends up with only the legacy
	// users.role='viewer' fallback until an admin promotes them.
	granted := h.applyEntraRoleGrants(ctx, customerID, userID, claims.Groups)
	h.log.Info("entra customer: JIT-created user",
		"user_id", userID, "customer_id", customerID, "email", email,
		"group_count", len(claims.Groups), "roles_granted", granted)
	return userID, nil
}

// applyEntraRoleGrants resolves the caller's group memberships against
// customer role_mappings and inserts matching rows into user_roles.
// Returns the number of role grants inserted (0 when no groups match or
// no rows exist). Errors are logged and swallowed — a mapping table
// glitch shouldn't fail sign-in.
//
// The join is done in a single query: role_mappings → roles (by
// customer_id + case-insensitive name match). Duplicate grants use
// ON CONFLICT DO NOTHING so a group→role mapping that references the
// same role as another mapping doesn't blow up the INSERT.
func (h *EntraCustomerHandler) applyEntraRoleGrants(ctx context.Context, customerID, userID string, groupIDs []string) int {
	if len(groupIDs) == 0 {
		return 0
	}
	tag, err := h.store.AdminPool().Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $2::uuid, r.id
		  FROM role_mappings m
		  JOIN roles r
		    ON r.customer_id = m.customer_id
		   AND lower(r.name) = lower(m.role)
		 WHERE m.customer_id = $1
		   AND m.group_id = ANY($3::text[])
		ON CONFLICT DO NOTHING`,
		customerID, userID, groupIDs)
	if err != nil {
		h.log.Warn("entra customer: role grant failed",
			"user_id", userID, "customer_id", customerID, "error", err)
		return 0
	}
	return int(tag.RowsAffected())
}

// mintSession creates a user_sessions row and returns the raw av_ token.
// Copy of the vendor helper — kept as its own method so the two paths
// don't share hidden state.
func (h *EntraCustomerHandler) mintSession(ctx context.Context, userID string, r *http.Request) (string, error) {
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
	_, _ = h.store.AdminPool().Exec(ctx,
		`UPDATE users SET last_login_at = now() WHERE id = $1`, userID)
	return token, nil
}

// slugFromCookie is a best-effort read for error redirects — the state
// cookie can be missing (someone hitting /callback directly) which is fine;
// the caller falls back to app.<env>.
func (h *EntraCustomerHandler) slugFromCookie(r *http.Request) string {
	cust, err := r.Cookie("entra_c_cust")
	if err != nil || cust.Value == "" {
		return ""
	}
	var slug string
	_ = h.store.AdminPool().QueryRow(r.Context(),
		`SELECT COALESCE(slug,'') FROM customers WHERE id = $1`, cust.Value).Scan(&slug)
	return slug
}

// customerCallbackURL constructs the return URL after a successful
// sign-in. Slug-scoped when we have one; app.<env> fallback otherwise.
func (h *EntraCustomerHandler) customerCallbackURL(slug, sessionToken string) string {
	tok := url.QueryEscape(sessionToken)
	base := strings.TrimRight(h.portalBaseURL, "/")
	if slug == "" || base == "" {
		if base == "" {
			return "/sign-in/callback?token=" + tok
		}
		return base + "/sign-in/callback?token=" + tok
	}
	// Swap the first host label to the slug. Matches how the reset URL
	// builder in publicapi does it — one code path for the app→slug
	// derivation and it keeps env suffix knowledge in one place per
	// package. Duplicated here rather than exported from publicapi to
	// keep the dependency direction (portalapi → publicapi) clean; if
	// this grows a third caller, factor it up.
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

// redirectSignInError bounces the browser back to the appropriate
// sign-in page with an ?entra_error= code the sign-in form can render.
// Prefers the customer's branded sign-in URL when we know the slug,
// falls back to app.<env>/sign-in otherwise.
func (h *EntraCustomerHandler) redirectSignInError(w http.ResponseWriter, r *http.Request, slug, code string) {
	base := strings.TrimRight(h.portalBaseURL, "/")
	if base == "" {
		// Dev fallback — the same scheme+host the callback came in on.
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
	target := base + "/sign-in?entra_error=" + url.QueryEscape(code)
	if slug != "" {
		if u, err := url.Parse(base); err == nil && u.Host != "" {
			hostParts := strings.SplitN(u.Host, ".", 2)
			if len(hostParts) == 2 {
				u.Host = slug + "." + hostParts[1]
				u.Path = "/sign-in"
				u.RawQuery = "entra_error=" + url.QueryEscape(code)
				target = u.String()
			}
		}
	}
	http.Redirect(w, r, target, http.StatusFound)
}
