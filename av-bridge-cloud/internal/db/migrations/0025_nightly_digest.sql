-- 0025_nightly_digest.sql — morning digest bookkeeping.
--
-- Phase A slice 5: adds a per-customer date stamp so the digest sender
-- goroutine can send at most once per morning (idempotent under restart,
-- unique per calendar day in the customer's timezone).
--
-- We store the local date rather than an instant so a customer whose
-- schedule falls near local midnight still gets exactly one digest per
-- calendar day, regardless of what UTC instant that day started at.

ALTER TABLE nightly_schedule
    ADD COLUMN digest_last_sent_for date;
