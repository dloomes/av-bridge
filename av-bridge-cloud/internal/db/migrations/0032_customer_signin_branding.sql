-- 0032_customer_signin_branding.sql — extend per-customer branding to cover
-- the sign-in page as well as the in-app chrome.
--
-- Migration 0021 seeded logo + accent + display_name. Feedback from UAT
-- vendor testing: helpdesk operators want to hand a fully-branded sign-in
-- surface to each customer without touching platform code. New fields:
--
--   sign_in_message           — short welcome text under the product name
--   support_contact           — email / URL surfaced in the sign-in footer
--   sso_button_label          — override for the "Sign in with Microsoft" CTA
--   sign_in_hero              — bytea, optional background image on sign-in
--   sign_in_hero_content_type — mime for the hero (mirrors logo_content_type)
--
-- All fields are optional; NULLs mean "use the platform defaults". Values
-- are bounded with CHECK constraints so a fat-finger paste can't fill a
-- customer row with a paragraph.
--
-- No permission change: branding.update already gates the PATCH surface,
-- and the migration touches only columns branding.update was designed
-- around. Reads stay open to any authed tenant user, same as logo/accent.

ALTER TABLE customers
    ADD COLUMN sign_in_message           text,
    ADD COLUMN support_contact           text,
    ADD COLUMN sso_button_label          text,
    ADD COLUMN sign_in_hero              bytea,
    ADD COLUMN sign_in_hero_content_type text;

-- Length caps mirror the portal's client-side maxLength attributes so a
-- caller bypassing the UI still can't blow up a row. Kept generous enough
-- to hold "See you again — sign in to continue managing rooms at Acme."
ALTER TABLE customers
    ADD CONSTRAINT customers_sign_in_message_len
    CHECK (sign_in_message IS NULL OR length(sign_in_message) <= 500);

ALTER TABLE customers
    ADD CONSTRAINT customers_support_contact_len
    CHECK (support_contact IS NULL OR length(support_contact) <= 200);

ALTER TABLE customers
    ADD CONSTRAINT customers_sso_button_label_len
    CHECK (sso_button_label IS NULL OR length(sso_button_label) <= 60);

-- Hero image content-type is validated as the same allowlist the logo
-- uses (png / jpeg / svg); enforced in Go for the "must exist" case, this
-- CHECK is defence in depth against a direct DB write.
ALTER TABLE customers
    ADD CONSTRAINT customers_sign_in_hero_content_type
    CHECK (
      sign_in_hero_content_type IS NULL OR
      sign_in_hero_content_type IN ('image/png', 'image/jpeg', 'image/svg+xml', 'image/webp')
    );
