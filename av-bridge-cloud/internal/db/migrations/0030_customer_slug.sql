-- Adds a URL slug per customer so the portal sign-in page can be reached at
-- <slug>.<env>.involvecloud.com and render the customer's pre-login branding
-- (logo, display name, accent) via GET /public/branding?slug=<slug>.
--
-- Nullable — existing customers and future customers without a subdomain
-- configured aren't forced to have one; those tenants continue to sign in via
-- the universal app.<env>.involvecloud.com route.
--
-- Format: 3-50 chars, [a-z0-9-], must start AND end with alphanumeric so we
-- never mint "-acme" / "acme-" / "--foo--"-style slugs. Enforced at the app
-- layer too (validator + admin API) but the CHECK guards against direct SQL.

ALTER TABLE customers ADD COLUMN slug text;

-- Partial unique index — allows many NULLs while forbidding duplicate slugs.
CREATE UNIQUE INDEX customers_slug_key ON customers(slug) WHERE slug IS NOT NULL;

ALTER TABLE customers
    ADD CONSTRAINT customers_slug_format
    CHECK (slug IS NULL OR slug ~ '^[a-z0-9][a-z0-9-]{1,48}[a-z0-9]$');

-- Backfill the PoC seed so acme.<env>.involvecloud.com works out of the box.
-- Guarded by the current NULL check so re-running is a no-op if an operator
-- has already set a different slug for this row.
UPDATE customers SET slug = 'acme'
 WHERE id = '11111111-1111-1111-1111-111111111111'
   AND slug IS NULL;
