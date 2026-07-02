-- 0014_firmware_targets.sql — per-customer firmware reference data.
--
-- Two things live on this row:
--   docs_url        — link to the vendor's release notes for this
--                     (make, model). Rendered on the firmware page so a
--                     customer's admin can "go check the vendor" without
--                     the platform pretending to know what's current.
--   target_version  — optional. When set, the firmware endpoint compares
--                     each device's firmware_version against this value
--                     to badge outdated / current. Leaving it NULL keeps
--                     the page honest: no fleet-heuristic "outdated" flag,
--                     just the actual versions present.
--
-- Keyed by (customer_id, make, model) — unique per customer so different
-- tenants can maintain their own approved-firmware policy independently.

CREATE TABLE firmware_targets (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id    uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    make           text NOT NULL,
    model          text NOT NULL,
    target_version text,
    docs_url       text,
    notes          text,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     text,
    UNIQUE (customer_id, make, model)
);

CREATE INDEX firmware_targets_customer_idx ON firmware_targets (customer_id);

ALTER TABLE firmware_targets ENABLE ROW LEVEL SECURITY;
ALTER TABLE firmware_targets FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation_firmware_targets ON firmware_targets
    USING (customer_id::text = current_setting('app.current_customer', true));

GRANT SELECT, INSERT, UPDATE, DELETE ON firmware_targets TO app_tenant;
-- Admin pool doesn't read this table today; keep it tenant-only so a
-- cross-tenant admin query can't accidentally leak firmware policies.
