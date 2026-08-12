-- 0023 — PHASE 4: the supported restore-generation model. D18 / T0029. Receipt: T0039. Additive, DARK.
--
-- MEASURED FIRST, against the repository's ACTUAL restore workflow (docs/BACKUP_AND_RESTORE.md §1):
--
--     pg_restore -U stayconnect_site -d stayconnect_site /var/backups/.../site-<stamp>.dump
--
-- The dump is restored INTO THE EXISTING DATABASE of the EXISTING CLUSTER. So pg_control's
-- system_identifier -- which 0019 used as the sole restore detector -- DOES NOT CHANGE. The supported
-- restore is precisely the case 0019 could not see.
--
-- That is a real defect and this migration corrects it. system_identifier stays as one defensive signal,
-- because it does catch the cases it was chosen for -- a dump restored into a NEW cluster, a promoted
-- replica, a cloned VM -- but it is no longer the thing the model rests on.
--
-- THE MODEL. Two facts must be compared, and the second must live somewhere pg_restore cannot rewrite:
--
--   restore_generation        stored IN the database, and therefore restored along with everything else.
--                             A restored copy carries the generation that was current when the BACKUP was
--                             taken.
--   the management marker     a file on the appliance's management partition (/etc/stayconnect), outside
--                             the database, written by the supported restore tool. pg_dump does not read
--                             it and pg_restore does not write it, so it keeps counting forward across a
--                             restore.
--
-- If the marker's generation is AHEAD of the database's, this data is older than the appliance knows it
-- should be: it has been rolled back. That is the detection.
--
-- WHAT THIS IS NOT PROTECTED BY. There is no TPM, no monotonic counter and no secure element in this
-- deployment profile, and none is claimed. The marker is an ordinary root-owned file: an attacker with
-- root on the appliance can rewrite it, and so can a careless administrator. It is a defence against
-- ACCIDENT and against the ordinary operational restore, not against a privileged adversary. Recorded here
-- as a limitation rather than left for a reader to assume otherwise.
BEGIN;

ALTER TABLE iam_v2.financial_epochs
  ADD COLUMN IF NOT EXISTS restore_generation bigint NOT NULL DEFAULT 0;

COMMENT ON COLUMN iam_v2.financial_epochs.restore_generation IS
  'The restore generation this row was written under. It travels WITH the database through a pg_restore, '
  'which is what makes comparing it against the management-partition marker a rollback detector.';

-- The supported-restore ledger. One row per restore the tool performed, so an operator can answer "when
-- was this data restored, from what, and who ran it" without reading a shell history.
CREATE TABLE IF NOT EXISTS iam_v2.financial_restore_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  restore_generation bigint NOT NULL,
  -- The manifest's digest, not its contents: the manifest names a backup artefact and is verified by the
  -- tool before any restore happens. Storing the digest lets a later reader confirm WHICH manifest was
  -- honoured without the financial record carrying a file path or an operator's shell environment.
  manifest_sha256 text NOT NULL CHECK (manifest_sha256 ~ '^[0-9a-f]{64}$'),
  backup_taken_at timestamptz,
  restored_at timestamptz NOT NULL DEFAULT now(),
  restored_by text,
  -- SUPPORTED: the manifest verified and the tool drove the restore.
  -- UNSUPPORTED_RAW_SNAPSHOT: a restore was detected that no manifest explains -- a VM snapshot rollback,
  -- a hand-run pg_restore, a promoted replica. It is recorded, held and reconciled exactly like a
  -- supported one; what differs is that nobody can say what it contains.
  restore_kind text NOT NULL CHECK (restore_kind IN ('SUPPORTED','UNSUPPORTED_RAW_SNAPSHOT')),
  detected_by text NOT NULL CHECK (detected_by IN ('MANAGEMENT_MARKER','SYSTEM_IDENTITY','RESTORE_TOOL')),
  UNIQUE (tenant_id, site_id, restore_generation, restore_kind)
);
CREATE INDEX IF NOT EXISTS fin_restore_events_site
  ON iam_v2.financial_restore_events (tenant_id, site_id, restored_at DESC);

