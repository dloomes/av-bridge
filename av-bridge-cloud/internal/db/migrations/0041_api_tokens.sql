-- 0041_api_tokens.sql — customer-issued Public API tokens (v1: read-only).
--
-- Distinct from user session tokens (0033/0037/0038):
--   * Session tokens are bound to a user, expire in 24h by default, and
--     confer a Principal with whatever roles that user holds.
--   * API tokens are bound to a customer + a fixed scope list. They are
--     minted by an admin from the portal, kept in a machine at the
--     integrating system's end, and carry a token_prefix for display
--     so an operator can identify which key is which without ever
--     seeing the secret again after creation.
--
-- Token layout, chosen at the API layer (not enforced by SQL):
--   avb_<8-hex prefix>_<48-hex secret>
--   └── stored as SHA-256(prefix + "_" + secret) in token_hash
--   └── prefix is displayed in the portal list ("avb_a1b2c3d4…")
--   └── raw token is returned exactly once at create time
--
-- Scope model:
--   scopes text[] — subset of KnownPermissions the token confers when
--   presented at /pub/v1/*. v1 restricts the allowlist at the handler
--   layer to view.* keys only; write scopes land in a later slice with
--   real per-endpoint gating.
--
-- No RLS on api_tokens itself — the auth layer looks up a token BEFORE
-- a Principal exists (same reasoning as the other auth tables). Every
-- SELECT from the resolver runs under app_admin. Portal CRUD reads its
-- own tenant's rows via an explicit customer_id filter on top of RLS
-- (below), which handles the /api/v1/api-tokens list/patch/delete path.

CREATE TABLE api_tokens (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id    uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    name           text NOT NULL,
    token_prefix   text NOT NULL,
    token_hash     text NOT NULL UNIQUE,
    scopes         text[] NOT NULL DEFAULT '{}',
    created_by     uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    last_used_at   timestamptz,
    last_used_ip   text,
    expires_at     timestamptz,
    revoked_at     timestamptz,
    revoked_by     uuid REFERENCES users(id) ON DELETE SET NULL
);

-- Fast auth-layer lookup: SELECT ... WHERE token_hash = $1. The UNIQUE
-- constraint above already creates an index; explicit note only.

-- Portal listing: WHERE customer_id AND revoked_at IS NULL ORDER BY
-- created_at DESC. Partial index keeps the "active tokens" scan tight
-- while revoked rows stay for audit.
CREATE INDEX api_tokens_customer_active_idx
    ON api_tokens (customer_id, created_at DESC)
 WHERE revoked_at IS NULL;

-- RLS: portal-side reads run as app_tenant under a customer scope; the
-- auth-layer resolver runs as app_admin (BYPASSRLS). Same shape as the
-- rest of the tenant tables.
ALTER TABLE api_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_tokens FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON api_tokens
  USING       (customer_id = current_setting('app.current_customer', true)::uuid)
  WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON api_tokens TO app_admin, app_tenant;

-- Seed api_token.view + api_token.manage onto every existing admin
-- system-default role. New customers get them via seedSystemRoles() —
-- keep the two paths in sync. Operator + viewer don't get either by
-- default; managing programmatic access to the tenant is an admin
-- capability.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, p
  FROM roles r
  CROSS JOIN (VALUES ('api_token.view'), ('api_token.manage')) AS v(p)
 WHERE r.name = 'admin' AND r.is_system_default
   AND NOT EXISTS (
       SELECT 1 FROM role_permissions rp
        WHERE rp.role_id = r.id AND rp.permission = v.p
   );
