-- MG-9  Engine components: triggers + functions (iam_v2). Single transaction.
BEGIN;

-- ---- immutability / append-only ----
CREATE FUNCTION iam_v2.trg_reject_update_delete() RETURNS trigger AS $$
BEGIN RAISE EXCEPTION '% is immutable (no UPDATE/DELETE) on %', TG_TABLE_NAME, TG_OP; END; $$ LANGUAGE plpgsql;

-- fully immutable revisions
CREATE TRIGGER imm_pms_rev  BEFORE UPDATE OR DELETE ON iam_v2.pms_interface_revisions   FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();
CREATE TRIGGER imm_plan_rev BEFORE UPDATE OR DELETE ON iam_v2.service_plan_revisions    FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();
CREATE TRIGGER imm_pkg_rev  BEFORE UPDATE OR DELETE ON iam_v2.internet_package_revisions FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();
-- fully append-only ledgers
CREATE TRIGGER ao_accounting BEFORE UPDATE OR DELETE ON iam_v2.accounting_records      FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();
CREATE TRIGGER ao_pa_events  BEFORE UPDATE OR DELETE ON iam_v2.posting_attempt_events  FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();
CREATE TRIGGER ao_adjust     BEFORE UPDATE OR DELETE ON iam_v2.entitlement_adjustments FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();
CREATE TRIGGER ao_review     BEFORE UPDATE OR DELETE ON iam_v2.posting_review_actions  FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();
CREATE TRIGGER ao_postings   BEFORE UPDATE OR DELETE ON iam_v2.pms_postings            FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();

-- secret generations: identity immutable, only superseded_at mutable; never DELETE
CREATE FUNCTION iam_v2.trg_secret_gen_guard() RETURNS trigger AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'secret generations are not deletable'; END IF;
  IF ROW(NEW.ciphertext,NEW.nonce,NEW.encryption_key_id,NEW.cipher_version,NEW.generation_no,NEW.pms_interface_id)
     IS DISTINCT FROM ROW(OLD.ciphertext,OLD.nonce,OLD.encryption_key_id,OLD.cipher_version,OLD.generation_no,OLD.pms_interface_id)
  THEN RAISE EXCEPTION 'secret generation identity is immutable (only superseded_at may change)'; END IF;
  RETURN NEW;
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER sg_guard BEFORE UPDATE OR DELETE ON iam_v2.pms_interface_secret_generations FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_secret_gen_guard();

-- ---- posting_attempts one-way state ----
CREATE FUNCTION iam_v2.trg_posting_attempt_oneway() RETURNS trigger AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'posting_attempts is not deletable'; END IF;
  IF ROW(NEW.p_number,NEW.rn,NEW.g_number,NEW.sent_at,NEW.internal_posting_id,NEW.attempt_no,NEW.pms_interface_id)
     IS DISTINCT FROM ROW(OLD.p_number,OLD.rn,OLD.g_number,OLD.sent_at,OLD.internal_posting_id,OLD.attempt_no,OLD.pms_interface_id)
  THEN RAISE EXCEPTION 'posting_attempts identity is immutable'; END IF;
  IF OLD.outcome <> 'SENDING' AND NEW.outcome <> OLD.outcome THEN
     RAISE EXCEPTION 'posting_attempts.outcome is terminal (% -> %)', OLD.outcome, NEW.outcome; END IF;
  IF NEW.outcome = 'SENDING' AND OLD.outcome <> 'SENDING' THEN
     RAISE EXCEPTION 'posting_attempts.outcome cannot return to SENDING'; END IF;
  RETURN NEW;
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER pa_oneway BEFORE UPDATE OR DELETE ON iam_v2.posting_attempts FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_posting_attempt_oneway();

