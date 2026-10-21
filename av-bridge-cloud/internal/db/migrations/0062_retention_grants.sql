-- Retention (0061) runs as app_admin, which couldn't delete from two of its
-- tables:
--
--   alerts      app_admin had SELECT, INSERT, UPDATE only (0009, 0012, 0028).
--   audit_log   append-only for both app roles (0007), deliberately: the
--               application must not be able to remove its own audit trail.
--
-- alerts gets DELETE. audit_log stays append-only; instead this function,
-- owned by the migration role, deletes one organisation's entries past the
-- published 24 months and nothing else. app_admin may call it but still
-- can't DELETE from audit_log itself.

GRANT DELETE ON alerts TO app_admin;

CREATE OR REPLACE FUNCTION retention_prune_audit_log(p_customer uuid, p_max int)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    n bigint;
BEGIN
    -- audit_log has FORCE ROW LEVEL SECURITY, which applies to its owner
    -- too unless that's a superuser; scope to the one organisation.
    PERFORM set_config('app.current_customer', p_customer::text, true);
    DELETE FROM audit_log
     WHERE ctid = ANY (ARRAY(
           SELECT a.ctid FROM audit_log a
            WHERE a.customer_id = p_customer
              AND a.ts < now() - interval '24 months'
            LIMIT p_max));
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END
$$;

REVOKE ALL ON FUNCTION retention_prune_audit_log(uuid, int) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION retention_prune_audit_log(uuid, int) TO app_admin;
