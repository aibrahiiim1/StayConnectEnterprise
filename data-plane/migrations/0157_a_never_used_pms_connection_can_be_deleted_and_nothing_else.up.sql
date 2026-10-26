-- 0157 — A NEVER-USED PMS CONNECTION CAN BE DELETED, AND NOTHING ELSE CAN.
--
-- A PMS connection that was only ever configured (never switched on, never connected to its PMS, no guest, stay, posting, routing or answer attached) is a shell, and the
-- administrator should be able to remove it. Everything about a connection that was ever used is history and stays: revisions and secret generations are immutable, and
-- stays, events, postings and entitlements reference the connection by key.
--
-- iam_v2.delete_unused_pms_interface is the only path that removes a connection. It refuses unless the connection is switched off and has never attempted a connection, then
-- removes the connection's own configuration rows (draft revisions, their sealed secrets, runtime, settings) and the connection. Anything that still references the connection
-- (a stay, an event, a posting, a routing map, a package mapping, an answer, a conflict) makes the delete fail on its foreign key and the whole call rolls back, so a used
-- connection can never lose a row. The immutability guards on revisions and secrets are relaxed for exactly one case: a delete made inside this function for this connection.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.trg_reject_update_delete_unless_unused_delete()
  RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF TG_OP = 'DELETE' AND current_setting('iam_v2.unused_interface_delete', true) = OLD.pms_interface_id::text THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION '% is immutable (no UPDATE/DELETE) on %', TG_TABLE_NAME, TG_OP;
END; $fn$;

CREATE OR REPLACE FUNCTION iam_v2.trg_secret_gen_guard_unless_unused_delete()
  RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF current_setting('iam_v2.unused_interface_delete', true) = OLD.pms_interface_id::text THEN RETURN OLD; END IF;
    RAISE EXCEPTION 'secret generations are not deletable';
  END IF;
  IF ROW(NEW.ciphertext,NEW.nonce,NEW.encryption_key_id,NEW.cipher_version,NEW.generation_no,NEW.pms_interface_id)
     IS DISTINCT FROM ROW(OLD.ciphertext,OLD.nonce,OLD.encryption_key_id,OLD.cipher_version,OLD.generation_no,OLD.pms_interface_id)
  THEN RAISE EXCEPTION 'secret generation identity is immutable (only superseded_at may change)'; END IF;
  RETURN NEW;
END; $fn$;

DROP TRIGGER IF EXISTS imm_pms_rev ON iam_v2.pms_interface_revisions;
CREATE TRIGGER imm_pms_rev BEFORE DELETE OR UPDATE ON iam_v2.pms_interface_revisions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete_unless_unused_delete();

DROP TRIGGER IF EXISTS sg_guard ON iam_v2.pms_interface_secret_generations;
CREATE TRIGGER sg_guard BEFORE DELETE OR UPDATE ON iam_v2.pms_interface_secret_generations
  FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_secret_gen_guard_unless_unused_delete();

CREATE OR REPLACE FUNCTION iam_v2.delete_unused_pms_interface(p_tenant uuid, p_site uuid, p_interface uuid)
  RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_state text; v_attempted boolean; v_tbl text; v_has boolean;
BEGIN
  SELECT lifecycle_state INTO v_state FROM iam_v2.pms_interfaces
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_interface FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'PMS_INTERFACE_NOT_FOUND' USING ERRCODE = 'no_data_found';
  END IF;
  IF v_state <> 'AUTH_DISABLED' THEN
    RAISE EXCEPTION 'PMS_INTERFACE_IN_USE: the connection is % and can only be retired', v_state USING ERRCODE = 'check_violation';
  END IF;
  SELECT EXISTS (SELECT 1 FROM iam_v2.pms_interface_runtime
                  WHERE tenant_id = p_tenant AND site_id = p_site AND pms_interface_id = p_interface
                    AND (last_connect_attempt_at IS NOT NULL OR last_connected_at IS NOT NULL OR last_complete_sync_at IS NOT NULL OR last_event_cursor IS NOT NULL))
    INTO v_attempted;
  IF v_attempted THEN
    RAISE EXCEPTION 'PMS_INTERFACE_IN_USE: the connection has connected to its PMS and can only be retired' USING ERRCODE = 'check_violation';
  END IF;

  -- Whatever else carries this connection's id (stays, events, postings, answers, routing, packages, coverage, rooms, sign-in records, tomorrow's tables) is history or
  -- configuration in use: the connection is not deletable while any of it exists. The tables removed below are the connection's own configuration.
  FOR v_tbl IN
    SELECT c.relname FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'iam_v2'
      JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = 'pms_interface_id' AND NOT a.attisdropped
     WHERE c.relkind IN ('r','p')
       AND c.relname NOT IN ('pms_interface_runtime','pms_interface_secret_generations','pms_interface_revisions','pms_interface_pnumber_seq',
                             'pms_connection_settings','pms_connection_settings_changes','pms_interface_financial_settings','pms_interface_financial_setting_changes')
  LOOP
    EXECUTE format('SELECT EXISTS (SELECT 1 FROM iam_v2.%I WHERE pms_interface_id = $1)', v_tbl) INTO v_has USING p_interface;
    IF v_has THEN
      RAISE EXCEPTION 'PMS_INTERFACE_IN_USE: records in % still refer to the connection', v_tbl USING ERRCODE = 'check_violation';
    END IF;
  END LOOP;
  IF EXISTS (SELECT 1 FROM iam_v2.pms_source_conflicts WHERE interface_a = p_interface OR interface_b = p_interface) THEN
    RAISE EXCEPTION 'PMS_INTERFACE_IN_USE: a source conflict still refers to the connection' USING ERRCODE = 'check_violation';
  END IF;

  PERFORM set_config('iam_v2.unused_interface_delete', p_interface::text, true);
  UPDATE iam_v2.pms_interfaces SET current_revision_id = NULL
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_interface AND current_revision_id IS NOT NULL;
  DELETE FROM iam_v2.pms_interface_runtime        WHERE tenant_id = p_tenant AND site_id = p_site AND pms_interface_id = p_interface;
  DELETE FROM iam_v2.pms_interface_secret_generations WHERE tenant_id = p_tenant AND site_id = p_site AND pms_interface_id = p_interface;
  DELETE FROM iam_v2.pms_interface_revisions      WHERE tenant_id = p_tenant AND site_id = p_site AND pms_interface_id = p_interface;
  DELETE FROM iam_v2.pms_interface_pnumber_seq    WHERE tenant_id = p_tenant AND site_id = p_site AND pms_interface_id = p_interface;
  DELETE FROM iam_v2.pms_connection_settings      WHERE pms_interface_id = p_interface;
  DELETE FROM iam_v2.pms_interfaces               WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_interface;
END;
$fn$;

REVOKE ALL ON FUNCTION iam_v2.delete_unused_pms_interface(uuid, uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.delete_unused_pms_interface(uuid, uuid, uuid) TO svc_edged;

COMMIT;
