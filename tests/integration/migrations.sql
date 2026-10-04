-- Run only against FLEETRELAY_TEST_DATABASE_URL. Everything is rolled back,
-- including the schema and any optional extension created by the initial DDL.
BEGIN;
CREATE SCHEMA fleetrelay_migration_check;
SET LOCAL search_path = fleetrelay_migration_check;
\ir ../../migrations/apply.sql
INSERT INTO outbox(aggregate_id,kind,payload,business_key,created_at,publication_failures,next_publish_at)
VALUES('preserved','lease.changed','{"fixture":true}','preserved','2026-01-01',7,'2099-01-01');
\ir ../../migrations/apply.sql
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM outbox WHERE business_key='preserved' AND publication_failures=7 AND next_publish_at='2099-01-01') THEN
    RAISE EXCEPTION 'repeated bootstrap reset a pending retry';
  END IF;
END $$;
\ir ../../migrations/000002_outbox_retry.down.sql
DO $$ BEGIN
  IF (SELECT count(*) FROM outbox) <> 1 THEN RAISE EXCEPTION 'rollback lost outbox row'; END IF;
END $$;
\ir ../../migrations/apply.sql
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM outbox WHERE business_key='preserved' AND publication_failures=0
    AND next_publish_at=created_at AND payload='{"fixture":true}'::jsonb AND published_at IS NULL) THEN
    RAISE EXCEPTION 'upgrade lost pending row or did not preserve original eligibility';
  END IF;
END $$;
ROLLBACK;
\echo 'PASS: fresh bootstrap, repeat bootstrap, rollback and existing-row upgrade'
