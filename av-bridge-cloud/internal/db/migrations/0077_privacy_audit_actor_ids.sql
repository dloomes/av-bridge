-- Privacy and offboarding audit entries (privacy.*, customer.offboard,
-- customer.offboard_cancel, customer.export) record their actor as
-- 'user:<uuid>', never an email, so an erasure that matches the actor only on
-- the email leaves them naming the person. The access export (privacy.Collect)
-- matches actor = 'user:' || id for every account id; erasure must anonymise
-- the same entries.
--
-- This replaces 0075's array overload: actor also matches 'user:' || id for
-- each id (case-insensitively, as target_id does), and such an actor is
-- replaced by the stand-in. Everything else is as in 0075. The 0073 signature
-- delegates to this overload, so it gains the same match. Same security shape
-- as 0062/0073/0075: owned by the migration role, SECURITY DEFINER, fixed
-- search_path, scoped to one organisation through app.current_customer,
-- EXECUTE for app_admin only.
CREATE OR REPLACE FUNCTION privacy_anonymise_audit(
    p_customer uuid, p_email text, p_user_ids text[], p_standin text)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    n       bigint;
    uids    text[];
    actors  text[];
    esc     text;
    pattern text;
BEGIN
    IF coalesce(p_email, '') = '' THEN
        RAISE EXCEPTION 'privacy_anonymise_audit: email required';
    END IF;
    IF coalesce(p_standin, '') = '' THEN
        RAISE EXCEPTION 'privacy_anonymise_audit: stand-in required';
    END IF;
    -- Lower-cased, empty ids dropped (an empty id must never match target_id = ''
    -- or actor = 'user:').
    SELECT coalesce(array_agg(lower(u)), '{}') INTO uids
      FROM unnest(coalesce(p_user_ids, '{}')) AS u
     WHERE coalesce(u, '') <> '';
    SELECT coalesce(array_agg('user:' || u), '{}') INTO actors
      FROM unnest(uids) AS u;
    -- Regex-escape the email (class: ] ^ $ | ? * + ( ) { } . backslash [).
    esc := regexp_replace(p_email, '([]^$|?*+(){}.' || chr(92) || chr(92) || '[])',
                          chr(92) || chr(92) || chr(92) || '1', 'g');
    pattern := '(?<![A-Za-z0-9._%+-])' || esc || '(?![A-Za-z0-9_%+-]|[.][A-Za-z0-9])';
    -- audit_log has FORCE ROW LEVEL SECURITY; scope to the one organisation.
    PERFORM set_config('app.current_customer', p_customer::text, true);
    UPDATE audit_log a
       SET actor = CASE WHEN lower(a.actor) = lower(p_email) OR lower(a.actor) = ANY(actors)
                        THEN p_standin ELSE a.actor END,
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
            OR lower(a.actor) = ANY(actors)
            OR lower(a.target_id) = ANY(uids)
            OR (coalesce(a.before::text, '') || ' ' || coalesce(a."after"::text, '') || ' '
                || coalesce(a.metadata::text, '')) ~* pattern);
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END
$$;

REVOKE ALL ON FUNCTION privacy_anonymise_audit(uuid, text, text[], text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION privacy_anonymise_audit(uuid, text, text[], text) TO app_admin;
