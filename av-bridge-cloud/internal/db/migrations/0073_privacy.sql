-- Privacy groundwork (GDPR data requests): the privacy.manage permission,
-- offboarding columns on customers, the deletion record that outlives a
-- deleted customer, and the audit anonymiser.

-- Handled by administrators by default; any custom role can be given it.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, 'privacy.manage'
  FROM roles r
 WHERE r.name = 'admin' AND r.is_system_default
   AND NOT EXISTS (
       SELECT 1 FROM role_permissions rp
        WHERE rp.role_id = r.id AND rp.permission = 'privacy.manage'
   );

ALTER TABLE customers
    ADD COLUMN offboarding_started_at timestamptz,
    ADD COLUMN offboarding_started_by text,
    ADD COLUMN delete_after           timestamptz;

-- One row per offboarding. Deliberately no RLS and no FK: it must survive
-- the customer's deletion, and holds only the organisation name and
-- operator identities, never end-user data.
CREATE TABLE customer_deletions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id   uuid NOT NULL,
    customer_name text NOT NULL,
    started_at    timestamptz NOT NULL,
    started_by    text NOT NULL,
    cancelled_at  timestamptz,
    cancelled_by  text,
    deleted_at    timestamptz,
    row_counts    jsonb,
    downloads     jsonb NOT NULL DEFAULT '[]'
);

GRANT SELECT, INSERT, UPDATE ON customer_deletions TO app_admin;

-- audit_log is append-only for the app roles (0007). Erasing a person has to
-- rewrite the identifying parts of their entries, so, as with 0062's prune,
-- this function runs as the migration role, scoped to one organisation, and
-- app_admin still can't UPDATE audit_log directly.
--
-- Matches entries where the person is the actor, the target, or is named
-- anywhere in before/after/metadata. Actor is replaced by the stand-in;
-- identifying keys are dropped and any remaining mention of the email is
-- replaced by the stand-in. Returns the number of entries changed.
CREATE OR REPLACE FUNCTION privacy_anonymise_audit(
    p_customer uuid, p_email text, p_user_id text, p_standin text)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    n       bigint;
    pattern text := regexp_replace(p_email, '([]^$|?*+(){}.' || chr(92) || chr(92) || '[])', chr(92) || chr(92) || chr(92) || '1', 'g');
BEGIN
    -- audit_log has FORCE ROW LEVEL SECURITY; scope to the one organisation.
    PERFORM set_config('app.current_customer', p_customer::text, true);
    UPDATE audit_log a
       SET actor = CASE WHEN lower(a.actor) = lower(p_email) THEN p_standin ELSE a.actor END,
           before = CASE WHEN a.before IS NULL THEN NULL ELSE
               regexp_replace((a.before - 'email' - 'full_name' - 'target_email' - 'requester_email')::text,
                              pattern, p_standin, 'gi')::jsonb END,
           "after" = CASE WHEN a."after" IS NULL THEN NULL ELSE
               regexp_replace((a."after" - 'email' - 'full_name' - 'target_email' - 'requester_email')::text,
                              pattern, p_standin, 'gi')::jsonb END,
           metadata = CASE WHEN a.metadata IS NULL THEN NULL ELSE
               regexp_replace((a.metadata - 'email' - 'full_name' - 'target_email' - 'requester_email')::text,
                              pattern, p_standin, 'gi')::jsonb END
     WHERE a.customer_id = p_customer
       AND (lower(a.actor) = lower(p_email)
            OR a.target_id = p_user_id
            OR lower(coalesce(a.before::text, '') || coalesce(a."after"::text, '') || coalesce(a.metadata::text, ''))
               LIKE '%' || lower(p_email) || '%');
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END
$$;

REVOKE ALL ON FUNCTION privacy_anonymise_audit(uuid, text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION privacy_anonymise_audit(uuid, text, text, text) TO app_admin;