-- ---- entitlement guard: no exit from TERMINATED; counter decrease/window move only via adjustment; same-subject supersession ----
CREATE FUNCTION iam_v2.trg_entitlement_guard() RETURNS trigger AS $$
DECLARE s_key text; os_key text;
BEGIN
  IF TG_OP='UPDATE' THEN
    IF OLD.status='TERMINATED' AND NEW.status<>'TERMINATED' THEN
      RAISE EXCEPTION 'no transition out of TERMINATED'; END IF;
    IF ( NEW.consumed_data_bytes < OLD.consumed_data_bytes
      OR NEW.consumed_online_seconds < OLD.consumed_online_seconds
      OR (OLD.window_ends_at IS NOT NULL AND NEW.window_ends_at IS DISTINCT FROM OLD.window_ends_at) )
      AND current_setting('iam_v2.allow_adjust', true) IS DISTINCT FROM 'on'
    THEN RAISE EXCEPTION 'counter decrease / window move only via entitlement_adjustments'; END IF;
  END IF;
  IF NEW.supersedes_entitlement_id IS NOT NULL THEN
    SELECT CASE WHEN stay_id IS NOT NULL THEN 'stay:'||stay_id WHEN guest_account_id IS NOT NULL THEN 'acct:'||guest_account_id
                WHEN voucher_id IS NOT NULL THEN 'vou:'||voucher_id ELSE 'prin:'||guest_principal_id END
      INTO os_key FROM iam_v2.entitlements WHERE id=NEW.supersedes_entitlement_id;
    s_key := CASE WHEN NEW.stay_id IS NOT NULL THEN 'stay:'||NEW.stay_id WHEN NEW.guest_account_id IS NOT NULL THEN 'acct:'||NEW.guest_account_id
                  WHEN NEW.voucher_id IS NOT NULL THEN 'vou:'||NEW.voucher_id ELSE 'prin:'||NEW.guest_principal_id END;
    IF s_key IS DISTINCT FROM os_key THEN RAISE EXCEPTION 'cross-subject supersession rejected (% vs %)', s_key, os_key; END IF;
  END IF;
  RETURN NEW;
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER ent_guard BEFORE INSERT OR UPDATE ON iam_v2.entitlements FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_entitlement_guard();

-- audited adjustment: the ONLY sanctioned way to decrease a counter / move a window
CREATE FUNCTION iam_v2.apply_adjustment(p_ent uuid, p_field text, p_new text, p_actor uuid, p_reason text)
RETURNS void AS $$
DECLARE t uuid; s uuid; oldv text;
BEGIN
  SELECT tenant_id, site_id INTO t, s FROM iam_v2.entitlements WHERE id=p_ent;
  PERFORM set_config('iam_v2.allow_adjust','on', true);
  IF p_field='consumed_data_bytes' THEN
    SELECT consumed_data_bytes::text INTO oldv FROM iam_v2.entitlements WHERE id=p_ent;
    UPDATE iam_v2.entitlements SET consumed_data_bytes=p_new::bigint, usage_version=usage_version+1 WHERE id=p_ent;
  ELSIF p_field='window_ends_at' THEN
    SELECT window_ends_at::text INTO oldv FROM iam_v2.entitlements WHERE id=p_ent;
    UPDATE iam_v2.entitlements SET window_ends_at=p_new::timestamptz, usage_version=usage_version+1 WHERE id=p_ent;
  ELSE RAISE EXCEPTION 'unsupported adjustment field %', p_field; END IF;
  INSERT INTO iam_v2.entitlement_adjustments(tenant_id,site_id,entitlement_id,field,old_value,new_value,actor,reason)
    VALUES (t,s,p_ent,p_field,oldv,p_new,p_actor,p_reason);
  PERFORM set_config('iam_v2.allow_adjust','off', true);
END; $$ LANGUAGE plpgsql;

-- ---- folio-UNSET fail-closed CHARGE gate (before outbox/P#/transmission) + IN_HOUSE re-check ----
CREATE FUNCTION iam_v2.trg_posting_charge_gate() RETURNS trigger AS $$
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
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER charge_gate BEFORE INSERT ON iam_v2.pms_postings FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_posting_charge_gate();

-- ---- lock-namespace constants (verified from data-plane/internal/session/session.go) ----
CREATE FUNCTION iam_v2.ns_device_slot(p text) RETURNS bigint AS $$ SELECT hashtextextended(p, 11) $$ LANGUAGE sql IMMUTABLE;  -- LN_DEVICE_SLOT=11
CREATE FUNCTION iam_v2.ns_capacity(p text)    RETURNS bigint AS $$ SELECT hashtextextended(p, 7)  $$ LANGUAGE sql IMMUTABLE;  -- LN_CAPACITY=7

-- device/appliance admission: device-slot lock BEFORE capacity lock; reconnect replaces same device; enforce max
CREATE FUNCTION iam_v2.reserve_device_slot(p_ent uuid, p_dev uuid, p_cred text, p_appliance text, p_max int)
RETURNS text AS $$
DECLARE t uuid; s uuid; cnt int;
BEGIN
  SELECT tenant_id, site_id INTO t, s FROM iam_v2.entitlements WHERE id=p_ent;
  PERFORM pg_advisory_xact_lock(iam_v2.ns_device_slot(p_cred));   -- 1) device-slot namespace (11), before capacity
  PERFORM pg_advisory_xact_lock(iam_v2.ns_capacity(p_appliance)); -- 2) capacity namespace (7)
  -- reconnect: same device re-authorizes without burning a new slot
  IF EXISTS (SELECT 1 FROM iam_v2.entitlement_devices WHERE entitlement_id=p_ent AND device_id=p_dev) THEN
    UPDATE iam_v2.entitlement_devices SET status='AUTHORIZED', last_authorized=now()
      WHERE entitlement_id=p_ent AND device_id=p_dev;
    RETURN 'RECONNECT';
  END IF;
  SELECT count(*) INTO cnt FROM iam_v2.entitlement_devices WHERE entitlement_id=p_ent AND status='AUTHORIZED';
  IF cnt >= p_max THEN RETURN 'MAX_DEVICES_REACHED'; END IF;
  INSERT INTO iam_v2.entitlement_devices(tenant_id,site_id,entitlement_id,device_id,status,first_authorized,last_authorized)
    VALUES (t,s,p_ent,p_dev,'AUTHORIZED',now(),now());
  RETURN 'AUTHORIZED';
