-- 0026_recipe_to_routine_rename.sql — customer-facing rename.
--
-- "Recipe" reads too kitchen-adjacent for an ops product; "Routine" is
-- what the sales conversation calls this artefact anyway. Rename the
-- table, its FK columns on the schedule + per-room override rows, the
-- snapshot column on nightly_run, and the index / constraint names that
-- carry "recipe" in their identifier.
--
-- Fully mechanical — no data changes, no policy changes. All RLS
-- policies are attached to tables (not columns) so they follow the
-- rename automatically. Trigger names stay the same (they name the
-- table generically, not "recipe").

-- 1. The table itself.
ALTER TABLE nightly_test_recipe RENAME TO nightly_test_routine;

-- 2. Index on the renamed table.
ALTER INDEX nightly_test_recipe_customer_idx
    RENAME TO nightly_test_routine_customer_idx;

-- 3. FK column on nightly_schedule + its FK constraint.
ALTER TABLE nightly_schedule RENAME COLUMN test_recipe_id TO test_routine_id;
ALTER TABLE nightly_schedule
    RENAME CONSTRAINT nightly_schedule_recipe_fk TO nightly_schedule_routine_fk;

-- 4. FK column on room_nightly_config. Postgres auto-generated its FK
--    constraint name (room_nightly_config_test_recipe_id_fkey) so rename
--    that too for consistency.
ALTER TABLE room_nightly_config RENAME COLUMN test_recipe_id TO test_routine_id;
ALTER TABLE room_nightly_config
    RENAME CONSTRAINT room_nightly_config_test_recipe_id_fkey
                   TO room_nightly_config_test_routine_id_fkey;

-- 5. Snapshot column on nightly_run + its auto-generated FK constraint.
ALTER TABLE nightly_run RENAME COLUMN recipe_id TO routine_id;
ALTER TABLE nightly_run
    RENAME CONSTRAINT nightly_run_recipe_id_fkey
                   TO nightly_run_routine_id_fkey;

-- Trigger updated_at hook was named nightly_test_recipe_updated_at.
-- Rename it too so future greps don't find stragglers.
ALTER TRIGGER nightly_test_recipe_updated_at ON nightly_test_routine
    RENAME TO nightly_test_routine_updated_at;
