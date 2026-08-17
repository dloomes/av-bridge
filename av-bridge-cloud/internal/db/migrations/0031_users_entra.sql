-- 0031_users_entra.sql — Entra ID sign-in support on the users table.
--
-- Existing users (all local, all with a bcrypt password_hash) keep working
-- unchanged. This migration adds the fields needed for a second sign-in
-- provider — Microsoft Entra ID — and relaxes password_hash so an Entra-only
-- user can exist without one.
--
-- Design decisions:
--
--   1. provider column, default 'local'. Every existing row backfills as
--      local — no code path currently checks provider, so old code stays
--      correct until the OIDC handlers ship.
--
--   2. entra_sub column stores the stable Entra `sub` claim (a per-app,
--      per-user opaque identifier — NOT the user's email or oid). Nullable;
--      only entra-provider rows populate it.
--
--   3. Uniqueness: entra_sub is unique per tenant scope (customer_id OR
--      vendor_tenant_id), NOT globally. Two customers can't share a
--      subscription anyway, but scoping the uniqueness keeps the invariant
--      aligned with the rest of the user table.
--
--   4. First-time link semantics: a user who exists as local (seeded, e.g.
--      the vendor admin) MUST link cleanly on their first Entra sign-in —
--      the callback handler finds them by (scope, lower(email)) and updates
--      entra_sub on that row, without duplicating. No schema constraint
--      enforces link-on-existence — it's handler logic; the migration only
--      permits the shape.
--
--   5. password_hash becomes nullable. Existing rows keep their bcrypt hash;
--      pure-Entra users have NULL. Login handler already refuses NULL by
--      bcrypt failing on the sentinel hash it uses on the miss path — we
--      also add an explicit early-return in the code to short-circuit.
--
--   6. New CHECK: a row must have EITHER a password_hash (local login
--      possible) OR entra_sub (Entra login possible) OR both (a local user
--      who has since linked their Entra identity — coexistence). A row with
--      neither can never be authenticated and shouldn't exist.

ALTER TABLE users
    ADD COLUMN provider  text NOT NULL DEFAULT 'local'
        CHECK (provider IN ('local', 'entra')),
    ADD COLUMN entra_sub text;

ALTER TABLE users
    ALTER COLUMN password_hash DROP NOT NULL;

ALTER TABLE users
    ADD CONSTRAINT users_auth_material_present CHECK (
        password_hash IS NOT NULL OR entra_sub IS NOT NULL
    );

-- Look-up index: given (vendor_tenant_id | customer_id, entra_sub) return
-- the user row. Two partial unique indexes so nulls in the other scope
-- column don't collide.
CREATE UNIQUE INDEX users_vendor_entra_sub_idx
    ON users (vendor_tenant_id, entra_sub)
    WHERE vendor_tenant_id IS NOT NULL AND entra_sub IS NOT NULL;

CREATE UNIQUE INDEX users_customer_entra_sub_idx
    ON users (customer_id, entra_sub)
    WHERE customer_id IS NOT NULL AND entra_sub IS NOT NULL;
