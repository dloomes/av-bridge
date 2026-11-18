-- Live events ride Postgres NOTIFY so every web copy's browsers get them.
-- A NOTIFY payload is capped at 8000 bytes; an event too large for that is
-- parked here and the notification carries only the row id. Rows are
-- short-lived (the relay deletes those older than an hour). Global table, no
-- customer_id and no RLS: only the admin pool touches it.

CREATE TABLE live_event_overflow (
    id         bigserial PRIMARY KEY,
    body       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

GRANT SELECT, INSERT, DELETE ON live_event_overflow TO app_admin;
GRANT USAGE ON SEQUENCE live_event_overflow_id_seq TO app_admin;
