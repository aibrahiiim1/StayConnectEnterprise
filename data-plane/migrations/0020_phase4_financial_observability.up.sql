-- 0020 — PHASE 4: the one column financial observability needs. D18 / T0029. Receipt: T0038.
--
-- MEASURED: posting_outbox records STATE but not WHEN. The contract requires backlog AGE as an operational
-- signal, and age cannot be derived from anything the table holds -- "seven items queued" is not actionable
-- without "the oldest for nineteen minutes", because the first is normal and the second is an incident.
--
-- Additive with a default, so existing rows get a truthful-enough value (now()) rather than a fabricated
-- history. The comment records that: an age computed for a row that predates this migration measures time
-- since the migration, not time since enqueue.
BEGIN;

ALTER TABLE iam_v2.posting_outbox ADD COLUMN IF NOT EXISTS enqueued_at timestamptz NOT NULL DEFAULT now();

COMMENT ON COLUMN iam_v2.posting_outbox.enqueued_at IS
  'When this work entered the outbox. Rows that predate migration 0020 carry the migration time, so their '
  'measured age understates the true wait; no history is invented to hide that.';

CREATE INDEX IF NOT EXISTS outbox_backlog_age
  ON iam_v2.posting_outbox (tenant_id, site_id, enqueued_at)
  WHERE state IN ('QUEUED','IN_FLIGHT','HELD_RECOVERY');

INSERT INTO public.schema_migrations (version) VALUES ('0020_phase4_financial_observability')
  ON CONFLICT DO NOTHING;
COMMIT;
