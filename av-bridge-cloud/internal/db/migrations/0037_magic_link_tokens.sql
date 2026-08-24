-- 0037_magic_link_tokens.sql — vendor-issued break-glass sign-in tokens (M4.1).
--
-- Vendor helpdesk admins mint one of these when someone locked themselves
-- out (SSO-only tenant + broken Entra, forgotten password + no email
-- inbox access, etc.). Consumption mints a normal `av_` session for the
-- target user and redirects to their branded /sign-in/callback — the
-- portal reads the token from the URL exactly as though the user had
-- signed in via password or Entra.
--
-- Distinct from password_reset_tokens (0033) because the intent differs:
--   * password reset REPLACES the user's password_hash — the user then
--     signs in as usual.
--   * magic link is a one-shot session — no password mutation, works
--     regardless of whether the user has a password, works on SSO-only
--     tenants (that's the whole point).
--
-- Security shape mirrors password_reset_tokens:
--   * Raw 32-byte token → stored as SHA-256 hash. DB dump doesn't leak.
--   * Single-use: used_at gates the redeem UPDATE.
--   * 15-minute TTL by default (shorter than reset — this is an
--     interactive vendor→user handoff, not an email round-trip).
--   * issued_by tracks the vendor user who minted the token so the
--     audit trail names them, not just "someone".
--
-- No RLS: like every other auth table, this must be lookupable BEFORE a
-- Principal exists. Isolation is enforced by the redeem UPDATE scoping.

CREATE TABLE magic_link_tokens (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash     text NOT NULL UNIQUE,
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL,
    used_at        timestamptz,
    issued_by      uuid REFERENCES users(id) ON DELETE SET NULL,
    requester_ip   text,
    user_agent     text
);

-- Fast lookup for the "any recent unused token?" cooldown check on the
-- mint path — vendor admin double-clicks the button, we want the second
-- click to reuse the first (or at least not spam sessions).
CREATE INDEX magic_link_tokens_user_active_idx
    ON magic_link_tokens (user_id, created_at DESC)
 WHERE used_at IS NULL;

CREATE INDEX magic_link_tokens_expires_idx
    ON magic_link_tokens (expires_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON magic_link_tokens TO app_admin;
