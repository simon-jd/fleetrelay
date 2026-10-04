ALTER TABLE outbox
  ADD COLUMN publication_failures integer NOT NULL DEFAULT 0 CHECK (publication_failures >= 0),
  ADD COLUMN next_publish_at timestamptz NOT NULL DEFAULT now();

-- Preserve the age/order of existing work during upgrade.
UPDATE outbox SET next_publish_at = created_at;
CREATE INDEX outbox_ready ON outbox(next_publish_at, created_at, outbox_id)
  WHERE published_at IS NULL;
