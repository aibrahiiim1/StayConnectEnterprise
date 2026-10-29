-- AD-43 (audit P6 L1): a capture that happens after a card checkout was marked failed or unknown was never noticed.
--
-- A slow, READ-ONLY re-query of the provider (Go side, behind an off-by-default execution switch) asks about FAILED / UNKNOWN charges
-- of the last N days. When the provider reports a capture, OneGate does NOT change the charge, the settlement or the purchase and
-- grants nothing: it opens a LATE_CAPTURE review case for finance, who decide outside (refund at the provider, or grant by hand).
--
--   * card_reversal_reviews gains the kind LATE_CAPTURE (the case, and its resolution, are the existing ones).
--   * payment_checkouts.late_checked_at spaces the re-queries (a charge is asked about at most every 6 hours).
--   * p4_late_capture_candidates picks what to ask about; p4_mark_late_checked records that it was asked;
--     p4_open_late_capture_review opens the case, once, and changes nothing else.
--   * N is the setting site_card_recheck_settings.window_days (default 7, 1..30, per site, versioned, audited), CLAUDE.md section 0C.
--
-- Additive and idempotent. The re-query itself cannot run until the deployer switches STAYCONNECT_PHASE4_PAYMENT_RECHECK on (and the
-- provider is deployed); nothing here calls a provider.

ALTER TABLE iam_v2.payment_checkouts ADD COLUMN IF NOT EXISTS late_checked_at timestamptz;

ALTER TABLE iam_v2.card_reversal_reviews DROP CONSTRAINT IF EXISTS card_reversal_reviews_kind_check;
ALTER TABLE iam_v2.card_reversal_reviews ADD CONSTRAINT card_reversal_reviews_kind_check
    CHECK (kind IN ('DISPUTE','CHARGEBACK','REVERSAL_REFUSED','LATE_CAPTURE'));
DO $$ BEGIN
    ALTER TABLE iam_v2.card_reversal_reviews ADD CONSTRAINT card_reversal_reviews_late_has_detail
        CHECK (kind <> 'LATE_CAPTURE' OR (detail IS NOT NULL AND length(detail) <= 500));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE TABLE IF NOT EXISTS iam_v2.site_card_recheck_settings (
    tenant_id      uuid NOT NULL,
    site_id        uuid NOT NULL,
    window_days    integer NOT NULL DEFAULT 7,
    config_version bigint  NOT NULL DEFAULT 1,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, site_id),
    CONSTRAINT scrs_window_bounded CHECK (window_days BETWEEN 1 AND 30),
    CONSTRAINT scrs_version_positive CHECK (config_version >= 1)
);
CREATE TABLE IF NOT EXISTS iam_v2.site_card_recheck_setting_changes (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid NOT NULL,
    site_id            uuid NOT NULL,
    changed_at         timestamptz NOT NULL DEFAULT now(),
    changed_by         text NOT NULL CHECK (length(btrim(changed_by)) > 0),
    reason             text NOT NULL CHECK (length(btrim(reason)) BETWEEN 3 AND 500),
    old_window_days    integer,
    new_window_days    integer NOT NULL,
    new_config_version bigint NOT NULL
);
DROP TRIGGER IF EXISTS scrsc_append_only ON iam_v2.site_card_recheck_setting_changes;
CREATE TRIGGER scrsc_append_only BEFORE UPDATE OR DELETE ON iam_v2.site_card_recheck_setting_changes
    FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_append_only_refuse();

