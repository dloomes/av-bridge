-- At most one open offboarding per customer (GDPR, 0073). The offboard
-- endpoint refuses a customer that is already offboarding, under a row lock
-- on customers; this makes the database hold the same rule, so cancel and
-- export (which update "the open row") can never find two.
--
-- Safe on existing data: rows are only written by the offboard endpoint,
-- which never leaves two open rows for one customer.
CREATE UNIQUE INDEX customer_deletions_one_open
    ON customer_deletions (customer_id)
    WHERE cancelled_at IS NULL AND deleted_at IS NULL;
