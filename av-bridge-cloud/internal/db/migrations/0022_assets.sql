-- Slice 9: CMDB — the assets table + link from devices.
--
-- An asset row represents a physical thing the tenant owns and cares to
-- track: a display, a mount, a cable, a remote, a chair with an integrated
-- touchscreen. Some assets are monitored (there's a matching devices row
-- that a collector polls) and some are not (a wall mount just sits there).
--
-- Devices existed before assets, so we let a device stand alone with a
-- nullable asset_id FK. Existing tenants keep working; over time customers
-- backfill asset rows for their fleet. New devices can optionally point at
-- an asset on create; there's no automatic device→asset creation because
-- the AV team may want to record extra provenance (cost, warranty,
-- supplier) that the bridge doesn't know about.
--
--   room_id           — nullable. In-storage / retired / decommissioned assets
--                       have no room. Physical-scope RBAC treats a NULL room
--                       the same as "invisible to scoped users" (a scoped
--                       user shouldn't see the storage cupboard's contents).
--   asset_tag         — customer-assigned inventory tag ("AV-042"). Unique
--                       per tenant when supplied; nullable so quick-add
--                       flows don't need one upfront.
--   category          — deliberately a free text field with an application-
--                       layer allowlist rather than a CHECK constraint.
--                       Categories will grow over time (custom-category
--                       feature down the line) and every DB migration for a
--                       new category would be silly.
--   status            — small stable enum; CHECK constraint is fine here.
--
-- Purchase / warranty dates live on the same row for straightforward
-- reporting. If richer finance data lands later, promote to a separate
-- asset_finance table rather than widening this one.

CREATE TABLE assets (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id    uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    room_id        uuid REFERENCES rooms(id) ON DELETE SET NULL,
    asset_tag      text,
    name           text NOT NULL,
    category       text NOT NULL,
    manufacturer   text,
    model          text,
    serial_number  text,
    status         text NOT NULL DEFAULT 'in_service'
        CHECK (status IN ('in_service', 'in_storage', 'retired', 'in_repair')),
    purchase_date  date,
    warranty_end   date,
    notes          text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Unique asset_tag per tenant when set — partial index handles the NULL case
-- correctly (NULL != NULL in SQL, so two assets can both have asset_tag=NULL).
CREATE UNIQUE INDEX assets_customer_asset_tag_idx
    ON assets (customer_id, asset_tag)
    WHERE asset_tag IS NOT NULL;

-- Common read path: list assets in a customer, optionally filtered by room.
CREATE INDEX assets_customer_room_idx
    ON assets (customer_id, room_id);

-- Full-text search is out of scope for MVP; asset_tag + serial_number
-- lookups use a plain index. name search rides ILIKE on the customer feed.
CREATE INDEX assets_customer_serial_idx
    ON assets (customer_id, serial_number)
    WHERE serial_number IS NOT NULL;

-- updated_at auto-touch — used by the portal to show "last edited" hints.
CREATE OR REPLACE FUNCTION assets_touch_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER assets_updated_at_trigger
    BEFORE UPDATE ON assets
    FOR EACH ROW
    EXECUTE FUNCTION assets_touch_updated_at();

ALTER TABLE assets ENABLE ROW LEVEL SECURITY;
ALTER TABLE assets FORCE ROW LEVEL SECURITY;

GRANT SELECT, INSERT, UPDATE, DELETE ON assets TO app_tenant, app_admin;

CREATE POLICY tenant_isolation ON assets
  USING       (customer_id = current_setting('app.current_customer', true)::uuid)
  WITH CHECK  (customer_id = current_setting('app.current_customer', true)::uuid);

-- Physical scope — mirrors the device pattern in 0019. Unplaced (room_id
-- IS NULL) assets are invisible to scoped users; unscoped admins see them
-- alongside placed rows.
CREATE POLICY building_scope_assets ON assets
    AS RESTRICTIVE
    USING (
        current_setting('app.building_scope', true) = ''
        OR (
            room_id IS NOT NULL
            AND EXISTS (
                SELECT 1 FROM rooms r
                WHERE r.id = assets.room_id
                  AND r.building_id::text = ANY(string_to_array(current_setting('app.building_scope', true), ','))
            )
        )
    );

-- Devices can point at their canonical asset row. NULL is fine — a device
-- without an asset row is the pre-CMDB norm and will keep working.
-- ON DELETE SET NULL: if an asset is deleted, its device (if any) stays
-- monitored — the collector doesn't care about the CMDB.
ALTER TABLE devices
    ADD COLUMN asset_id uuid REFERENCES assets(id) ON DELETE SET NULL;

CREATE INDEX devices_asset_id_idx
    ON devices (asset_id)
    WHERE asset_id IS NOT NULL;

-- New permissions. Seed onto existing admin roles so the admin bundle
-- picks up the new keys. seedSystemRoles() in db/customers.go mirrors
-- this for future customers — keep both in sync.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, p.permission
  FROM roles r
 CROSS JOIN (VALUES ('view.assets'), ('asset.crud')) AS p(permission)
 WHERE r.name = 'admin' AND r.is_system_default
   AND NOT EXISTS (
       SELECT 1 FROM role_permissions rp
        WHERE rp.role_id = r.id AND rp.permission = p.permission
   );

-- Operators + viewers get view.assets so they can see inventory in the
-- portal without being able to edit it. Only admin gets asset.crud.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, 'view.assets'
  FROM roles r
 WHERE r.name IN ('operator', 'viewer') AND r.is_system_default
   AND NOT EXISTS (
       SELECT 1 FROM role_permissions rp
        WHERE rp.role_id = r.id AND rp.permission = 'view.assets'
   );
