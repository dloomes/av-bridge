-- Display links: a read-only link that shows one or more shared dashboards
-- on a wall screen, rotating between them, without anyone signed in.
-- The link carries a token (only its hash is stored) that signs in as a
-- display: view-only permissions, the creator's physical scope, and only
-- the dashboards listed here. Revoking it stops the screen at once.

CREATE TABLE dashboard_display_links (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id     uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    name            text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 80),
    token_hash      text NOT NULL UNIQUE,
    dashboard_ids   uuid[] NOT NULL CHECK (cardinality(dashboard_ids) BETWEEN 1 AND 10),
    rotate_seconds  int  NOT NULL DEFAULT 60 CHECK (rotate_seconds BETWEEN 10 AND 3600),
    -- The creator's physical scope, so a link never sees more than they
    -- could: {"business_units": [...], "regions": [...], "locations": [...],
    -- "buildings": [...], "rooms": [...]}; {} = the whole organisation.
    scope           jsonb NOT NULL DEFAULT '{}',
    created_by      text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    last_used_at    timestamptz,
    revoked_at      timestamptz
);

CREATE INDEX dashboard_display_links_customer_idx ON dashboard_display_links (customer_id, created_at DESC);

ALTER TABLE dashboard_display_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE dashboard_display_links FORCE  ROW LEVEL SECURITY;
GRANT SELECT, INSERT, UPDATE, DELETE ON dashboard_display_links TO app_tenant, app_admin;
CREATE POLICY tenant_isolation ON dashboard_display_links
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);
