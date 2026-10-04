DROP INDEX outbox_ready;
ALTER TABLE outbox DROP COLUMN next_publish_at, DROP COLUMN publication_failures;
