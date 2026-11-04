-- Erasure (GDPR data requests) anonymises a person's audit entries for every
-- account id they have had: the current one and earlier, hard-deleted ones
-- (found again through user.delete entries, see privacy.accountIDs). The
-- 0073 function takes one id and compares target_id case-sensitively, while
-- the access export matches lower(target_id); an entry the export shows must
-- be anonymised by erasure.
--
-- This adds an overload taking every id at once, so each entry is updated
-- (and counted) once, and matches target_id case-insensitively. The 0073
-- signature is kept, delegating to it. Same security shape as 0073: owned by
-- the migration role, SECURITY DEFINER, fixed search_path, scoped to one
-- organisation through app.current_customer, EXECUTE for app_admin only.
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
    esc     text;
    pattern text;
BEGIN
    IF coalesce(p_email, '') = '' THEN
        RAISE EXCEPTION 'privacy_anonymise_audit: email required';
    END IF;
    IF coalesce(p_standin, '') = '' THEN
        RAISE EXCEPTION 'privacy_anonymise_audit: stand-in required';
    END IF;
    -- Lower-cased, empty ids dropped (an empty id must never match target_id = '').
    SELECT coalesce(array_agg(lower(u)), '{}') INTO uids
      FROM unnest(coalesce(p_user_ids, '{}')) AS u
     WHERE coalesce(u, '') <> '';
    -- Regex-escape the email (class: ] ^ $ | ? * + ( ) { } . backslash [).
    esc := regexp_replace(p_email, '([]^$|?*+(){}.' || chr(92) || chr(92) || '[])',
                          chr(92) || chr(92) || chr(92) || '1', 'g');
    pattern := '(?<![A-Za-z0-9._%+-])' || esc || '(?![A-Za-z0-9_%+-]|[.][A-Za-z0-9])';
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
            OR lower(a.target_id) = ANY(uids)
            OR (coalesce(a.before::text, '') || ' ' || coalesce(a."after"::text, '') || ' '
                || coalesce(a.metadata::text, '')) ~* pattern);
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END
$$;

REVOKE ALL ON FUNCTION privacy_anonymise_audit(uuid, text, text[], text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION privacy_anonymise_audit(uuid, text, text[], text) TO app_admin;

-- The 0073 signature, now case-insensitive on target_id through the overload.
CREATE OR REPLACE FUNCTION privacy_anonymise_audit(
    p_customer uuid, p_email text, p_user_id text, p_standin text)
RETURNS bigint
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT privacy_anonymise_audit(p_customer, p_email, ARRAY[p_user_id]::text[], p_standin);
$$;

REVOKE ALL ON FUNCTION privacy_anonymise_audit(uuid, text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION privacy_anonymise_audit(uuid, text, text, text) TO app_admin;
