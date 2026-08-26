package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/pubapi"
	"github.com/jackc/pgx/v5"
)

// Public API token management — portal-side CRUD.
//
// The public API itself lives in internal/pubapi; this file is the
// admin surface for provisioning + revoking tokens from the portal.
// A token is a long-lived tenant credential — the format, scopes, and
// hashing scheme are owned by pubapi (see pubapi/tokens.go). We call
// through to pubapi.GenerateToken to keep the wire format in one
// place.
//
// Endpoint list:
//
//   GET    /api/v1/api-tokens          — list (never returns raw secrets)
//   POST   /api/v1/api-tokens          — mint, returns raw token EXACTLY ONCE
//   DELETE /api/v1/api-tokens/{id}     — revoke (soft-delete; row kept for audit)
//
// v1 scope allowlist is view.* only. A caller trying to request e.g.
// device.crud gets a 400 — the portal UI hides the option too. Wider
// scopes land in a later slice with per-endpoint gating.

// apiTokenRow is the JSON shape the portal renders — omits the token
// hash and any material that could recreate the secret. LastUsedAt +
// LastUsedIP are the two fields an operator cares about most when
// spotting stale keys.
type apiTokenRow struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	TokenPrefix  string     `json:"token_prefix"`
	Scopes       []string   `json:"scopes"`
	CreatedAt    time.Time  `json:"created_at"`
	CreatedBy    string     `json:"created_by,omitempty"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP   string     `json:"last_used_ip,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	RevokedBy    string     `json:"revoked_by,omitempty"`
}

// ListAPITokens — GET /api/v1/api-tokens
//
// Returns every token in the caller's tenant, revoked or not. The
// portal filters revoked separately so an operator can browse the
// history when auditing a suspicious call from an old key. Ordered
// active-first, most-recent-first.
func (h *Handler) ListAPITokens(w http.ResponseWriter, r *http.Request) {
	out := []apiTokenRow{}
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT t.id::text, t.name, t.token_prefix,
			       COALESCE(t.scopes, '{}'::text[]),
			       t.created_at,
			       COALESCE(cu.email, ''),
			       t.last_used_at, COALESCE(t.last_used_ip, ''),
			       t.expires_at, t.revoked_at,
			       COALESCE(ru.email, '')
			  FROM api_tokens t
			  LEFT JOIN users cu ON cu.id = t.created_by
			  LEFT JOIN users ru ON ru.id = t.revoked_by
			 ORDER BY (t.revoked_at IS NULL) DESC,
			          t.created_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rr apiTokenRow
			if err := rows.Scan(
				&rr.ID, &rr.Name, &rr.TokenPrefix, &rr.Scopes,
				&rr.CreatedAt, &rr.CreatedBy,
				&rr.LastUsedAt, &rr.LastUsedIP,
				&rr.ExpiresAt, &rr.RevokedAt, &rr.RevokedBy,
			); err != nil {
				return err
			}
			out = append(out, rr)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type createAPITokenReq struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresAt *string  `json:"expires_at,omitempty"` // RFC3339; nil = no expiry
}

type createAPITokenResp struct {
	apiTokenRow
	// Token is the raw secret. Returned EXACTLY ONCE — the portal
	// UI shows it in a one-time-reveal panel with a copy button; the
	// hash on the row can't be reversed.
	Token string `json:"token"`
}

// v1PublicAPIAllowedScopes gates what a token can be minted with.
// Read-only in v1 — write scopes need per-endpoint gating that we
// haven't shipped yet. Anything outside this set is a 400 at mint
// time. Kept as a small map (not the portalauth catalogue) so it
// changes independently of the tenant-side RBAC.
var v1PublicAPIAllowedScopes = map[string]struct{}{
	portalauth.PermViewDashboard:     {},
	portalauth.PermViewAssets:        {},
	portalauth.PermViewFirmware:      {},
	portalauth.PermViewNotifications: {},
	portalauth.PermViewAudit:         {},
	portalauth.PermViewReports:       {},
	portalauth.PermNightlyView:       {},
}