-- The supported restore tool calls this AFTER a verified restore, to stamp the database with the
-- generation the marker has already been advanced to. It enters recovery deliberately: a supported restore
-- is still a restore, and the work that was in flight when the backup was taken is still unaccounted for.
CREATE OR REPLACE FUNCTION iam_v2.p4_record_supported_restore(
  p_tenant uuid, p_site uuid, p_generation bigint, p_manifest_sha text,
  p_backup_taken_at timestamptz, p_restored_by text)
RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE cur record; v_epoch bigint;
BEGIN
  IF p_manifest_sha IS NULL OR p_manifest_sha !~ '^[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'RESTORE_MANIFEST_REQUIRED: a supported restore is identified by its verified manifest '
                    'digest' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('p4.epoch:' || p_tenant::text || ':' || p_site::text));
  SELECT * INTO cur FROM iam_v2.financial_epochs
   WHERE tenant_id = p_tenant AND site_id = p_site ORDER BY epoch DESC LIMIT 1;
  IF cur.epoch IS NOT NULL AND p_generation <= cur.restore_generation THEN
    RAISE EXCEPTION 'RESTORE_GENERATION_NOT_ADVANCED: the database already records generation %; a restore '
                    'must advance it', cur.restore_generation USING ERRCODE = 'check_violation';
  END IF;

  v_epoch := coalesce(cur.epoch, 0) + 1;
  IF cur.epoch IS NOT NULL AND cur.released_at IS NULL THEN
    v_epoch := cur.epoch;  -- already held; a restore during recovery does not open a second epoch
    UPDATE iam_v2.financial_epochs SET restore_generation = p_generation
     WHERE tenant_id = p_tenant AND site_id = p_site AND epoch = v_epoch;
  ELSE
    INSERT INTO iam_v2.financial_epochs
      (tenant_id, site_id, epoch, system_identity, reason, restore_generation)
    VALUES (p_tenant, p_site, v_epoch, coalesce(cur.system_identity, 'restored'),
            'RESTORE_DETECTED', p_generation);
  END IF;

  INSERT INTO iam_v2.financial_restore_events
    (tenant_id, site_id, restore_generation, manifest_sha256, backup_taken_at, restored_by,
     restore_kind, detected_by)
  VALUES (p_tenant, p_site, p_generation, p_manifest_sha, p_backup_taken_at, p_restored_by,
          'SUPPORTED', 'RESTORE_TOOL')
  ON CONFLICT DO NOTHING;

  PERFORM iam_v2.p4_hold_financial_rails(p_tenant, p_site, v_epoch);
  RETURN v_epoch;
END $fn$;
REVOKE EXECUTE ON FUNCTION
  iam_v2.p4_record_supported_restore(uuid,uuid,bigint,text,timestamptz,text) FROM PUBLIC;

-- ============================================================================
-- Startup reconciliation, driven by the marker rather than by a caller's claim
-- ============================================================================
-- p_marker_generation comes from the management partition, read by the process at startup. It is NOT a
-- financial authority a caller may assert: the function only ever compares it, and the only thing it can
-- cause is MORE holding. A caller that lies with a HIGHER number puts its own site into recovery; a caller
-- that lies with a lower one is ignored. Neither can release anything or unhold anything, which is the
-- property that makes accepting the parameter safe.
--
-- p_system_identity remains a second, independent signal for the cases the marker cannot see.
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

-- The current generation, for the restore tool to advance from and for the operator surface to display.
CREATE OR REPLACE FUNCTION iam_v2.p4_current_restore_generation(p_tenant uuid, p_site uuid)
RETURNS bigint
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT coalesce(max(restore_generation), 0) FROM iam_v2.financial_epochs
   WHERE tenant_id = p_tenant AND site_id = p_site;
$fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_current_restore_generation(uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p4_current_restore_generation(uuid,uuid)
  TO sc_payment_runtime, sc_financial_operator;
GRANT SELECT ON iam_v2.financial_restore_events TO sc_financial_operator;

INSERT INTO public.schema_migrations (version) VALUES ('0023_phase4_restore_generation')
  ON CONFLICT DO NOTHING;
COMMIT;
