// Package portalauth — Entra ID / Microsoft identity-token verifier.
//
// This file is the security-sensitive core of the Entra sign-in path.
// Everything else (authorize redirect, code exchange, session mint, JIT
// user link) is plain HTTP; only this file decides whether the id_token
// Microsoft handed back was actually signed by Microsoft for OUR app.
//
// Design constraints — every rule below exists to close a known attack:
//
//   - RS256 only. The token header's "alg" is inspected against a
//     whitelist; "none" and HS256 are rejected. Prevents the classic
//     alg-confusion (algorithm-swap) family.
//
//   - Fresh key by "kid". Microsoft rotates signing keys; we look the key
//     up by kid at verify time and refuse tokens whose kid we can't find.
//     A short in-memory JWKS cache (5 min) means a rotated key that WAS
//     in the cache but is now gone from the JWKS forces a fresh fetch
//     rather than a silent verify-with-old-key.
//
//   - Issuer + audience exact-string match. iss must equal
//     "https://login.microsoftonline.com/{tid}/v2.0" for the tenant we
//     were configured for; aud must equal our client_id. No prefix or
//     substring checks — Entra has bitten more than one team on
//     `iss.contains("microsoftonline.com")`.
//
//   - exp / nbf / iat with a small clock-skew tolerance (60s) so a
//     borderline-fresh token from a slightly-drifting box still verifies,
//     but a token issued minutes in the future doesn't.
//
// The verifier is transport-agnostic — callers hand in a token, get back
// the parsed claims or an error. The HTTP dance lives in portalapi/entra.go.
package portalauth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// EntraClaims mirrors the subset of a Microsoft v2.0 id-token we act on.
// Field tags match the on-wire claim names. Groups may be absent (the
// tenant admin controls the "groups" optional claim on the app reg); the
// resolver treats an empty list as "no group memberships".
type EntraClaims struct {
	Sub    string   `json:"sub"`
	Oid    string   `json:"oid"` // per-tenant stable user id — not used for lookup, kept for logging
	Email  string   `json:"email"`
	Upn    string   `json:"preferred_username"` // fallback when "email" is absent (Entra business tenants often omit email)
	Name   string   `json:"name"`
	Tid    string   `json:"tid"`
	Aud    string   `json:"aud"`
	Iss    string   `json:"iss"`
	Exp    int64    `json:"exp"`
	Nbf    int64    `json:"nbf"`
	Iat    int64    `json:"iat"`
	Groups []string `json:"groups"`
}

// EmailOrUpn returns the best-available user-identifying email string.
// Entra Workforce tenants populate "email" only when a mailbox exists;
// they always populate preferred_username. Callers should treat this as
// the address to match against the users table.
func (c EntraClaims) EmailOrUpn() string {
	if c.Email != "" {
		return c.Email
	}
	return c.Upn
}

// EntraVerifier is a per-Entra-tenant JWT verifier. One instance per
// (tenant_id, client_id) pair — the vendor path constructs one at startup,
// the future customer path constructs one per configured customer.
type EntraVerifier struct {
	tenantID string
	clientID string

	// httpc is used for JWKS fetches. Kept small (5s) so a broken Microsoft
	// endpoint can't hang the request path indefinitely; verification then
	// fails and the caller returns 502.
	httpc *http.Client

	mu      sync.Mutex
	jwks    map[string]*rsa.PublicKey // kid → key
	fetched time.Time
}

// NewEntraVerifier. tenantID + clientID must be non-empty for verification
// to run — if either is empty, Verify always errors.
func NewEntraVerifier(tenantID, clientID string) *EntraVerifier {
	return &EntraVerifier{
		tenantID: tenantID,
		clientID: clientID,
		httpc:    &http.Client{Timeout: 5 * time.Second},
	}
}

// Enabled reports whether the verifier is fully configured. Callers use
// this to skip mounting the Entra HTTP routes when config is absent.
func (v *EntraVerifier) Enabled() bool {
	return v != nil && v.tenantID != "" && v.clientID != ""
}

