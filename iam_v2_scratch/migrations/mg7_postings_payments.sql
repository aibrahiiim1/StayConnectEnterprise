-- MG-7  Financial postings & payments (iam_v2). Ledger schema + integrity; execution is phase 4.
-- Reversal: only the passive REVERSAL ledger row + linkage + Sum<=CHARGE. NO executable reversal sender.
BEGIN;
CREATE TABLE iam_v2.pms_postings (              -- append-only ledger
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  settlement_id uuid NOT NULL, purchase_id uuid NOT NULL,
  stay_id uuid, folio_id uuid,
  posting_interface_revision_id uuid NOT NULL, secret_generation_id uuid,
  posting_type text NOT NULL CHECK (posting_type IN ('CHARGE','REVERSAL')),
  reverses_posting_id uuid,
  amount_minor bigint NOT NULL, currency char(3), currency_exponent smallint,
  idempotency_key text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, site_id, pms_interface_id, id),
  UNIQUE (tenant_id, site_id, id),
  UNIQUE (id, pms_interface_id),
  CONSTRAINT posting_reversal_link CHECK ((posting_type='REVERSAL') = (reverses_posting_id IS NOT NULL)),
  FOREIGN KEY (tenant_id, site_id, settlement_id) REFERENCES iam_v2.settlements (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, purchase_id) REFERENCES iam_v2.purchases (tenant_id, site_id, id),
  FOREIGN KEY (settlement_id, purchase_id) REFERENCES iam_v2.settlements (id, purchase_id),
  FOREIGN KEY (purchase_id, pms_interface_id) REFERENCES iam_v2.purchases (id, pms_interface_id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, posting_interface_revision_id)
    REFERENCES iam_v2.pms_interface_revisions (tenant_id, site_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, secret_generation_id)
    REFERENCES iam_v2.pms_interface_secret_generations (tenant_id, site_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id) REFERENCES iam_v2.stays (tenant_id, site_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, folio_id) REFERENCES iam_v2.folios (tenant_id, site_id, pms_interface_id, id));

CREATE TABLE iam_v2.posting_outbox (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL, posting_id uuid NOT NULL,
  state text NOT NULL DEFAULT 'QUEUED' CHECK (state IN ('QUEUED','IN_FLIGHT','DONE','HELD_RECOVERY')),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, posting_id)
    REFERENCES iam_v2.pms_postings (tenant_id, site_id, pms_interface_id, id));
CREATE UNIQUE INDEX outbox_one_active ON iam_v2.posting_outbox (posting_id)
  WHERE state IN ('QUEUED','IN_FLIGHT','HELD_RECOVERY');

-- payment_transactions: merchant_account_id FK to stripe_accounts is DEFERRED (no platform anchor exists).
CREATE TABLE iam_v2.payment_transactions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, settlement_id uuid NOT NULL,
  merchant_account_id uuid NOT NULL,              -- FK to public.stripe_accounts DEFERRED to payment phase
  transaction_type text NOT NULL CHECK (transaction_type IN ('CHARGE','REFUND','CHARGEBACK')),
  parent_transaction_id uuid,
  provider text NOT NULL, provider_ref text NOT NULL, idempotency_key text NOT NULL UNIQUE,
  amount_minor bigint NOT NULL CHECK (amount_minor > 0),
  currency char(3) NOT NULL, currency_exponent smallint NOT NULL,
  status text NOT NULL CHECK (status IN ('CREATED','PENDING','CAPTURED','FAILED','EXPIRED','CANCELLED','UNKNOWN')),
  UNIQUE (tenant_id, provider, merchant_account_id, provider_ref),
  UNIQUE (tenant_id, site_id, settlement_id, id),
  CONSTRAINT ptx_parent CHECK ((transaction_type='CHARGE') = (parent_transaction_id IS NULL)),
  FOREIGN KEY (tenant_id, site_id, settlement_id) REFERENCES iam_v2.settlements (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, settlement_id, parent_transaction_id)
    REFERENCES iam_v2.payment_transactions (tenant_id, site_id, settlement_id, id));

CREATE TABLE iam_v2.posting_attempts (          -- immutable identity + one-way state (MG-9 triggers)
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  internal_posting_id uuid NOT NULL, pms_interface_id uuid NOT NULL, attempt_no int NOT NULL,
  p_number text NOT NULL, rn text, g_number text, sent_at timestamptz NOT NULL,
  outcome text NOT NULL DEFAULT 'SENDING' CHECK (outcome IN ('SENDING','ACKED','UNKNOWN','FAILED')),
  response_at timestamptz, pa_as_status text CHECK (pa_as_status IN ('OK','NG','NA','NP','NR','RY','UR')),
  UNIQUE (tenant_id, site_id, pms_interface_id, p_number),
  UNIQUE (internal_posting_id, attempt_no),
  UNIQUE (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, internal_posting_id) REFERENCES iam_v2.pms_postings (tenant_id, site_id, id));

CREATE TABLE iam_v2.posting_attempt_events (    -- fully append-only
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, posting_attempt_id uuid NOT NULL,
  event_type text NOT NULL, detail jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, site_id, posting_attempt_id) REFERENCES iam_v2.posting_attempts (tenant_id, site_id, id));

CREATE TABLE iam_v2.posting_review_actions (    -- immutable manual-review decisions
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, posting_id uuid NOT NULL,
  action text NOT NULL CHECK (action IN ('CONFIRM_POSTED','CONFIRM_NOT_POSTED_RETRY','CONFIRM_NOT_POSTED_ABANDON','CREATE_REVERSAL','ESCALATE')),
  actor uuid NOT NULL, reason text NOT NULL, evidence jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, site_id, posting_id) REFERENCES iam_v2.pms_postings (tenant_id, site_id, id));

CREATE TABLE iam_v2.financial_epoch (
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  epoch int NOT NULL DEFAULT 1, restore_generation int NOT NULL DEFAULT 0, updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, site_id));

CREATE TABLE iam_v2.compliance_archives (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  manifest_sha256 text NOT NULL, receipt_verified boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now());
COMMIT;
