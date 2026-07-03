-- Slice 7: freeze the caller's authorization context onto every audit row.
--
-- Role assignments and per-user building scope change over time (helpdesk
-- promotes a viewer to operator; a customer admin narrows a contractor's
-- scope). Without a snapshot, "who was allowed to see device X last month?"
-- becomes unanswerable — the current user_roles / users.building_scope_ids
-- state has already drifted.
--
-- We store this as structured columns (not JSON in metadata) so a compliance
-- query can filter directly: e.g. WHERE '<building-uuid>' = ANY(actor_scope).
--
--   actor_role      — the caller's derived primary role name at time of action
--                     ("admin" / "operator" / "viewer" / custom role name).
--                     NULL for legacy static-token calls and pre-slice-7 rows.
--   actor_scope     — the caller's building_scope_ids at time of action.
--                     Empty array = unscoped (full-tenant); non-empty = only
--                     those buildings. NULL for pre-slice-7 rows.
--   actor_is_vendor — true when the row was written by a vendor principal
--                     acting via X-Customer-Scope. Distinguishes "customer
--                     admin edited device" from "vendor edited device on
--                     their behalf" — matters for support forensics.

ALTER TABLE audit_log
    ADD COLUMN actor_role      text,
    ADD COLUMN actor_scope     text[],
    ADD COLUMN actor_is_vendor boolean NOT NULL DEFAULT false;
