-- Reverts 0100 (Amendment A1 / D46): restores the folio model and the folio identity strategy exactly as 0099
-- left them. Refused while anything this migration introduced holds data, or while a posting exists.
BEGIN;

DO $guard$
BEGIN
  IF EXISTS (SELECT 1 FROM iam_v2.pms_postings) OR EXISTS (SELECT 1 FROM iam_v2.posting_attempts)
     OR EXISTS (SELECT 1 FROM iam_v2.pms_financial_onboardings)
     OR EXISTS (SELECT 1 FROM iam_v2.pms_interface_revisions WHERE posting_target_model <> 'UNSET')
     OR EXISTS (SELECT 1 FROM iam_v2.stay_posting_blocks)
     OR EXISTS (SELECT 1 FROM iam_v2.pms_answer_confirmations)
     OR EXISTS (SELECT 1 FROM iam_v2.posting_presend_aborts) THEN
    RAISE EXCEPTION 'A1_DOWN: postings, onboardings, blocks or answer confirmations exist; refusing to revert'
      USING ERRCODE = 'check_violation';
  END IF;
END $guard$;

DROP TRIGGER IF EXISTS stay_posting_permission ON iam_v2.stays;
DROP TRIGGER IF EXISTS p4_attempt_targets_posting_reservation ON iam_v2.posting_attempts;
DROP FUNCTION IF EXISTS iam_v2.trg_stay_posting_permission();
DROP FUNCTION IF EXISTS iam_v2.p4_attempt_targets_posting_reservation();
DROP FUNCTION IF EXISTS iam_v2.p4_admin_posting_block(uuid,uuid,uuid,text,text,uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_place_stay_posting_block(uuid,text,text,uuid,text,uuid,text);
DROP FUNCTION IF EXISTS iam_v2.p4_clear_posting_unresolved(uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_stay_feed_confirms(uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_refresh_stay_posting_permission(uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_stay_room_charge_open(uuid);
DROP FUNCTION IF EXISTS iam_v2.pms_answer_confirmation_record(uuid,uuid,uuid,text,text,text,text,uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_answer_effect(uuid,text);
DROP FUNCTION IF EXISTS iam_v2.p4_posting_abort_before_send(uuid,text);
DROP FUNCTION IF EXISTS iam_v2.p4_lock_posting_stay(uuid);
DROP TABLE IF EXISTS iam_v2.stay_posting_blocks;
DROP TABLE IF EXISTS iam_v2.pms_answer_confirmations;
DROP TABLE IF EXISTS iam_v2.posting_presend_aborts;
DROP FUNCTION IF EXISTS iam_v2.stay_posting_blocks_guard();

-- stays: posting_allowed returns to its pre-A1 state (nothing set it true).
UPDATE iam_v2.stays SET posting_allowed = false, posting_block_reason = NULL, posting_permission_source = NULL,
       posting_checked_at = NULL WHERE posting_allowed OR posting_block_reason IS NOT NULL;

ALTER TABLE iam_v2.pms_interface_revisions DROP CONSTRAINT IF EXISTS pms_interface_revisions_posting_target_model_check;
ALTER TABLE iam_v2.pms_interface_revisions RENAME COLUMN posting_target_model TO folio_identity_strategy;
ALTER TABLE iam_v2.pms_interface_revisions ADD CONSTRAINT pms_interface_revisions_folio_identity_strategy_check
  CHECK (folio_identity_strategy IN ('UNSET','GLOBALLY_UNIQUE','UNIQUE_PER_STAY','REUSED_SEQUENTIAL'));
COMMENT ON COLUMN iam_v2.pms_interface_revisions.folio_identity_strategy IS NULL;
ALTER TABLE iam_v2.pms_financial_onboardings DROP CONSTRAINT IF EXISTS pms_financial_onboardings_posting_target_model_check;
ALTER TABLE iam_v2.pms_financial_onboardings RENAME COLUMN posting_target_model TO folio_identity_strategy;
ALTER TABLE iam_v2.pms_financial_onboardings ADD CONSTRAINT pms_financial_onboardings_folio_identity_strategy_check
  CHECK (folio_identity_strategy IN ('GLOBALLY_UNIQUE','UNIQUE_PER_STAY','REUSED_SEQUENTIAL'));

ALTER TABLE iam_v2.posting_attempts DROP CONSTRAINT IF EXISTS posting_attempts_outcome_check;
ALTER TABLE iam_v2.posting_attempts ADD CONSTRAINT posting_attempts_outcome_check
  CHECK (outcome IN ('SENDING','ACKED','UNKNOWN','FAILED'));


CREATE TABLE iam_v2.folios (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    external_folio_id text NOT NULL,
    identity_epoch integer DEFAULT 1 NOT NULL,
    folio_kind text DEFAULT 'GUEST'::text NOT NULL,
    status text DEFAULT 'OPEN'::text NOT NULL,
    CONSTRAINT folios_folio_kind_check CHECK ((folio_kind = ANY (ARRAY['GUEST'::text, 'COMPANY'::text, 'GROUP_MASTER'::text, 'OTHER'::text]))),
    CONSTRAINT folios_status_check CHECK ((status = ANY (ARRAY['OPEN'::text, 'CLOSED'::text])))
);

CREATE TABLE iam_v2.stay_folios (
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    stay_id uuid NOT NULL,
    folio_id uuid NOT NULL,
    is_default_posting_target boolean DEFAULT false NOT NULL
);

ALTER TABLE ONLY iam_v2.folios
    ADD CONSTRAINT folios_pkey PRIMARY KEY (id);

ALTER TABLE ONLY iam_v2.folios
    ADD CONSTRAINT folios_tenant_id_site_id_pms_interface_id_external_folio_id_key UNIQUE (tenant_id, site_id, pms_interface_id, external_folio_id, identity_epoch);

ALTER TABLE ONLY iam_v2.folios
    ADD CONSTRAINT folios_tenant_id_site_id_pms_interface_id_id_key UNIQUE (tenant_id, site_id, pms_interface_id, id);

ALTER TABLE ONLY iam_v2.stay_folios
    ADD CONSTRAINT stay_folios_pkey PRIMARY KEY (stay_id, folio_id);

ALTER TABLE ONLY iam_v2.stay_folios
    ADD CONSTRAINT stay_folios_tenant_id_site_id_pms_interface_id_stay_id_foli_key UNIQUE (tenant_id, site_id, pms_interface_id, stay_id, folio_id);

CREATE UNIQUE INDEX folio_open_identity ON iam_v2.folios USING btree (tenant_id, site_id, pms_interface_id, external_folio_id) WHERE (status = 'OPEN'::text);

CREATE UNIQUE INDEX stay_folio_default ON iam_v2.stay_folios USING btree (stay_id) WHERE is_default_posting_target;

ALTER TABLE ONLY iam_v2.folios
    ADD CONSTRAINT folios_tenant_id_site_id_pms_interface_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces(tenant_id, site_id, id);

ALTER TABLE ONLY iam_v2.stay_folios
    ADD CONSTRAINT stay_folios_tenant_id_site_id_pms_interface_id_folio_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, folio_id) REFERENCES iam_v2.folios(tenant_id, site_id, pms_interface_id, id);

ALTER TABLE ONLY iam_v2.stay_folios
    ADD CONSTRAINT stay_folios_tenant_id_site_id_pms_interface_id_stay_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id) REFERENCES iam_v2.stays(tenant_id, site_id, pms_interface_id, id);

ALTER TABLE iam_v2.pms_postings DROP CONSTRAINT IF EXISTS posting_gnumber_wire_safe;
ALTER TABLE iam_v2.pms_postings DROP COLUMN IF EXISTS g_number;
ALTER TABLE iam_v2.pms_postings ADD COLUMN folio_id uuid;

ALTER TABLE ONLY iam_v2.pms_postings
    ADD CONSTRAINT pms_postings_tenant_id_site_id_pms_interface_id_folio_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, folio_id) REFERENCES iam_v2.folios(tenant_id, site_id, pms_interface_id, id);

DO $g$ BEGIN IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sc_posting_runtime') THEN EXECUTE 'GRANT SELECT ON TABLE iam_v2.folios TO sc_posting_runtime'; END IF; END $g$;

DO $g$ BEGIN IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN EXECUTE 'GRANT SELECT ON TABLE iam_v2.folios TO svc_edged'; END IF; END $g$;

DO $g$ BEGIN IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN EXECUTE 'GRANT SELECT,INSERT ON TABLE iam_v2.folios TO svc_pmsd'; END IF; END $g$;

DO $g$ BEGIN IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sc_posting_runtime') THEN EXECUTE 'GRANT SELECT ON TABLE iam_v2.stay_folios TO sc_posting_runtime'; END IF; END $g$;

DO $g$ BEGIN IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN EXECUTE 'GRANT SELECT ON TABLE iam_v2.stay_folios TO svc_scd'; END IF; END $g$;

DO $g$ BEGIN IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN EXECUTE 'GRANT SELECT ON TABLE iam_v2.stay_folios TO svc_edged'; END IF; END $g$;

DO $g$ BEGIN IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN EXECUTE 'GRANT SELECT,INSERT,UPDATE ON TABLE iam_v2.stay_folios TO svc_pmsd'; END IF; END $g$;

DROP FUNCTION IF EXISTS iam_v2.pms_interface_financial_onboard(uuid,uuid,uuid,uuid,text,text,smallint,text,text,uuid);

CREATE OR REPLACE FUNCTION iam_v2.pms_interface_financial_onboard(p_tenant uuid, p_site uuid, p_iface uuid, p_expected_revision uuid, p_strategy text, p_currency text, p_exponent smallint, p_attestation text, p_reason text, p_operator uuid) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'public', 'pg_temp'
    AS $_$
DECLARE v_current uuid; v_kind text; v_new uuid; v_status text;
BEGIN
  IF p_strategy NOT IN ('GLOBALLY_UNIQUE','UNIQUE_PER_STAY','REUSED_SEQUENTIAL') THEN
    RAISE EXCEPTION 'ONBOARDING_STRATEGY: a concrete folio identity strategy is required' USING ERRCODE = 'check_violation';
  END IF;
  IF p_currency IS NULL OR p_currency !~ '^[A-Z]{3}$' OR p_exponent IS NULL OR p_exponent NOT BETWEEN 0 AND 4 THEN
    RAISE EXCEPTION 'ONBOARDING_CURRENCY: a three-letter currency and an exponent 0..4 are required' USING ERRCODE = 'check_violation';
  END IF;
  IF length(btrim(coalesce(p_attestation,''))) < 20 THEN
    RAISE EXCEPTION 'ONBOARDING_ATTESTATION: state what was observed about how this PMS identifies folios' USING ERRCODE = 'check_violation';
  END IF;
  IF length(btrim(coalesce(p_reason,''))) < 4 THEN
    RAISE EXCEPTION 'ONBOARDING_REASON: a reason is required' USING ERRCODE = 'check_violation';
  END IF;
  SELECT status INTO v_status FROM public.operators WHERE id = p_operator AND tenant_id = p_tenant;
  IF v_status IS DISTINCT FROM 'active' THEN
    RAISE EXCEPTION 'ONBOARDING_ACTOR: an active operator of this tenant must approve onboarding' USING ERRCODE = 'check_violation';
  END IF;
  SELECT current_revision_id, connector_kind INTO v_current, v_kind FROM iam_v2.pms_interfaces
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_iface FOR UPDATE;
  IF v_current IS NULL THEN
    RAISE EXCEPTION 'ONBOARDING_INTERFACE: no published revision for this interface' USING ERRCODE = 'no_data_found';
  END IF;
  IF v_current IS DISTINCT FROM p_expected_revision THEN
    RAISE EXCEPTION 'ONBOARDING_REVISION_CONFLICT: the interface changed while this form was open' USING ERRCODE = 'serialization_failure';
  END IF;
  IF v_kind <> 'protel-fias' THEN
    RAISE EXCEPTION 'ONBOARDING_CONNECTOR: room charge is supported for FIAS interfaces only' USING ERRCODE = 'check_violation';
  END IF;
  IF p_exponent <> 2 THEN
    RAISE EXCEPTION 'ONBOARDING_EXPONENT: FIAS transmits amounts with exponent 2' USING ERRCODE = 'check_violation';
  END IF;
  INSERT INTO iam_v2.pms_interface_revisions
    (tenant_id, site_id, pms_interface_id, revision_no, source_timezone, folio_identity_strategy, config,
     normalization_version, source_fingerprint, financial_base_currency, financial_base_currency_exponent)
  SELECT r.tenant_id, r.site_id, r.pms_interface_id,
         (SELECT COALESCE(MAX(x.revision_no),0)+1 FROM iam_v2.pms_interface_revisions x
           WHERE x.tenant_id = r.tenant_id AND x.site_id = r.site_id AND x.pms_interface_id = r.pms_interface_id),
         r.source_timezone, p_strategy, r.config, r.normalization_version, r.source_fingerprint, p_currency, p_exponent
    FROM iam_v2.pms_interface_revisions r WHERE r.id = v_current
  RETURNING id INTO v_new;
  UPDATE iam_v2.pms_interfaces SET current_revision_id = v_new
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_iface;
  INSERT INTO iam_v2.pms_financial_onboardings
    (tenant_id, site_id, pms_interface_id, revision_id, previous_revision_id, folio_identity_strategy, currency,
     currency_exponent, attestation, reason, approved_by)
  VALUES (p_tenant, p_site, p_iface, v_new, v_current, p_strategy, p_currency, p_exponent, btrim(p_attestation),
          btrim(p_reason), p_operator);
  RETURN v_new;
END $_$;

CREATE OR REPLACE FUNCTION iam_v2.pms_interface_financially_ready(p_tenant uuid, p_site uuid, p_iface uuid) RETURNS TABLE(ready boolean, reason text, currency character, currency_exponent smallint)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE i record; r record;
BEGIN
  SELECT * INTO i FROM iam_v2.pms_interfaces WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_iface;
  IF i.id IS NULL THEN ready := false; reason := 'INTERFACE_UNKNOWN'; RETURN NEXT; RETURN; END IF;
  IF i.connector_kind <> 'protel-fias' THEN ready := false; reason := 'CONNECTOR_NOT_FINANCIAL'; RETURN NEXT; RETURN; END IF;
  IF i.lifecycle_state NOT IN ('ACTIVE','AUTH_DISABLED') THEN ready := false; reason := 'INTERFACE_NOT_ACTIVE'; RETURN NEXT; RETURN; END IF;
  SELECT * INTO r FROM iam_v2.pms_interface_revisions WHERE id = i.current_revision_id;
  currency := r.financial_base_currency; currency_exponent := r.financial_base_currency_exponent;
  IF r.folio_identity_strategy = 'UNSET' OR r.financial_base_currency IS NULL THEN
    ready := false; reason := 'NOT_ONBOARDED'; RETURN NEXT; RETURN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM iam_v2.pms_financial_onboardings o WHERE o.revision_id = r.id) THEN
    ready := false; reason := 'ONBOARDING_NOT_APPROVED'; RETURN NEXT; RETURN;
  END IF;
  ready := true; reason := NULL; RETURN NEXT;
END $$;

CREATE OR REPLACE FUNCTION iam_v2.trg_posting_charge_gate() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE strat text; st text; pa boolean;
BEGIN
  IF NEW.posting_type = 'CHARGE' THEN
    SELECT folio_identity_strategy INTO strat FROM iam_v2.pms_interface_revisions
      WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND pms_interface_id=NEW.pms_interface_id AND id=NEW.posting_interface_revision_id;
    IF strat IS NULL OR strat='UNSET' THEN
      RAISE EXCEPTION 'FOLIO_STRATEGY_UNSET: financial CHARGE blocked fail-closed (interface %, revision %)', NEW.pms_interface_id, NEW.posting_interface_revision_id
        USING ERRCODE='check_violation';
    END IF;
    IF NEW.stay_id IS NOT NULL THEN
      SELECT status, posting_allowed INTO st, pa FROM iam_v2.stays
        WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND pms_interface_id=NEW.pms_interface_id AND id=NEW.stay_id;
      IF st IS DISTINCT FROM 'IN_HOUSE' OR pa IS NOT TRUE THEN
        RAISE EXCEPTION 'POSTING_NOT_ALLOWED: stay % not IN_HOUSE/posting_allowed', NEW.stay_id USING ERRCODE='check_violation';
      END IF;
    END IF;
  END IF;
  RETURN NEW;
END; $$;

CREATE OR REPLACE FUNCTION iam_v2.p4_create_room_charge_posting(p_tenant uuid, p_site uuid, p_settlement uuid) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE se record; pu record; ac record; i record; rdy record; v_folio uuid; v_posting uuid;
BEGIN
  SELECT * INTO se FROM iam_v2.settlements WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_settlement FOR UPDATE;
  IF se.id IS NULL OR se.method <> 'PMS_POSTING' OR se.status <> 'REQUIRED' THEN
    RAISE EXCEPTION 'ROOM_CHARGE_SETTLEMENT: not a REQUIRED room-charge settlement' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO pu FROM iam_v2.purchases WHERE id = se.purchase_id FOR UPDATE;
  IF pu.state <> 'AWAITING_SETTLEMENT' OR pu.stay_id IS NULL OR pu.pms_interface_id IS NULL OR pu.settlement_mapping_id IS NULL THEN
    RAISE EXCEPTION 'ROOM_CHARGE_PURCHASE: the purchase does not pin a stay, interface and mapping' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO ac FROM iam_v2.auth_contexts WHERE id = pu.auth_context_id;
  IF ac.method <> 'PMS' OR ac.stay_id IS DISTINCT FROM pu.stay_id THEN
    RAISE EXCEPTION 'ROOM_CHARGE_AUTHENTICATION: a room charge needs a verified Room sign-in for the same stay' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO rdy FROM iam_v2.pms_interface_financially_ready(p_tenant, p_site, pu.pms_interface_id);
  IF NOT rdy.ready THEN
    RAISE EXCEPTION 'ROOM_CHARGE_NOT_READY: %', rdy.reason USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO i FROM iam_v2.pms_interfaces WHERE id = pu.pms_interface_id;
  SELECT sf.folio_id INTO v_folio FROM iam_v2.stay_folios sf
   WHERE sf.tenant_id = p_tenant AND sf.site_id = p_site AND sf.pms_interface_id = pu.pms_interface_id
     AND sf.stay_id = pu.stay_id AND sf.is_default_posting_target;
  IF v_folio IS NULL THEN
    RAISE EXCEPTION 'ROOM_CHARGE_NO_FOLIO: the stay has no default posting folio' USING ERRCODE = 'check_violation';
  END IF;
  INSERT INTO iam_v2.pms_postings
    (tenant_id, site_id, pms_interface_id, settlement_id, purchase_id, stay_id, folio_id,
     posting_interface_revision_id, posting_type, amount_minor, currency, currency_exponent, idempotency_key)
  VALUES (p_tenant, p_site, pu.pms_interface_id, se.id, pu.id, pu.stay_id, v_folio,
          i.current_revision_id, 'CHARGE', pu.amount_minor, pu.currency, pu.currency_exponent,
          'room-charge:' || se.id::text)
  RETURNING id INTO v_posting;
  INSERT INTO iam_v2.posting_outbox (tenant_id, site_id, pms_interface_id, posting_id, state)
  VALUES (p_tenant, p_site, pu.pms_interface_id, v_posting, 'QUEUED');
  UPDATE iam_v2.settlements SET status = 'IN_PROGRESS' WHERE id = se.id;
  RETURN v_posting;
END $$;

CREATE OR REPLACE FUNCTION iam_v2.p4_posting_command_authorised(p_iface uuid, p_pnum text, p_sha256 text) RETURNS text
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $_$
DECLARE a record; n int; v_outbox text; v_type text; rdy record;
BEGIN
  IF p_iface IS NULL OR p_pnum IS NULL OR p_pnum !~ '^[0-9]{1,18}$' OR p_sha256 IS NULL OR p_sha256 !~ '^[0-9a-f]{64}$' THEN
    RETURN 'COMMAND_MALFORMED';
  END IF;
  SELECT count(*) INTO n FROM iam_v2.posting_attempts WHERE pms_interface_id = p_iface AND p_number = p_pnum;
  IF n = 0 THEN RETURN 'NO_SUCH_ATTEMPT'; END IF;
  IF n > 1 THEN RETURN 'P_NUMBER_NOT_UNIQUE'; END IF;
  SELECT * INTO a FROM iam_v2.posting_attempts x WHERE x.pms_interface_id = p_iface AND x.p_number = p_pnum;
  IF a.outcome <> 'SENDING' THEN RETURN 'ATTEMPT_NOT_SENDING'; END IF;
  IF a.ps_sha256 IS NULL OR a.ps_sha256 <> p_sha256 THEN RETURN 'COMMAND_BYTES_NOT_AUTHORISED'; END IF;
  IF a.sent_at < now() - interval '2 minutes' THEN RETURN 'ATTEMPT_TOO_OLD'; END IF;
  SELECT p.posting_type INTO v_type FROM iam_v2.pms_postings p WHERE p.id = a.internal_posting_id;
  IF v_type IS DISTINCT FROM 'CHARGE' THEN RETURN 'NOT_A_CHARGE'; END IF;
  SELECT o.state INTO v_outbox FROM iam_v2.posting_outbox o WHERE o.posting_id = a.internal_posting_id;
  IF v_outbox IS DISTINCT FROM 'IN_FLIGHT' THEN RETURN 'POSTING_NOT_IN_FLIGHT'; END IF;
  SELECT * INTO rdy FROM iam_v2.pms_interface_financially_ready(a.tenant_id, a.site_id, p_iface);
  IF NOT COALESCE(rdy.ready, false) THEN RETURN 'INTERFACE_NOT_FINANCIALLY_READY'; END IF;
  RETURN 'AUTHORISED';
END $_$;

CREATE OR REPLACE FUNCTION iam_v2.p4_posting_settlement_outcome(p_posting uuid) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE p record; se record; a record; v_target text; v_ent uuid;
BEGIN
  SELECT * INTO p FROM iam_v2.pms_postings WHERE id = p_posting AND posting_type = 'CHARGE';
  IF p.id IS NULL THEN RAISE EXCEPTION 'POSTING_UNKNOWN' USING ERRCODE = 'no_data_found'; END IF;
  SELECT * INTO se FROM iam_v2.settlements WHERE id = p.settlement_id FOR UPDATE;
  IF se.method <> 'PMS_POSTING' THEN RETURN 'NOT_A_ROOM_CHARGE'; END IF;
  SELECT * INTO a FROM iam_v2.posting_attempts WHERE internal_posting_id = p.id ORDER BY attempt_no DESC LIMIT 1;
  IF a.id IS NULL THEN RETURN 'NO_ATTEMPT'; END IF;
  IF a.outcome = 'ACKED' AND a.pa_as_status = 'OK' THEN
    v_target := 'SETTLED';
  ELSIF a.outcome = 'ACKED' THEN
    v_target := 'FAILED';
  ELSIF a.outcome = 'UNKNOWN' THEN
    v_target := 'MANUAL_REVIEW';
  ELSE
    RETURN 'NOT_CONCLUSIVE';
  END IF;
  IF se.status = v_target OR se.status IN ('FAILED','PARTIALLY_REVERSED','REVERSED') THEN
    NULL;
  ELSIF se.status IN ('IN_PROGRESS','MANUAL_REVIEW') THEN
    IF NOT (se.status = 'MANUAL_REVIEW' AND v_target = 'MANUAL_REVIEW') THEN
      UPDATE iam_v2.settlements SET status = v_target WHERE id = se.id;
    END IF;
  ELSE
    RAISE EXCEPTION 'POSTING_SETTLEMENT_STATE: settlement is %', se.status USING ERRCODE = 'check_violation';
  END IF;
  IF v_target = 'SETTLED' THEN
    SELECT entitlement_id INTO v_ent FROM iam_v2.p4_grant_paid_entitlement(p.tenant_id, p.site_id, se.id);
    RETURN 'GRANTED';
  END IF;
  RETURN v_target;
END $$;

CREATE OR REPLACE FUNCTION iam_v2.p4_posting_review_apply(p_tenant uuid, p_site uuid, p_posting uuid) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE p record; se record; rs record; v_ent uuid;
BEGIN
  SELECT * INTO p FROM iam_v2.pms_postings WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_posting AND posting_type = 'CHARGE';
  IF p.id IS NULL THEN RAISE EXCEPTION 'POSTING_UNKNOWN' USING ERRCODE = 'no_data_found'; END IF;
  SELECT * INTO rs FROM iam_v2.posting_review_state WHERE posting_id = p.id;
  IF rs.terminal_action IS NULL THEN RETURN 'NO_DECISION'; END IF;
  SELECT * INTO se FROM iam_v2.settlements WHERE id = p.settlement_id FOR UPDATE;
  IF rs.terminal_action = 'CONFIRM_NOT_POSTED_RETRY' THEN
    UPDATE iam_v2.posting_outbox SET state = 'QUEUED'
     WHERE posting_id = p.id AND state = 'HELD_RECOVERY' AND rs.retry_authorized_attempt_no IS NOT NULL
       AND rs.retry_authorization_consumed_at IS NULL;
    RETURN 'REQUEUED';
  END IF;
  IF se.method <> 'PMS_POSTING' THEN RETURN 'NOT_A_ROOM_CHARGE'; END IF;
  IF rs.terminal_action = 'CONFIRM_POSTED' THEN
    IF se.status IN ('IN_PROGRESS','MANUAL_REVIEW') THEN
      UPDATE iam_v2.settlements SET status = 'SETTLED' WHERE id = se.id;
    END IF;
    IF (SELECT status FROM iam_v2.settlements WHERE id = se.id) = 'SETTLED' THEN
      SELECT entitlement_id INTO v_ent FROM iam_v2.p4_grant_paid_entitlement(p.tenant_id, p.site_id, se.id);
      RETURN 'GRANTED';
    END IF;
    RETURN 'NOT_SETTLED';
  END IF;
  IF rs.terminal_action = 'CONFIRM_NOT_POSTED_ABANDON' THEN
    IF se.status IN ('IN_PROGRESS','MANUAL_REVIEW') THEN
      UPDATE iam_v2.settlements SET status = 'FAILED' WHERE id = se.id;
    END IF;
    RETURN 'FAILED';
  END IF;
  RETURN 'NO_EFFECT';
END $$;

CREATE OR REPLACE FUNCTION iam_v2.request_full_resync(p_tenant uuid, p_site uuid, p_interface uuid, p_reason text) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_id uuid;
BEGIN
  -- The reason vocabulary is closed HERE as well as in edged. A bounded set enforced only in the process that
  -- happens to call today is not a bound; it is a convention that survives until the next caller.
  IF p_reason IS NULL OR p_reason NOT IN
     ('SUSPECTED_STALE_GUEST_LIST','AFTER_PMS_MAINTENANCE','OPERATOR_VERIFICATION','SUPPORT_REQUEST') THEN
    RAISE EXCEPTION 'unbounded resync reason' USING ERRCODE = 'check_violation';
  END IF;

  UPDATE iam_v2.pms_interface_runtime rt
     SET resync_command_id            = gen_random_uuid(),
         resync_command_requested_at  = now(),
         resync_command_reason        = p_reason,
         resync_command_generation    = rt.runtime_generation,
         resync_command_claimed_at    = NULL,
         sync_stage                   = 'REQUESTING_FULL_SYNC',
         sync_stage_at                = now(),
         sync_failure_code            = NULL,
         updated_at                   = now()
    FROM iam_v2.pms_interfaces pi
   WHERE pi.tenant_id = rt.tenant_id AND pi.site_id = rt.site_id AND pi.id = rt.pms_interface_id
     AND rt.tenant_id = p_tenant AND rt.site_id = p_site AND rt.pms_interface_id = p_interface
     -- there is a worker with a live socket to receive this
     AND pi.lifecycle_state = 'ACTIVE'
     AND rt.transport_status = 'CONNECTED'
     -- a resync is not already running, and no earlier request is still waiting to be claimed
     AND rt.sync_status <> 'RESYNC_IN_PROGRESS'
     AND rt.resync_command_id IS NULL
  RETURNING rt.resync_command_id INTO v_id;

  RETURN v_id; -- NULL when no row qualified; the caller explains which precondition failed
END;
$$;

CREATE OR REPLACE FUNCTION iam_v2.record_posting_review_action(p_posting uuid, p_action text, p_actor uuid, p_reason text, p_evidence jsonb DEFAULT '{}'::jsonb, p_expected_version integer DEFAULT NULL::integer, p_reversal_amount bigint DEFAULT NULL::bigint) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  v_t uuid; v_s uuid; st record; v_action_id uuid; v_next_attempt int; v_attempts int; la record;
  o record; v_amount bigint; v_rev uuid;
BEGIN
  IF p_action NOT IN ('CONFIRM_POSTED','CONFIRM_NOT_POSTED_RETRY','CONFIRM_NOT_POSTED_ABANDON',
                      'CREATE_REVERSAL','ESCALATE') THEN
    RAISE EXCEPTION 'REVIEW_ACTION_UNKNOWN: % is not in the approved review catalog', p_action
      USING ERRCODE = 'check_violation';
  END IF;
  IF p_actor IS NULL OR p_reason IS NULL OR btrim(p_reason) = '' THEN
    RAISE EXCEPTION 'REVIEW_ACTOR_REASON_REQUIRED: every financial review decision is attributable'
      USING ERRCODE = 'check_violation';
  END IF;
  IF p_action <> 'ESCALATE'
     AND (p_evidence IS NULL OR jsonb_typeof(p_evidence) <> 'object' OR p_evidence = '{}'::jsonb) THEN
    RAISE EXCEPTION 'REVIEW_EVIDENCE_REQUIRED: a terminal financial decision must record its evidence'
      USING ERRCODE = 'check_violation';
  END IF;

  SELECT * INTO o FROM iam_v2.pms_postings WHERE id = p_posting;
  IF o.id IS NULL THEN
    RAISE EXCEPTION 'REVIEW_POSTING_UNKNOWN: posting % does not exist', p_posting
      USING ERRCODE = 'foreign_key_violation';
  END IF;
  v_t := o.tenant_id; v_s := o.site_id;

  PERFORM pg_advisory_xact_lock(iam_v2.ns_financial_review(p_posting::text));
  INSERT INTO iam_v2.posting_review_state (posting_id, tenant_id, site_id)
  VALUES (p_posting, v_t, v_s) ON CONFLICT (posting_id) DO NOTHING;
  SELECT * INTO st FROM iam_v2.posting_review_state WHERE posting_id = p_posting FOR UPDATE;

  IF p_expected_version IS NOT NULL AND p_expected_version <> st.review_version THEN
    RAISE EXCEPTION 'REVIEW_VERSION_STALE: expected version %, current is %',
      p_expected_version, st.review_version USING ERRCODE = 'serialization_failure';
  END IF;

  SELECT count(*) INTO v_attempts FROM iam_v2.posting_attempts WHERE internal_posting_id = p_posting;
  IF v_attempts = 0 AND p_action <> 'ESCALATE' THEN
    RAISE EXCEPTION 'REVIEW_NOT_APPLICABLE: posting % has no transmission attempt to decide about', p_posting
      USING ERRCODE = 'check_violation';
  END IF;

  IF p_action <> 'ESCALATE' AND st.terminal_action IS NOT NULL THEN
    IF st.terminal_action = p_action THEN
      RAISE EXCEPTION 'REVIEW_ALREADY_DECIDED: posting % is already decided as %', p_posting, st.terminal_action
        USING ERRCODE = 'unique_violation';
    END IF;
    RAISE EXCEPTION 'REVIEW_CONFLICT: posting % is already decided as %; % is incompatible',
      p_posting, st.terminal_action, p_action USING ERRCODE = 'unique_violation';
  END IF;

  SELECT attempt_no, outcome, pa_as_status INTO la
    FROM iam_v2.posting_attempts WHERE internal_posting_id = p_posting
    ORDER BY attempt_no DESC LIMIT 1;

  -- THE ACTION/STATE MATRIX.
  IF p_action = 'CONFIRM_NOT_POSTED_RETRY' THEN
    IF la.outcome = 'ACKED' AND la.pa_as_status = 'OK' THEN
      RAISE EXCEPTION 'REVIEW_RETRY_REFUSED: attempt % was ACKed OK by the PMS; retrying it would post the '
                      'charge twice. Use CREATE_REVERSAL or CONFIRM_POSTED.', la.attempt_no
        USING ERRCODE = 'check_violation';
    END IF;
    IF la.outcome = 'SENDING' THEN
      RAISE EXCEPTION 'REVIEW_RETRY_REFUSED: attempt % is still SENDING; its outcome is not yet known',
        la.attempt_no USING ERRCODE = 'check_violation';
    END IF;
  END IF;

  -- CREATE_REVERSAL corrects money the PMS is believed to hold. Reversing a charge nobody thinks was
  -- posted would put a correction in the ledger for a debit that never happened.
  IF p_action = 'CREATE_REVERSAL' THEN
    IF o.posting_type <> 'CHARGE' THEN
      RAISE EXCEPTION 'REVIEW_REVERSAL_REFUSED: only a CHARGE can be reversed' USING ERRCODE = 'check_violation';
    END IF;
    IF la.outcome = 'SENDING' THEN
      RAISE EXCEPTION 'REVIEW_REVERSAL_REFUSED: attempt % is still SENDING; its outcome is not yet known',
        la.attempt_no USING ERRCODE = 'check_violation';
    END IF;
    IF NOT (la.outcome = 'UNKNOWN' OR (la.outcome = 'ACKED' AND la.pa_as_status = 'OK')) THEN
      RAISE EXCEPTION 'REVIEW_REVERSAL_REFUSED: the latest attempt is %/%; nothing is believed posted, so '
                      'there is nothing to reverse. Use CONFIRM_NOT_POSTED_ABANDON.',
        la.outcome, coalesce(la.pa_as_status,'-') USING ERRCODE = 'check_violation';
    END IF;
    v_amount := coalesce(p_reversal_amount, o.amount_minor);
  ELSIF p_reversal_amount IS NOT NULL THEN
    RAISE EXCEPTION 'REVIEW_AMOUNT_NOT_APPLICABLE: only CREATE_REVERSAL carries an amount'
      USING ERRCODE = 'check_violation';
  END IF;

  PERFORM set_config('iam_v2.p4_review_writer', txid_current()::text, true);

  INSERT INTO iam_v2.posting_review_actions (tenant_id, site_id, posting_id, action, actor, reason, evidence)
  VALUES (v_t, v_s, p_posting, p_action, p_actor, p_reason, coalesce(p_evidence, '{}'::jsonb))
  RETURNING id INTO v_action_id;

  IF p_action = 'CREATE_REVERSAL' THEN
    -- §15: "a new ledger row referencing the original". It pins the SAME evidence the original pinned, so
    -- the correction is attached to the same authorization rather than to a fresh resolution.
    INSERT INTO iam_v2.pms_postings
      (tenant_id, site_id, pms_interface_id, settlement_id, purchase_id, stay_id, folio_id,
       posting_interface_revision_id, secret_generation_id, posting_type, reverses_posting_id,
       amount_minor, currency, currency_exponent, idempotency_key)
    VALUES (o.tenant_id, o.site_id, o.pms_interface_id, o.settlement_id, o.purchase_id, o.stay_id, o.folio_id,
            o.posting_interface_revision_id, o.secret_generation_id, 'REVERSAL', o.id,
            v_amount, o.currency, o.currency_exponent, o.idempotency_key || ':rev:' || v_action_id::text)
    RETURNING id INTO v_rev;
  END IF;

  PERFORM set_config('iam_v2.p4_review_writer', '', true);

  IF p_action = 'ESCALATE' THEN
    UPDATE iam_v2.posting_review_state
       SET escalation_count = escalation_count + 1, review_version = review_version + 1, updated_at = now()
     WHERE posting_id = p_posting;
  ELSE
    IF p_action = 'CONFIRM_NOT_POSTED_RETRY' THEN
      SELECT coalesce(max(attempt_no), 0) + 1 INTO v_next_attempt
        FROM iam_v2.posting_attempts WHERE internal_posting_id = p_posting;
    ELSE
      v_next_attempt := NULL;
    END IF;
    UPDATE iam_v2.posting_review_state
       SET terminal_action = p_action, terminal_action_id = v_action_id, decided_at = now(),
           retry_authorized_attempt_no = v_next_attempt, reversal_posting_id = v_rev,
           review_version = review_version + 1, updated_at = now()
     WHERE posting_id = p_posting;
  END IF;

  RETURN v_action_id;
END $$;

ALTER FUNCTION iam_v2.trg_posting_charge_gate() SECURITY INVOKER;
ALTER FUNCTION iam_v2.trg_posting_charge_gate() RESET search_path;

CREATE OR REPLACE VIEW iam_v2.posting_execution_state AS
 SELECT p.id AS posting_id,
    p.tenant_id,
    p.site_id,
    p.pms_interface_id,
    p.posting_type,
    p.amount_minor,
    p.currency,
    p.currency_exponent,
    p.idempotency_key,
    p.created_at,
        CASE
            WHEN (la.attempt_no IS NULL) THEN 'NOT_ATTEMPTED'::text
            WHEN (la.outcome = 'SENDING'::text) THEN 'IN_FLIGHT'::text
            WHEN (la.outcome = 'UNKNOWN'::text) THEN 'UNKNOWN'::text
            WHEN (la.outcome = 'FAILED'::text) THEN 'NOT_SENT'::text
            WHEN ((la.outcome = 'ACKED'::text) AND (la.pa_as_status = 'OK'::text)) THEN 'POSTED'::text
            WHEN (la.outcome = 'ACKED'::text) THEN 'REJECTED'::text
            ELSE NULL::text
        END AS execution_state,
    la.attempt_no AS latest_attempt_no,
    la.p_number AS latest_p_number,
    la.outcome AS latest_attempt_outcome,
    la.pa_as_status AS latest_pa_as_status,
    ac.attempt_count,
    ac.unknown_attempt_count,
    (ac.unknown_attempt_count > 0) AS has_unknown_history,
    ob.state AS outbox_state,
    rs.terminal_action AS terminal_review_action,
    rs.review_version,
    rs.escalation_count,
    rs.retry_authorized_attempt_no,
    (rs.retry_authorization_consumed_at IS NOT NULL) AS retry_authorization_consumed,
    ((la.outcome = 'UNKNOWN'::text) AND (rs.terminal_action IS NULL)) AS awaiting_manual_review,
    iam_v2.p4_interface_freshness_block(p.tenant_id, p.site_id, p.pms_interface_id, p.posting_interface_revision_id, now()) AS freshness_block
   FROM ((((iam_v2.pms_postings p
     LEFT JOIN LATERAL ( SELECT a.attempt_no,
            a.outcome,
            a.p_number,
            a.pa_as_status
           FROM iam_v2.posting_attempts a
          WHERE (a.internal_posting_id = p.id)
          ORDER BY a.attempt_no DESC
         LIMIT 1) la ON (true))
     LEFT JOIN LATERAL ( SELECT count(*) AS attempt_count,
            count(*) FILTER (WHERE (a.outcome = 'UNKNOWN'::text)) AS unknown_attempt_count
           FROM iam_v2.posting_attempts a
          WHERE (a.internal_posting_id = p.id)) ac ON (true))
     LEFT JOIN LATERAL ( SELECT o.state
           FROM iam_v2.posting_outbox o
          WHERE ((o.posting_id = p.id) AND (o.state = ANY (ARRAY['QUEUED'::text, 'IN_FLIGHT'::text, 'HELD_RECOVERY'::text])))
         LIMIT 1) ob ON (true))
     LEFT JOIN iam_v2.posting_review_state rs ON ((rs.posting_id = p.id)));

DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.pms_interface_financial_onboard(uuid,uuid,uuid,uuid,text,text,smallint,text,text,uuid) TO svc_edged;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.pms_interface_financial_onboard(uuid,uuid,uuid,uuid,text,text,smallint,text,text,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.folios OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.stay_folios OWNER TO iam_v2_owner';
  END IF;
END
$grant$;
REVOKE ALL ON FUNCTION iam_v2.pms_interface_financial_onboard(uuid,uuid,uuid,uuid,text,text,smallint,text,text,uuid) FROM PUBLIC;

COMMIT;