CREATE OR REPLACE FUNCTION iam_v2.card_recheck_settings_get(p_tenant uuid, p_site uuid)
RETURNS TABLE (window_days integer, config_version bigint, is_default boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
    SELECT COALESCE(s.window_days, 7), COALESCE(s.config_version, 0), (s.tenant_id IS NULL)
      FROM (SELECT 1) one LEFT JOIN iam_v2.site_card_recheck_settings s ON s.tenant_id = p_tenant AND s.site_id = p_site;
$fn$;

CREATE OR REPLACE FUNCTION iam_v2.card_recheck_settings_set(
    p_tenant uuid, p_site uuid, p_expected_version bigint, p_window_days integer, p_operator text, p_reason text)
RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_cur bigint; v_old integer; v_ver bigint;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'RECHECK_SETTING_ACTOR: an operator identity is required' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_reason IS NULL OR length(btrim(p_reason)) < 3 OR length(p_reason) > 500 THEN
        RAISE EXCEPTION 'RECHECK_SETTING_REASON: a reason of 3 to 500 characters is required' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_window_days IS NULL OR p_window_days < 1 OR p_window_days > 30 THEN
        RAISE EXCEPTION 'RECHECK_SETTING_BOUNDS: the window must be between 1 and 30 days' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('card_recheck_settings'), hashtext(p_site::text));
    SELECT s.config_version, s.window_days INTO v_cur, v_old
      FROM iam_v2.site_card_recheck_settings s WHERE s.tenant_id = p_tenant AND s.site_id = p_site FOR UPDATE;
    IF p_expected_version IS NULL OR COALESCE(v_cur, 0) <> p_expected_version THEN
        RAISE EXCEPTION 'RECHECK_SETTING_VERSION_CONFLICT: the setting changed (now version %); reload and try again', COALESCE(v_cur, 0)
            USING ERRCODE = 'serialization_failure';
    END IF;
    INSERT INTO iam_v2.site_card_recheck_settings AS s (tenant_id, site_id, window_days)
    VALUES (p_tenant, p_site, p_window_days)
    ON CONFLICT (tenant_id, site_id) DO UPDATE
       SET window_days = EXCLUDED.window_days, config_version = s.config_version + 1, updated_at = now()
    RETURNING s.config_version INTO v_ver;
    INSERT INTO iam_v2.site_card_recheck_setting_changes
           (tenant_id, site_id, changed_by, reason, old_window_days, new_window_days, new_config_version)
    VALUES (p_tenant, p_site, btrim(p_operator), btrim(p_reason), v_old, p_window_days, v_ver);
    RETURN v_ver;
END $fn$;

