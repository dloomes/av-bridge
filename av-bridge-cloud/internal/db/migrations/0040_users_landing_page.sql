-- 0040_users_landing_page.sql — user-level default landing page preference.
--
-- Two landing surfaces exist for a signed-in user: the overview page (the
-- default, hierarchical stats + tile grid) and the map view (geographic
-- pins per building). This preference determines where sign-in lands the
-- user; both pages remain reachable from the sidebar at all times.
--
-- Stored on `users` because the choice is per-account, not per-tenant. A
-- vendor helpdesk user can prefer the map while an operator on the same
-- tenant sticks with overview. CHECK enum keeps the column self-describing
-- and rejects a typo before it reaches the redirect logic.
--
-- Kept as a single column rather than a `user_preferences` table because
-- we only have one preference. If a second preference lands we can lift
-- both to a JSONB `preferences` column or a keyed side table then.

ALTER TABLE users
    ADD COLUMN landing_page text NOT NULL DEFAULT 'overview'
        CHECK (landing_page IN ('overview', 'map'));
