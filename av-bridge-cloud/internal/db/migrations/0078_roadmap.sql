-- Product roadmap in the portal, with share links.
--
-- The roadmap belongs to Involve, not to any customer, so these tables are
-- global: no customer_id and no RLS. Signed-in reads and the public share
-- endpoint go through the admin pool, with permission checks in the
-- handlers. app_tenant may read the content tables only, and never the
-- share links (they hold token hashes).

CREATE TABLE roadmap_settings (
    id          boolean PRIMARY KEY DEFAULT true CHECK (id),
    title       text NOT NULL,
    headline    text NOT NULL,
    lede        text NOT NULL,
    as_of       date NOT NULL,
    disclaimer  text NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  text
);

CREATE TABLE roadmap_periods (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    position    int  NOT NULL UNIQUE CHECK (position BETWEEN 0 AND 3),
    name        text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 40),
    months      text NOT NULL CHECK (length(btrim(months)) BETWEEN 1 AND 60),
    is_current  boolean NOT NULL DEFAULT false
);

CREATE TABLE roadmap_themes (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    position    int  NOT NULL,
    name        text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 60),
    note        text NOT NULL DEFAULT '' CHECK (length(note) <= 160)
);

CREATE TABLE roadmap_items (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    theme_id        uuid NOT NULL REFERENCES roadmap_themes(id) ON DELETE RESTRICT,
    period_position int  NOT NULL CHECK (period_position BETWEEN 0 AND 3),
    status          text NOT NULL CHECK (status IN ('committed', 'planned', 'exploring')),
    release_month   text CHECK (release_month IS NULL OR length(btrim(release_month)) BETWEEN 1 AND 20),
    title           text NOT NULL CHECK (length(title) BETWEEN 1 AND 80),
    description     text NOT NULL CHECK (length(description) BETWEEN 1 AND 240),
    position        int  NOT NULL DEFAULT 0,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    updated_by      text
);
CREATE INDEX roadmap_items_theme_idx ON roadmap_items (theme_id);

CREATE TABLE roadmap_shipped (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    when_label  text NOT NULL CHECK (length(btrim(when_label)) BETWEEN 1 AND 40),
    title       text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 160),
    position    int  NOT NULL DEFAULT 0
);

-- A link an editor shares with someone outside the portal. Only the SHA-256
-- of the token is stored.
CREATE TABLE roadmap_share_links (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name          text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 80),
    token_hash    text NOT NULL UNIQUE,
    expires_at    timestamptz NOT NULL,
    created_by    text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz,
    use_count     int NOT NULL DEFAULT 0,
    revoked_at    timestamptz
);

GRANT SELECT, INSERT, UPDATE, DELETE ON
    roadmap_settings, roadmap_periods, roadmap_themes, roadmap_items,
    roadmap_shipped, roadmap_share_links TO app_admin;
GRANT SELECT ON
    roadmap_settings, roadmap_periods, roadmap_themes, roadmap_items,
    roadmap_shipped TO app_tenant;

-- Only meaningful for Involve users; set through the helpdesk Users API.
ALTER TABLE users ADD COLUMN roadmap_editor boolean NOT NULL DEFAULT false;

-- Seed: the roadmap as published on 2 Nov 2026.
INSERT INTO roadmap_settings (title, headline, lede, as_of, disclaimer) VALUES (
    'M.A.R.C.U.S. Roadmap 2027',
    'Bring a new estate under management faster',
    'Over the next 12 months M.A.R.C.U.S. makes onboarding a self-service job, monitors more of the equipment you already own, and connects to the tools your teams already use. A new release ships every month.',
    '2026-11-02',
    'This roadmap shows our current plans. Planned and Exploring items, and their timing, may change as we learn from customers. It is not a commitment to deliver any feature by a given date, and should not be relied on for purchasing decisions.');

INSERT INTO roadmap_periods (position, name, months, is_current) VALUES
    (0, 'Now',       'Nov 2026 – Jan 2027', true),
    (1, 'Next',      'Feb – Apr 2027',      false),
    (2, 'Mid-year',  'May – Jul 2027',      false),
    (3, 'Later',     'Aug – Oct 2027',      false);

