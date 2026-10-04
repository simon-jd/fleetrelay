-- Run with psql -X --set ON_ERROR_STOP=1 --single-transaction --file apply.sql.
-- This prototype bootstrap supports only the two known schema versions.
SET LOCAL lock_timeout = '5s';
SELECT pg_advisory_xact_lock(hashtext('fleetrelay-schema-migration'));
SELECT to_regclass('devices') IS NULL AS needs_initial \gset
\if :needs_initial
\ir 000001_initial.up.sql
\endif
SELECT NOT EXISTS (
  SELECT 1 FROM information_schema.columns
  WHERE table_schema = current_schema() AND table_name = 'outbox'
    AND column_name = 'next_publish_at'
) AS needs_outbox_retry \gset
\if :needs_outbox_retry
\ir 000002_outbox_retry.up.sql
\endif