-- What to ask the provider about: a FAILED or UNKNOWN charge whose checkout began within the window, not asked in the last 6 hours,
-- and with no late-capture case yet.
CREATE OR REPLACE FUNCTION iam_v2.p4_late_capture_candidates(p_tenant uuid, p_site uuid, p_days integer, p_limit integer)
RETURNS TABLE (transaction_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
    SELECT pt.id
      FROM iam_v2.payment_transactions pt
      JOIN iam_v2.payment_checkouts pc ON pc.transaction_id = pt.id
     WHERE pt.tenant_id = p_tenant AND pt.site_id = p_site AND pt.transaction_type = 'CHARGE'
       AND pt.status IN ('FAILED', 'UNKNOWN')
       AND pc.created_at > now() - make_interval(days => LEAST(GREATEST(COALESCE(p_days, 7), 1), 30))
       AND (pc.late_checked_at IS NULL OR pc.late_checked_at < now() - interval '6 hours')
       AND NOT EXISTS (SELECT 1 FROM iam_v2.card_reversal_reviews r
                        WHERE r.charge_transaction_id = pt.id AND r.kind = 'LATE_CAPTURE')
     ORDER BY pc.late_checked_at NULLS FIRST, pc.created_at
     LIMIT LEAST(GREATEST(COALESCE(p_limit, 25), 1), 200);
$fn$;

CREATE OR REPLACE FUNCTION iam_v2.p4_mark_late_checked(p_tenant uuid, p_site uuid, p_charge uuid) RETURNS void
LANGUAGE sql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
    UPDATE iam_v2.payment_checkouts SET late_checked_at = now()
     WHERE transaction_id = p_charge AND tenant_id = p_tenant AND site_id = p_site;
$fn$;

-- The only thing a late capture does: open a case. The charge, the settlement and the purchase stay as they were; nothing is granted.
CREATE OR REPLACE FUNCTION iam_v2.p4_open_late_capture_review(
    p_tenant uuid, p_site uuid, p_charge uuid, p_provider_ref text, p_amount_minor bigint, p_currency text)
RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE ch record; n integer;
BEGIN
    IF p_provider_ref IS NULL OR length(p_provider_ref) NOT BETWEEN 1 AND 200 THEN
        RAISE EXCEPTION 'a late capture needs the provider reference' USING ERRCODE = 'check_violation';
    END IF;
    SELECT * INTO ch FROM iam_v2.payment_transactions
     WHERE id = p_charge AND tenant_id = p_tenant AND site_id = p_site AND transaction_type = 'CHARGE'
       AND status IN ('FAILED', 'UNKNOWN');
    IF NOT FOUND THEN
        RETURN 'NOT_A_FAILED_CHARGE';
    END IF;
    INSERT INTO iam_v2.card_reversal_reviews
           (tenant_id, site_id, settlement_id, charge_transaction_id, kind, provider_ref, detail)
    VALUES (p_tenant, p_site, ch.settlement_id, p_charge, 'LATE_CAPTURE', left(p_provider_ref, 200),
            left(format('The provider reports a capture of %s %s for a card checkout OneGate recorded as %s. Nothing was granted and nothing was changed. Refund it at the provider, or grant access by hand, then resolve this case.',
                        COALESCE(p_amount_minor::text, '?'), COALESCE(left(p_currency, 3), '?'), lower(ch.status)), 500))
    ON CONFLICT (charge_transaction_id, kind, provider_ref) DO NOTHING;
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN CASE WHEN n = 1 THEN 'OPENED' ELSE 'ALREADY_OPEN' END;
END $fn$;

REVOKE ALL ON FUNCTION iam_v2.card_recheck_settings_get(uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.card_recheck_settings_set(uuid,uuid,bigint,integer,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_late_capture_candidates(uuid,uuid,integer,integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_mark_late_checked(uuid,uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_open_late_capture_review(uuid,uuid,uuid,text,bigint,text) FROM PUBLIC;

-- Staff text and who acted (migration 0147): the setting's change log is registered like every other.
INSERT INTO iam_v2.scrub_registry (table_name, column_name, kind, ts_column, handled, jsonb_key) VALUES
  ('iam_v2.site_card_recheck_setting_changes', 'reason',     'note',  'changed_at', false, NULL),
  ('iam_v2.site_card_recheck_setting_changes', 'changed_by', 'label', NULL,         false, NULL)
ON CONFLICT (table_name, column_name) DO NOTHING;

DO $inst$
DECLARE t text; trg record; def text;
BEGIN
  FOREACH t IN ARRAY ARRAY['iam_v2.site_card_recheck_setting_changes'] LOOP
    FOR trg IN SELECT g.oid, g.tgname FROM pg_trigger g
                WHERE g.tgrelid = to_regclass(t) AND NOT g.tgisinternal AND g.tgconstraint = 0
                  AND (g.tgtype & 2) = 2 AND (g.tgtype & 24) <> 0 AND g.tgname <> 'zz_scrub_guard' LOOP
      def := pg_get_triggerdef(trg.oid);
      IF def LIKE '%iam_v2.scrub%' THEN CONTINUE; END IF;
      EXECUTE format('DROP TRIGGER %I ON %s', trg.tgname, t);
      EXECUTE replace(def, ' EXECUTE FUNCTION', ' WHEN (current_setting(''iam_v2.scrub'', true) IS DISTINCT FROM ''on'') EXECUTE FUNCTION');
    END LOOP;
    EXECUTE format('DROP TRIGGER IF EXISTS zz_scrub_guard ON %s', t);
    EXECUTE format('CREATE TRIGGER zz_scrub_guard BEFORE UPDATE OR DELETE ON %s FOR EACH ROW WHEN (current_setting(''iam_v2.scrub'', true) = ''on'') EXECUTE FUNCTION iam_v2.scrub_guard()', t);
  END LOOP;
END $inst$;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.site_card_recheck_settings OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.site_card_recheck_setting_changes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.card_recheck_settings_get(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.card_recheck_settings_set(uuid,uuid,bigint,integer,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_late_capture_candidates(uuid,uuid,integer,integer) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_mark_late_checked(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_open_late_capture_review(uuid,uuid,uuid,text,bigint,text) OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- Group-role grants (sc_payment_*) are made here as migration 0096 and 0115 do; the edged login's are mirrored in deploy/gatep.
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sc_payment_runtime') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.card_recheck_settings_get(uuid,uuid) TO sc_payment_runtime;
    GRANT EXECUTE ON FUNCTION iam_v2.p4_late_capture_candidates(uuid,uuid,integer,integer) TO sc_payment_runtime;
    GRANT EXECUTE ON FUNCTION iam_v2.p4_mark_late_checked(uuid,uuid,uuid) TO sc_payment_runtime;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sc_payment_outcome') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p4_open_late_capture_review(uuid,uuid,uuid,text,bigint,text) TO sc_payment_outcome;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.card_recheck_settings_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.card_recheck_settings_set(uuid,uuid,bigint,integer,text,text) TO svc_edged;
  END IF;
END $g$;
