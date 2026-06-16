-- Slice 1 schema: tenant hierarchy + collectors + devices + telemetry/events.
-- gen_random_uuid() is built into PostgreSQL 13+ (no extension needed).
--
-- Hierarchy: Customer -> Region -> Location -> Building -> Room -> Device.
-- Every tenant table carries customer_id directly so the RLS policy is a
-- single-column check with no joins (see 0002_rls.sql).

CREATE TABLE customers (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE regions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    name        text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE locations (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    region_id   uuid NOT NULL REFERENCES regions(id) ON DELETE CASCADE,
    name        text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE buildings (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    location_id uuid NOT NULL REFERENCES locations(id) ON DELETE CASCADE,
    name        text NOT NULL,
    address     text,
    timezone    text,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE rooms (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    building_id uuid NOT NULL REFERENCES buildings(id) ON DELETE CASCADE,
    name        text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE collectors (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id         uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    building_id         uuid REFERENCES buildings(id) ON DELETE SET NULL,
    -- bridge_collector_id is the id the on-prem bridge reports today (from its
    -- YAML). Transitional: once config-pull mints ids, the cloud id is canonical.
    bridge_collector_id text UNIQUE,
    name                text NOT NULL,
    hmac_secret_enc     bytea NOT NULL,
    status              text NOT NULL DEFAULT 'unknown',
    last_seen_at        timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE devices (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id      uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    room_id          uuid REFERENCES rooms(id) ON DELETE SET NULL,   -- null = unplaced
    collector_id     uuid NOT NULL REFERENCES collectors(id) ON DELETE CASCADE,
    -- reported_id is the bridge's own device id (e.g. "display-sony-01").
    -- Transitional correlation key; drops away once config-pull mints ids.
    reported_id      text,
    name             text,
    type             text,
    protocol         text,
    make             text,
    model            text,
    serial_number    text,
    firmware_version text,
    mac_address      text,
    ip_address       text,
    tags             jsonb,
    latest_status    text,
    latest_metrics   jsonb,
    last_seen_at     timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (collector_id, reported_id)
);

CREATE TABLE telemetry (
    id          bigserial PRIMARY KEY,
    customer_id uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    device_id   uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    status      text,
    metrics     jsonb,
    error       text,
    ts          timestamptz NOT NULL
);
CREATE INDEX telemetry_device_ts_idx ON telemetry (device_id, ts DESC);

CREATE TABLE events (
    id          bigserial PRIMARY KEY,
    customer_id uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    device_id   uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    event_type  text,
    payload     jsonb,
    ts          timestamptz NOT NULL
);
CREATE INDEX events_device_ts_idx ON events (device_id, ts DESC);