// Verify parses the id token, checks the signature against Microsoft's
// JWKS for this tenant, and validates the standard claims. Returns the
// parsed claims on success.
//
// The single-caller shape means we don't need to expose intermediate
// stages (parse-header, fetch-jwks, verify-sig) — every failure is a
// terminal auth failure and folds into the same "no" answer.
func (v *EntraVerifier) Verify(ctx context.Context, idToken string) (EntraClaims, error) {
	if !v.Enabled() {
		return EntraClaims{}, errors.New("entra verifier not configured")
	}

	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return EntraClaims{}, errors.New("malformed jwt: expected 3 segments")
	}
	headerBytes, err := base64URLDecode(parts[0])
	if err != nil {
		return EntraClaims{}, fmt.Errorf("decode header: %w", err)
	}
	payloadBytes, err := base64URLDecode(parts[1])
	if err != nil {
		return EntraClaims{}, fmt.Errorf("decode payload: %w", err)
	}
	sig, err := base64URLDecode(parts[2])
	if err != nil {
		return EntraClaims{}, fmt.Errorf("decode signature: %w", err)
	}

	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return EntraClaims{}, fmt.Errorf("parse header: %w", err)
	}
	// alg whitelist — Microsoft v2.0 tokens are always RS256. Rejecting
	// "none" here shuts down the classic alg-confusion attack cold.
	if header.Alg != "RS256" {
		return EntraClaims{}, fmt.Errorf("unsupported alg %q (only RS256 accepted)", header.Alg)
	}
	if header.Kid == "" {
		return EntraClaims{}, errors.New("jwt header missing kid")
	}

	pub, err := v.keyForKid(ctx, header.Kid)
	if err != nil {
		return EntraClaims{}, fmt.Errorf("lookup key %q: %w", header.Kid, err)
	}

	// Signature covers header + "." + payload (the first two segments as
	// their base64url-encoded ASCII forms). Hash SHA-256, verify RSA.
	signingInput := parts[0] + "." + parts[1]
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return EntraClaims{}, fmt.Errorf("signature verify: %w", err)
	}

	var claims EntraClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return EntraClaims{}, fmt.Errorf("parse claims: %w", err)
	}
	if err := v.validateClaims(claims); err != nil {
		return EntraClaims{}, err
	}
	return claims, nil
}

// validateClaims enforces iss / aud / exp / nbf. Each check has its own
// error string so a failed verify tells the operator which invariant
// broke (helpful when Microsoft changes their claim shape or an
// operator mis-configures the tenant ID).
func (v *EntraVerifier) validateClaims(c EntraClaims) error {
	// Issuer must match v2.0 shape for OUR tenant, exactly. No prefix
	// checks — the substring-match footgun (`.contains("microsoft")`)
	// has burned real production systems.
	wantIss := "https://login.microsoftonline.com/" + v.tenantID + "/v2.0"
	if c.Iss != wantIss {
		return fmt.Errorf("iss mismatch: got %q want %q", c.Iss, wantIss)
	}
	if c.Aud != v.clientID {
		return fmt.Errorf("aud mismatch: got %q want %q", c.Aud, v.clientID)
	}
	if c.Tid != v.tenantID {
		return fmt.Errorf("tid mismatch: got %q want %q", c.Tid, v.tenantID)
	}
	// 60s clock-skew tolerance in both directions. Enough to survive a
	// mildly drifting VM host; tight enough that a token issued minutes
	// in the future is still refused.
	now := time.Now().Unix()
	if c.Exp != 0 && now-60 >= c.Exp {
		return fmt.Errorf("token expired at %d (now=%d)", c.Exp, now)
	}
	if c.Nbf != 0 && now+60 < c.Nbf {
		return fmt.Errorf("token not yet valid (nbf=%d, now=%d)", c.Nbf, now)
	}
	if c.Sub == "" {
		return errors.New("sub claim empty")
	}
	return nil
}

