-- 0015_local_users.sql — local username+password auth.
--
-- Non-Entra sign-in path. Customers who don't want to (or can't yet) federate
-- with Entra can log in with an email + password stored here. Passwords are
-- bcrypt hashes; sessions are opaque tokens (`av_<hex>`) whose SHA-256 hash
-- is stored server-side so a DB read alone can't impersonate a live user.
--
-- Users live in one of two scopes: a customer (tenant-scoped like the Entra
-- path) or a vendor tenant (cross-tenant helpdesk). The CHECK constraint
-- enforces exactly one of customer_id / vendor_tenant_id is set — same shape
-- as role_mappings so the two paths compose cleanly.
--
-- Both tables live outside RLS. Login has to look up the user *before* it
-- knows which tenant they belong to, and session lookup happens on every
-- authenticated request. Tenant isolation is enforced by the resolver setting
-- Principal.CustomerID correctly, not by the users/sessions rows themselves.

CREATE TABLE users (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email             text NOT NULL,
    password_hash     text NOT NULL,               -- bcrypt
    full_name         text,
    customer_id       uuid REFERENCES customers(id) ON DELETE CASCADE,
    vendor_tenant_id  uuid REFERENCES vendor_tenants(id) ON DELETE CASCADE,
    role              text NOT NULL CHECK (role IN ('admin','operator','viewer')),
    disabled_at       timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    last_login_at     timestamptz,
    CHECK (
        (customer_id IS NOT NULL AND vendor_tenant_id IS NULL)
        OR
        (customer_id IS NULL AND vendor_tenant_id IS NOT NULL)
    )
);

-- Email uniqueness is scoped: two customers can each have a user
-- alice@example.com without colliding. Vendor emails are globally unique
-- across the vendor tenant.
CREATE UNIQUE INDEX users_customer_email_idx
    ON users (customer_id, lower(email)) WHERE customer_id IS NOT NULL;
CREATE UNIQUE INDEX users_vendor_email_idx
    ON users (vendor_tenant_id, lower(email)) WHERE vendor_tenant_id IS NOT NULL;

-- Sessions: opaque token, hashed at rest. token_hash is SHA-256(token) so a
-- DB dump does not leak live tokens. Absolute TTL — no rolling refresh yet.
CREATE TABLE user_sessions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash    text NOT NULL UNIQUE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    revoked_at    timestamptz,
    user_agent    text,
    ip_address    text
);

CREATE INDEX user_sessions_user_idx    ON user_sessions (user_id);
CREATE INDEX user_sessions_expires_idx ON user_sessions (expires_at);

-- Resolver + login handler both live in the cloud's admin scope (they have to
-- read users cross-tenant before a Principal exists). No RLS.
GRANT SELECT, INSERT, UPDATE, DELETE ON users         TO app_admin;
GRANT SELECT, INSERT, UPDATE, DELETE ON user_sessions TO app_admin;
