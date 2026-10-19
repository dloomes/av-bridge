-- Customisable dashboards: a named, ordered layout of widgets. Each user
-- can build their own (owner_user_id set); admins with dashboard.share
-- can publish shared ones the whole organisation can view or copy
-- (owner_user_id NULL). The layout is the portal's widget list, validated
-- by the API (portalapi/dashboards.go); widgets fetch their own data
-- through the normal, scope-filtered endpoints, so a shared dashboard
-- never shows a viewer more than their access allows.

CREATE TABLE dashboards (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id    uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    owner_user_id  uuid,                         -- NULL = shared with the organisation
    name           text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 80),
    layout         jsonb NOT NULL DEFAULT '{"widgets": []}',
    created_by     text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX dashboards_customer_owner_idx ON dashboards (customer_id, owner_user_id);

ALTER TABLE dashboards ENABLE ROW LEVEL SECURITY;
ALTER TABLE dashboards FORCE  ROW LEVEL SECURITY;
GRANT SELECT, INSERT, UPDATE, DELETE ON dashboards TO app_tenant, app_admin;
CREATE POLICY tenant_isolation ON dashboards
    USING       (customer_id = current_setting('app.current_customer', true)::uuid)
    WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

-- Administrators can publish shared dashboards; any custom role can be
-- given it.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, 'dashboard.share'
  FROM roles r
 WHERE r.name = 'admin' AND r.is_system_default
   AND NOT EXISTS (
       SELECT 1 FROM role_permissions rp
        WHERE rp.role_id = r.id AND rp.permission = 'dashboard.share'
   );