INSERT INTO roadmap_themes (position, name, note) VALUES
    (0, 'Faster onboarding', 'From signed contract to a monitored estate'),
    (1, 'Device coverage',   'More of the equipment you already own'),
    (2, 'Room operations',   'Keep rooms ready and fix them remotely'),
    (3, 'Integrations',      'Work inside the tools you already use');

INSERT INTO roadmap_items (theme_id, period_position, status, release_month, title, description, position)
SELECT t.id, v.q, v.s, v.rel, v.title, v.descr, v.pos
FROM (VALUES
    ('Faster onboarding', 0, 'committed', 'Nov', 'Bulk device import', 'Add hundreds of devices from one spreadsheet, checked before anything is saved.', 0),
    ('Faster onboarding', 0, 'committed', 'Nov', 'Signed Windows Collector installer', 'Passes application allow-listing; proxy support built in.', 1),
    ('Faster onboarding', 0, 'committed', 'Dec', 'Onboarding checklist', 'A guided list in the portal of what''s set up and what''s left.', 2),
    ('Faster onboarding', 1, 'planned', NULL, 'Device discovery', 'The Collector scans chosen subnets and offers what it finds for import.', 0),
    ('Faster onboarding', 1, 'planned', NULL, 'Collector virtual appliance', 'A ready-made VM for VMware and Hyper-V: import, enrol, done.', 1),
    ('Device coverage', 0, 'committed', 'Dec', 'Samsung and LG displays', 'Power, input, volume, temperature and firmware, with Room Readiness power control.', 0),
    ('Device coverage', 0, 'committed', 'Jan', 'NEC displays and projectors', 'The same readings and controls as other displays.', 1),
    ('Device coverage', 2, 'planned', NULL, 'Microsoft Teams Rooms and Zoom Rooms', 'Platform health beside the hardware, so a signed-out room shows as a problem.', 0),
    ('Device coverage', 2, 'planned', NULL, 'Extron and AMX control', 'Control processors and switchers, alongside Crestron.', 1),
    ('Device coverage', 3, 'exploring', NULL, 'Q-SYS and Shure', 'Processors and microphones: levels, battery, mute state and faults.', 0),
    ('Room operations', 0, 'committed', 'Jan', 'Sequenced power-on', 'Switch a room''s devices on in a set order, with waits between steps.', 0),
    ('Room operations', 1, 'planned', NULL, 'Firmware updates from the portal', 'Upload a vendor file; update one device or a group on a schedule. Cisco RoomOS first.', 0),
    ('Room operations', 1, 'planned', NULL, 'Remote terminal and touch panel access', 'SSH, Telnet and touch panels through the Collector, with no VPN; every session audited.', 1),
    ('Room operations', 3, 'exploring', NULL, 'End-to-end room tests', 'Prove signal reaches each display and audio passes through the room.', 0),
    ('Room operations', 3, 'exploring', NULL, 'Firmware vulnerability check', 'Read vendor release notes in one click: latest release, plus devices flagged for known security vulnerabilities, with a report.', 1),
    ('Integrations', 2, 'planned', NULL, 'ServiceNow CMDB sync', 'Assets and devices kept in step with your CMDB, so incidents hit the right item.', 0),
    ('Integrations', 3, 'exploring', NULL, 'Microsoft Teams app', 'Act on alerts in Teams: acknowledge, run a check or open the device.', 0)
) AS v(theme, q, s, rel, title, descr, pos)
JOIN roadmap_themes t ON t.name = v.theme;

INSERT INTO roadmap_shipped (when_label, title, position) VALUES
    ('Nov 2026', 'Power schedules and one-off dates by room tag', 0),
    ('Oct 2026', 'Room tags: filter, report, dashboards and commands by room type', 1),
    ('Oct 2026', 'Readiness checks by room tag', 2),
    ('Oct 2026', 'Ping step and routine flow diagrams', 3),
    ('Oct 2026', 'Device last contacted', 4),
    ('Oct 2026', 'Firmware versions in the Public API', 5),
    ('Oct 2026', 'Collector logs in the portal', 6);
