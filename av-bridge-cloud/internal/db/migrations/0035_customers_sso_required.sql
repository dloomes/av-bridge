-- 0035_customers_sso_required.sql — SSO-only enforcement per customer (M4).
--
-- Flag that, when true, makes Entra the ONLY sign-in path for the customer's
-- users. Local password login, admin-initiated password reset, self-serve
-- password reset via email, and creating new local users are all refused
-- when this is set. Existing users with a password_hash keep the hash on
-- their row (it just can't be used), so a customer that flips the toggle
-- OFF later gets those passwords back.
--
-- Default false so pre-M4 tenants keep their existing dual-auth behaviour.
-- Enabling is guarded at the API layer:
--   * customer must have entra_tenant_id set (otherwise nobody could
--     sign in — pure lockout)
--   * cloud must be running with customer Entra SSO configured
--     (customerSSOEnabled at boot)
-- Both are UI-only checks; nothing enforces them at the DB level because a
-- vendor admin acting from a properly-configured cloud can flip the flag
-- for any customer whose Entra tenant they know is correct.

ALTER TABLE customers
    ADD COLUMN sso_required boolean NOT NULL DEFAULT false;
