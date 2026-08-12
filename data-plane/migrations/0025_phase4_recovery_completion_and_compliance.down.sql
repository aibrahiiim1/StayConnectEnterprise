-- Reverse 0025. The 0023 reconciliation body is restored verbatim: the version that had no MARKER_BEHIND
-- branch. The compliance columns and the archive functions go with it; existing archive ROWS are kept,
-- because deleting a record that something was archived would be worse than leaving it.
BEGIN;
REVOKE EXECUTE ON FUNCTION
  iam_v2.p4_authorize_zero_attempt_retry(uuid,uuid,text,jsonb) FROM sc_financial_operator;
DROP FUNCTION IF EXISTS iam_v2.p4_authorize_zero_attempt_retry(uuid,uuid,text,jsonb);
DROP FUNCTION IF EXISTS iam_v2.p4_assert_compliance_archived(uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_record_compliance_archive(uuid,uuid,text,text,jsonb);
DROP INDEX IF EXISTS iam_v2.ppa_merchant_ref_globally_unique;
ALTER TABLE iam_v2.compliance_archives
  DROP COLUMN IF EXISTS receipt_blocked_reason,
  DROP COLUMN IF EXISTS row_counts,
  DROP COLUMN IF EXISTS artifact_path,
  DROP COLUMN IF EXISTS purpose;

CREATE OR REPLACE FUNCTION iam_v2.p4_reconcile_financial_epoch_v2(
  p_tenant uuid, p_site uuid, p_system_identity text, p_marker_generation bigint,
  p_marker_present boolean)
RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE cur record; v_epoch bigint; v_reason text := NULL; v_detect text;
BEGIN
  IF p_system_identity IS NULL OR btrim(p_system_identity) = '' THEN
    RAISE EXCEPTION 'RECOVERY_IDENTITY_REQUIRED' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('p4.epoch:' || p_tenant::text || ':' || p_site::text));
  SELECT * INTO cur FROM iam_v2.financial_epochs
   WHERE tenant_id = p_tenant AND site_id = p_site ORDER BY epoch DESC LIMIT 1;

  IF cur.epoch IS NULL THEN
    INSERT INTO iam_v2.financial_epochs
      (tenant_id, site_id, epoch, system_identity, reason, released_at, restore_generation)
    VALUES (p_tenant, p_site, 1, p_system_identity, 'INITIAL', now(),
            CASE WHEN p_marker_present THEN p_marker_generation ELSE 0 END);
    RETURN 'INITIALIZED';
  END IF;

  -- SIGNAL 1: the management marker is ahead of the database. This is the supported-restore case, and the
  -- one system_identifier cannot see, because pg_restore into the same cluster changes nothing it reads.
  IF p_marker_present AND p_marker_generation > cur.restore_generation THEN
    v_reason := 'MARKER_AHEAD'; v_detect := 'MANAGEMENT_MARKER';
  -- SIGNAL 2: the cluster is not the one that wrote this row. Kept because it catches what the marker
  -- cannot: a dump restored into a fresh cluster, a promoted replica, a cloned appliance.
  ELSIF cur.system_identity <> p_system_identity THEN
    v_reason := 'IDENTITY_CHANGED'; v_detect := 'SYSTEM_IDENTITY';
  -- SIGNAL 3: the marker is GONE or BEHIND. A missing marker on a database that has one recorded means the
  -- management partition was replaced or the data was moved onto an appliance that never had it. Fail
  -- closed: this is the unsupported-raw-snapshot path, and "we cannot tell" is a reason to hold.
  ELSIF cur.restore_generation > 0 AND NOT p_marker_present THEN
    v_reason := 'MARKER_MISSING'; v_detect := 'MANAGEMENT_MARKER';
  END IF;

  IF v_reason IS NULL THEN
    RETURN CASE WHEN cur.released_at IS NULL THEN 'RECOVERY_ACTIVE' ELSE 'UNCHANGED' END;
  END IF;

  IF cur.released_at IS NULL THEN
    UPDATE iam_v2.financial_epochs
       SET system_identity = p_system_identity,
           restore_generation = greatest(cur.restore_generation,
                                         CASE WHEN p_marker_present THEN p_marker_generation ELSE 0 END)
     WHERE tenant_id = p_tenant AND site_id = p_site AND epoch = cur.epoch;
    PERFORM iam_v2.p4_hold_financial_rails(p_tenant, p_site, cur.epoch);
    RETURN 'RECOVERY_ACTIVE';
  END IF;

  v_epoch := cur.epoch + 1;
  INSERT INTO iam_v2.financial_epochs
    (tenant_id, site_id, epoch, system_identity, reason, restore_generation)
  VALUES (p_tenant, p_site, v_epoch, p_system_identity, 'RESTORE_DETECTED',
          greatest(cur.restore_generation,
                   CASE WHEN p_marker_present THEN p_marker_generation ELSE 0 END));

  -- A detected restore that no manifest explains is recorded as exactly that. The operator surface shows
  -- the difference, because "we restored this deliberately from a verified backup" and "this data is
  -- older than it should be and nobody knows why" call for different reconciliation.
  INSERT INTO iam_v2.financial_restore_events
    (tenant_id, site_id, restore_generation, manifest_sha256, restore_kind, detected_by, restored_by)
  VALUES (p_tenant, p_site,
          greatest(cur.restore_generation, CASE WHEN p_marker_present THEN p_marker_generation ELSE 0 END),
          repeat('0', 64), 'UNSUPPORTED_RAW_SNAPSHOT', v_detect, v_reason)
  ON CONFLICT DO NOTHING;

  PERFORM iam_v2.p4_hold_financial_rails(p_tenant, p_site, v_epoch);
  RETURN 'RECOVERY_ENTERED';
END $fn$;
REVOKE EXECUTE ON FUNCTION
  iam_v2.p4_reconcile_financial_epoch_v2(uuid,uuid,text,bigint,boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION
  iam_v2.p4_reconcile_financial_epoch_v2(uuid,uuid,text,bigint,boolean) TO sc_payment_runtime;
DELETE FROM public.schema_migrations WHERE version = '0025_phase4_recovery_completion_and_compliance';
COMMIT;
