BEGIN;

CREATE TABLE IF NOT EXISTS central_notification_settings (
  id                          boolean PRIMARY KEY DEFAULT true CHECK (id),
  enabled                     boolean NOT NULL DEFAULT false,
  smtp_host                   text NOT NULL DEFAULT '' CHECK (length(smtp_host) <= 253),
  smtp_port                   int NOT NULL DEFAULT 587 CHECK (smtp_port BETWEEN 1 AND 65535),
  smtp_security               text NOT NULL DEFAULT 'starttls' CHECK (smtp_security IN ('starttls','tls')),
  smtp_username               text NOT NULL DEFAULT '' CHECK (length(smtp_username) <= 320),
  from_address                text NOT NULL DEFAULT '' CHECK (length(from_address) <= 320),
  from_name                   text NOT NULL DEFAULT 'OneGate Central' CHECK (length(from_name) <= 100),
  console_url                 text NOT NULL DEFAULT '' CHECK (length(console_url) <= 300),
  licence_first_warning_days  int NOT NULL DEFAULT 30 CHECK (licence_first_warning_days BETWEEN 2 AND 180),
  licence_second_warning_days int NOT NULL DEFAULT 7 CHECK (licence_second_warning_days BETWEEN 1 AND 90),
  licence_final_warning_days  int NOT NULL DEFAULT 1 CHECK (licence_final_warning_days BETWEEN 0 AND 30),
  offline_after_minutes       int NOT NULL DEFAULT 1440 CHECK (offline_after_minutes BETWEEN 15 AND 10080),
  digest_hour_utc             int NOT NULL DEFAULT 7 CHECK (digest_hour_utc BETWEEN 0 AND 23),
  log_retention_days          int NOT NULL DEFAULT 90 CHECK (log_retention_days BETWEEN 7 AND 730),
  max_attempts                int NOT NULL DEFAULT 8 CHECK (max_attempts BETWEEN 1 AND 20),
  portal_tls_event_cursor     bigint,
  last_digest_at              timestamptz,
  last_success_at             timestamptz,
  last_error                  text,
  last_error_at               timestamptz,
  updated_at                  timestamptz NOT NULL DEFAULT now(),
  updated_by                  text,
  CHECK (licence_final_warning_days < licence_second_warning_days
         AND licence_second_warning_days < licence_first_warning_days)
);
INSERT INTO central_notification_settings (id) VALUES (true) ON CONFLICT (id) DO NOTHING;
COMMENT ON TABLE central_notification_settings IS
  'One row: how Central emails its staff about the fleet (0065). The SMTP password is a 0600 file in CTRLAPI_NOTIFY_DIR, never a column.';

CREATE TABLE IF NOT EXISTS central_notification_subscriptions (
  operator_id uuid NOT NULL REFERENCES operators(id) ON DELETE CASCADE,
  event_kind  text NOT NULL CHECK (event_kind IN ('licence_expiring','licence_grace','licence_expired',
                'appliance_offline','security_alert','retirement_unconfirmed','appliance_waiting',
                'portal_certificate_failed','backup_problem')),
  delivery    text NOT NULL CHECK (delivery IN ('immediate','digest')),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (operator_id, event_kind)
);
COMMENT ON TABLE central_notification_subscriptions IS
  'Which events each Central team member is emailed about, immediately or in the daily digest (0065).';

CREATE TABLE IF NOT EXISTS central_notification_episodes (
  event_kind  text NOT NULL,
  subject     text NOT NULL,
  stage       int NOT NULL DEFAULT 0,
  opened_at   timestamptz NOT NULL DEFAULT now(),
  notified_at timestamptz,
  PRIMARY KEY (event_kind, subject)
);
COMMENT ON TABLE central_notification_episodes IS
  'Conditions Central has already told its staff about, one row while each lasts, so each occurrence is sent once (0065).';

CREATE TABLE IF NOT EXISTS central_notification_outbox (
  id              bigserial PRIMARY KEY,
  created_at      timestamptz NOT NULL DEFAULT now(),
  operator_id     uuid REFERENCES operators(id) ON DELETE SET NULL,
  event_kind      text NOT NULL,
  delivery        text NOT NULL CHECK (delivery IN ('immediate','digest')),
  subject_line    text NOT NULL,
  body            text NOT NULL,
  status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sent','failed','dropped')),
  attempts        int NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  sent_at         timestamptz,
  last_error      text
);
CREATE INDEX IF NOT EXISTS central_notification_outbox_due
  ON central_notification_outbox (next_attempt_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS central_notification_outbox_created
  ON central_notification_outbox (created_at DESC);
COMMENT ON TABLE central_notification_outbox IS
  'Staff notification emails waiting, sent, failed or dropped (0065). Holds no address: the recipient is resolved at send time. Pruned after log_retention_days.';

SELECT central_apply_runtime_grants();

COMMIT;