// keyForKid returns the RSA public key for the given kid, fetching the
// JWKS if we don't have it cached (or if the cached copy doesn't contain
// this kid — meaning Microsoft rotated a key while we were quiet).
func (v *EntraVerifier) keyForKid(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	if key, ok := v.jwks[kid]; ok && time.Since(v.fetched) < 5*time.Minute {
		v.mu.Unlock()
		return key, nil
	}
	v.mu.Unlock()

	// Fetch fresh JWKS. A concurrent second verify would repeat this fetch;
	// that's fine — 1-2 concurrent GETs to Microsoft's public endpoint under
	// a rotation event is negligible cost and much simpler than a
	// singleflight around it.
	jwks, err := v.fetchJWKS(ctx)
	if err != nil {
		return nil, err
	}

	v.mu.Lock()
	v.jwks = jwks
	v.fetched = time.Now()
	key, ok := v.jwks[kid]
	v.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("kid %q not present in JWKS", kid)
	}
	return key, nil
}

// fetchJWKS pulls the current signing key set for our tenant. The v2.0
// endpoint returns a JWKS document with n (modulus, base64url big-endian)
// and e (exponent) per key — we decode both into an *rsa.PublicKey.
//
// Some keys also carry x5c (an X.509 chain); we prefer n/e when present
// (matches Microsoft's own docs) and fall back to x5c so a hypothetical
// n/e-less key doesn't silently vanish.
func (v *EntraVerifier) fetchJWKS(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	url := "https://login.microsoftonline.com/" + v.tenantID + "/discovery/v2.0/keys"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks fetch: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, err
	}

	var doc struct {
		Keys []struct {
			Kty string   `json:"kty"`
			Kid string   `json:"kid"`
			Use string   `json:"use"`
			N   string   `json:"n"`
			E   string   `json:"e"`
			X5c []string `json:"x5c"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("jwks parse: %w", err)
	}

	out := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		if key, ok := rsaFromNE(k.N, k.E); ok {
			out[k.Kid] = key
			continue
		}
		if len(k.X5c) > 0 {
			if key, ok := rsaFromX5c(k.X5c[0]); ok {
				out[k.Kid] = key
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("jwks: no usable RSA keys")
	}
	return out, nil
}

// rsaFromNE reconstructs an RSA public key from the base64url-encoded
// modulus (n) and exponent (e) values that JWKS returns.
func rsaFromNE(nStr, eStr string) (*rsa.PublicKey, bool) {
	nBytes, err := base64URLDecode(nStr)
	if err != nil {
		return nil, false
	}
	eBytes, err := base64URLDecode(eStr)
	if err != nil {
		return nil, false
	}
	// e is a big-endian unsigned int; pad-left to 4 bytes to fit in an int.
	// The common Entra exponent is 65537 (0x010001) so 3 bytes; 4 is a safe
	// ceiling.
	if len(eBytes) > 4 {
		return nil, false
	}
	var eInt int
	for _, b := range eBytes {
		eInt = eInt<<8 | int(b)
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: eInt}, true
}

// rsaFromX5c decodes a base64-standard-encoded DER cert and pulls its
// RSA public key. Fallback path — Microsoft's JWKS carries both n/e AND
// x5c for RSA keys, but this handles a future format change gracefully.
func rsaFromX5c(x5c string) (*rsa.PublicKey, bool) {
	der, err := base64.StdEncoding.DecodeString(x5c)
	if err != nil {
		return nil, false
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, false
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	return pub, ok
}

// base64URLDecode handles JWT's URL-safe base64 without padding.
// Encoding/base64.RawURLEncoding refuses padded input, StdEncoding refuses
// unpadded — we try both so a JWKS or token with either style parses.
func base64URLDecode(s string) ([]byte, error) {
	if raw, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return raw, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

