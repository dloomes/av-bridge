-- Slice 8: per-customer branding — logo, accent colour, product name.
--
-- Stored inline on the customers row rather than a separate blob table:
--   * logos are small (< ~200KB — a favicon-sized PNG or an SVG mark),
--   * they're read once at portal boot and cached client-side,
--   * a laptop demo doesn't want a separate blob-store dependency.
--
-- We revisit this when we deploy to prod with real object storage — at that
-- point logo becomes a URL pointing at a bucket, and the bytea column stays
-- around only as a fallback for the on-laptop path.
--
--   logo               — the bytes themselves, capped by the application layer
--                        (there's no CHECK constraint; the handler validates
--                        content-type + size on write).
--   logo_content_type  — image/png / image/jpeg / image/svg+xml. NULL when no
--                        logo is uploaded.
--   accent_color       — hex string like '#3b82f6'. Applied to the portal
--                        theme's --primary + --ring so buttons/links pick up
--                        the customer's brand colour.
--   display_name       — override for the "AV Bridge" wordmark + tab title.
--                        NULL falls back to "AV Bridge" on the client.
--
-- All four columns are nullable so an unbranded customer looks identical to
-- pre-slice-8 rows — no data migration needed for existing tenants.

ALTER TABLE customers
    ADD COLUMN logo              bytea,
    ADD COLUMN logo_content_type text,
    ADD COLUMN accent_color      text,
    ADD COLUMN display_name      text;

-- Applied on write so the portal can trust the value it reads back. Very
-- forgiving pattern — anything that looks like #RGB or #RRGGBB with case
-- variations passes; anything else is a 400 at the handler.
ALTER TABLE customers
    ADD CONSTRAINT customers_accent_color_hex
    CHECK (accent_color IS NULL OR accent_color ~* '^#([0-9a-f]{3}|[0-9a-f]{6})$');

-- Content type allowlist matches what the portal will render safely. SVG is
-- included because customer logos are often vector, but the handler must
-- also strip anything script-y from the SVG bytes before storing (defense
-- in depth against XSS via a <script> tag inside an SVG rendered in an
-- <img>).
ALTER TABLE customers
    ADD CONSTRAINT customers_logo_content_type
    CHECK (logo_content_type IS NULL
        OR logo_content_type IN ('image/png', 'image/jpeg', 'image/svg+xml'));

-- Seed the new 'branding.update' permission onto every existing admin
-- system-default role. New customers created after this migration get it
-- via seedSystemRoles() in db/customers.go — keep both in sync.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, 'branding.update'
  FROM roles r
 WHERE r.name = 'admin' AND r.is_system_default
   AND NOT EXISTS (
       SELECT 1 FROM role_permissions rp
        WHERE rp.role_id = r.id AND rp.permission = 'branding.update'
   );