// CreateAPIToken — POST /api/v1/api-tokens
func (h *Handler) CreateAPIToken(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	var req createAPITokenReq
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
		writeErr(w, http.StatusBadRequest, "name is too long (max 200 chars)")
		return
	}
	if len(req.Scopes) == 0 {
		writeErr(w, http.StatusBadRequest, "at least one scope is required")
		return
	}
	// De-dupe + validate. A duplicate scope isn't harmful, but the
	// stored array shouldn't carry noise.
	scopeSet := make(map[string]struct{}, len(req.Scopes))
	for _, s := range req.Scopes {
		s = strings.TrimSpace(s)
		if _, allowed := v1PublicAPIAllowedScopes[s]; !allowed {
			writeErr(w, http.StatusBadRequest,
				"unsupported scope: "+s+" (v1 supports view.* scopes only)")
			return
		}
		scopeSet[s] = struct{}{}
	}
	scopes := make([]string, 0, len(scopeSet))
	for s := range scopeSet {
		scopes = append(scopes, s)
	}

	var expiresAt *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "expires_at must be RFC3339")
			return
		}
		if t.Before(time.Now().Add(time.Minute)) {
			writeErr(w, http.StatusBadRequest, "expires_at must be at least a minute in the future")
			return
		}
		expiresAt = &t
	}

	raw, prefix, hash, err := pubapi.GenerateToken()
	if err != nil {
		h.log.Error("api-token create: gen failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	var (
		row       apiTokenRow
		createdBy string
	)
	if p.UserID != "" {
		createdBy = p.UserID
	}
	ok = h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO api_tokens
			    (customer_id, name, token_prefix, token_hash, scopes, created_by, expires_at)
			VALUES (current_setting('app.current_customer', true)::uuid,
			        $1, $2, $3, $4, NULLIF($5::text,'')::uuid, $6)
			RETURNING id::text, created_at`,
			req.Name, prefix, hash, scopes, createdBy, expiresAt,
		).Scan(&row.ID, &row.CreatedAt)
	})
	if !ok {
		return
	}
	row.Name = req.Name
	row.TokenPrefix = prefix
	row.Scopes = scopes
	row.ExpiresAt = expiresAt
	row.CreatedBy = p.Email

	// Audit — never log the raw token or hash. Prefix + name are
	// enough context for an operator retracing what was minted.
	_ = h.store.WithTenantScoped(r.Context(), p.CustomerID, principalScope(p), func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "api_token.create",
			TargetKind: "api_token", TargetID: row.ID,
			After: mustJSON(map[string]any{
				"name":         req.Name,
				"token_prefix": prefix,
				"scopes":       scopes,
				"expires_at":   expiresAt,
			}),
		}))
	})

	writeJSON(w, http.StatusCreated, createAPITokenResp{
		apiTokenRow: row,
		Token:       raw,
	})
}

// RevokeAPIToken — DELETE /api/v1/api-tokens/{id}
//
// Soft-delete: sets revoked_at + revoked_by so the row stays for audit
// and history browsing. The pubapi resolver treats any non-null
// revoked_at as invalid immediately.
func (h *Handler) RevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireCustomerScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "token id required")
		return
	}

	var tokName, tokPrefix string
	notFound := false
	alreadyRevoked := false
	ok = h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			UPDATE api_tokens
			   SET revoked_at = now(),
			       revoked_by = NULLIF($2::text,'')::uuid
			 WHERE id = $1
			   AND revoked_at IS NULL
			 RETURNING name, token_prefix`,
			id, p.UserID,
		).Scan(&tokName, &tokPrefix)
		if errors.Is(err, pgx.ErrNoRows) {
			// Distinguish "gone" from "already revoked" so the caller
			// gets a useful message on a double-click. One extra query,
			// still inside the RLS scope.
			var exists bool
			if e2 := tx.QueryRow(ctx, `SELECT true FROM api_tokens WHERE id = $1`, id).Scan(&exists); errors.Is(e2, pgx.ErrNoRows) {
				notFound = true
				return nil
			}
			alreadyRevoked = true
			return nil
		}
		return err
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "api token not found")
		return
	}
	if alreadyRevoked {
		writeErr(w, http.StatusConflict, "api token already revoked")
		return
	}

	_ = h.store.WithTenantScoped(r.Context(), p.CustomerID, principalScope(p), func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "api_token.revoke",
			TargetKind: "api_token", TargetID: id,
			After: mustJSON(map[string]any{
				"name":         tokName,
				"token_prefix": tokPrefix,
			}),
		}))
	})

	w.WriteHeader(http.StatusNoContent)
}
