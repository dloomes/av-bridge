-- 0038_collector_enrollment_tokens.sql — self-serve collector on-boarding.
--
-- Vendor / customer admin pre-provisions a collectors row from the portal
-- (name, building, notes). The insert also creates a row here — a
-- SHA-256'd one-time token bound to that collector_id. Ops hands the raw
-- token to the engineer or customer, who runs a one-liner on the target
-- Linux box; that script POSTs the token to /public/collectors/enroll
-- and gets back the collector's identity + HMAC secret in the response,
-- which it writes to /etc/av-bridge/env. From that moment on, the
-- bridge phones home with normal HMAC-signed /ingest calls and pulls
-- its device config from /bridge/config.
--
-- Shape mirrors the two prior single-use-token tables (0033 password
-- reset, 0037 magic link): raw token → SHA-256 at rest; used_at gates
-- the redeem UPDATE; expires_at bounds the useful lifetime.
--
-- TTL default: 7 days at the API layer. Site visits get scheduled with
-- lead time; a shorter TTL punishes normal onboarding rhythm. Vendor
-- can always re-mint via POST /api/v1/collectors/{id}/enrollment-token.

CREATE TABLE collector_enrollment_tokens (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    collector_id   uuid NOT NULL REFERENCES collectors(id) ON DELETE CASCADE,
    token_hash     text NOT NULL UNIQUE,
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL,
    used_at        timestamptz,
    issued_by      uuid REFERENCES users(id) ON DELETE SET NULL,
    requester_ip   text,
    user_agent     text
);

-- Fast lookup for the "any unexpired unused token for this collector?"
-- check on the re-mint path. Partial index keeps it small — the used
-- and expired rows are archival.
CREATE INDEX collector_enrollment_tokens_active_idx
    ON collector_enrollment_tokens (collector_id, created_at DESC)
 WHERE used_at IS NULL;

CREATE INDEX collector_enrollment_tokens_expires_idx
    ON collector_enrollment_tokens (expires_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON collector_enrollment_tokens TO app_admin;

-- Seed 'collector.crud' onto every existing admin system-default role.
-- New customers get it via seedSystemRoles() — keep both in sync.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, 'collector.crud'
  FROM roles r
 WHERE r.name = 'admin' AND r.is_system_default
   AND NOT EXISTS (
       SELECT 1 FROM role_permissions rp
        WHERE rp.role_id = r.id AND rp.permission = 'collector.crud'
   );