END; $$ LANGUAGE plpgsql;

-- ---- idempotent accounting-sample ingestion (watermark model) ----
CREATE FUNCTION iam_v2.ingest_sample(p_session uuid, p_seq bigint, p_up bigint, p_down bigint, p_epoch int DEFAULT 1)
RETURNS text AS $$
DECLARE w record; d bigint; t uuid; s uuid;
BEGIN
  SELECT tenant_id, site_id INTO t, s FROM iam_v2.sessions WHERE id=p_session;
  BEGIN
    INSERT INTO iam_v2.accounting_records(tenant_id,site_id,session_id,sample_seq,bytes_up,bytes_down)
      VALUES (t,s,p_session,p_seq,p_up,p_down);
  EXCEPTION WHEN unique_violation THEN RETURN 'DUPLICATE'; END;
  SELECT * INTO w FROM iam_v2.session_counter_watermarks WHERE session_id=p_session FOR UPDATE;
  IF NOT FOUND THEN
    INSERT INTO iam_v2.session_counter_watermarks(tenant_id,site_id,session_id,source_epoch,last_up,last_down,sample_seq,updated_at)
      VALUES (t,s,p_session,p_epoch,0,0,0,now());
    SELECT * INTO w FROM iam_v2.session_counter_watermarks WHERE session_id=p_session FOR UPDATE;
  END IF;
  IF p_epoch <> w.source_epoch THEN                 -- counter-reset epoch: treat sample as fresh delta from 0
    d := p_up + p_down;
    UPDATE iam_v2.session_counter_watermarks SET source_epoch=p_epoch, last_up=p_up, last_down=p_down,
      sample_seq=greatest(sample_seq,p_seq), updated_at=now() WHERE session_id=p_session;
  ELSIF p_seq <= w.sample_seq THEN
    RETURN 'STALE';                                 -- out-of-order / already-applied: ledgered, no double count
  ELSE
    d := greatest(p_up - w.last_up,0) + greatest(p_down - w.last_down,0);
    UPDATE iam_v2.session_counter_watermarks SET last_up=p_up, last_down=p_down, sample_seq=p_seq, updated_at=now()
      WHERE session_id=p_session;
  END IF;
  UPDATE iam_v2.entitlements e SET consumed_data_bytes = consumed_data_bytes + d, usage_version = usage_version+1
    FROM iam_v2.sessions ss WHERE ss.id=p_session AND e.id=ss.entitlement_id;
  RETURN 'APPLIED';
END; $$ LANGUAGE plpgsql;

-- idempotent session close (charges usage exactly once via watermark; second close is a no-op)
CREATE FUNCTION iam_v2.close_session(p_session uuid, p_reason text DEFAULT 'logout')
RETURNS text AS $$
DECLARE cur text;
BEGIN
  SELECT state INTO cur FROM iam_v2.sessions WHERE id=p_session FOR UPDATE;
  IF cur = 'ended' THEN RETURN 'ALREADY_ENDED'; END IF;
  UPDATE iam_v2.sessions SET state='ended', ended=now(), end_reason=p_reason WHERE id=p_session;
  RETURN 'ENDED';
END; $$ LANGUAGE plpgsql;

COMMIT;
