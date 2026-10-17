-- 0129 -- BACKUPS ARE ENCRYPTED TO THE HOTEL'S OWN KEYS
-- (GDPR remediation plan C5; Product-Owner decision D-C5 as revised 2026-10-17: hotel-held keys, several
--  holders, a restore-test reminder, no escrow; docs/privacy/GDPR_REMEDIATION_PLAN.md section 4).
--
-- Until now every backup -- the nightly database dump, the nightly copy of /etc/stayconnect with the seal keys,
-- and the on-demand dumps on the Backups screen -- was written in the clear. This migration adds the register
-- of the hotel's backup KEY HOLDERS. Each holder is an age public key; the private key was handed to the hotel
-- once, when the holder was added, and the appliance never had a copy. Every backup is encrypted to every
-- ACTIVE holder, so any one holder can restore alone, and nobody else -- the appliance and its supplier
-- included -- can read it.
--
--   PENDING  the key was generated and handed over; nothing is encrypted to it yet.
--   ACTIVE   the holder typed back the check code from the key file or the recovery sheet, proving it was
--            saved. Encryption switches on with the first ACTIVE holder.
--   REMOVED  no new backup is encrypted to it. Older backups still open with it.
--
-- The last ACTIVE holder cannot be removed: once on, encryption stays on. A holder is never deleted and never
-- returns to ACTIVE; every change is in the append-only event log.
--
-- The restore-test interval (default 6 months) is a backup setting beside the retention settings (scd,
-- /etc/stayconnect/backup-retention.conf), which is where the Backups screen's settings already live.

BEGIN;

CREATE TABLE IF NOT EXISTS public.backup_key_holders (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL,
  site_id           uuid NOT NULL,
  label             text NOT NULL CONSTRAINT bkh_label CHECK (length(btrim(label)) BETWEEN 1 AND 80),
  public_key        text NOT NULL CONSTRAINT bkh_public_key CHECK (public_key ~ '^age1[02-9ac-hj-np-z]{58}$'),
  status            text NOT NULL DEFAULT 'PENDING' CONSTRAINT bkh_status CHECK (status IN ('PENDING', 'ACTIVE', 'REMOVED')),
  check_code_hash   text CONSTRAINT bkh_check CHECK (check_code_hash IS NULL OR check_code_hash ~ '^[0-9a-f]{64}$'),
  loss_acknowledged boolean NOT NULL CONSTRAINT bkh_loss_ack CHECK (loss_acknowledged),
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        text NOT NULL CONSTRAINT bkh_created_by CHECK (length(btrim(created_by)) BETWEEN 1 AND 200),
  confirmed_at      timestamptz,
  removed_at        timestamptz,
  removed_by        text,
  remove_reason     text,
  last_tested_at    timestamptz,
  CONSTRAINT bkh_active_confirmed CHECK (status = 'PENDING' OR confirmed_at IS NOT NULL OR status = 'REMOVED'),
  CONSTRAINT bkh_removed CHECK ((status = 'REMOVED') = (removed_at IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS bkh_site_key ON public.backup_key_holders (tenant_id, site_id, public_key);
COMMENT ON TABLE public.backup_key_holders IS
  'The hotel''s backup key holders (0129, GDPR plan C5, decision D-C5): age public keys every backup is encrypted '
  'to. Private keys are never stored. Written only through the backup_key_holder_* functions.';

CREATE TABLE IF NOT EXISTS public.backup_key_holder_events (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL,
  site_id    uuid NOT NULL,
  holder_id  uuid NOT NULL REFERENCES public.backup_key_holders (id),
  event      text NOT NULL CONSTRAINT bkhe_event CHECK (event IN ('added', 'confirmed', 'removed', 'restore_tested')),
  actor      text NOT NULL CONSTRAINT bkhe_actor CHECK (length(btrim(actor)) BETWEEN 1 AND 200),
  reason     text,
  at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS bkhe_site ON public.backup_key_holder_events (tenant_id, site_id, at DESC);

CREATE OR REPLACE FUNCTION public.backup_key_holder_events_append_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'backup_key_holder_events is append-only: % refused', TG_OP USING ERRCODE = 'restrict_violation';
END $$;
DROP TRIGGER IF EXISTS backup_key_holder_events_append_only ON public.backup_key_holder_events;
CREATE TRIGGER backup_key_holder_events_append_only BEFORE UPDATE OR DELETE ON public.backup_key_holder_events
  FOR EACH ROW EXECUTE FUNCTION public.backup_key_holder_events_append_only();

-- A holder is never deleted.
CREATE OR REPLACE FUNCTION public.backup_key_holders_no_delete() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'a backup key holder is never deleted; remove it instead' USING ERRCODE = 'restrict_violation';
END $$;
DROP TRIGGER IF EXISTS backup_key_holders_no_delete ON public.backup_key_holders;
CREATE TRIGGER backup_key_holders_no_delete BEFORE DELETE ON public.backup_key_holders
  FOR EACH ROW EXECUTE FUNCTION public.backup_key_holders_no_delete();

-- ---------------------------------------------------------------------------------------------------------
-- The writes.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.backup_key_holder_add(p_tenant uuid, p_site uuid, p_label text, p_public_key text,
                                                        p_check_code_hash text, p_loss_acknowledged boolean, p_actor text)
    RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE v_id uuid;
BEGIN
  IF p_loss_acknowledged IS NOT TRUE THEN
    RAISE EXCEPTION 'the hotel must acknowledge that lost keys mean unreadable backups'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  INSERT INTO backup_key_holders (tenant_id, site_id, label, public_key, check_code_hash, loss_acknowledged, created_by)
  VALUES (p_tenant, p_site, btrim(p_label), p_public_key, p_check_code_hash, true, btrim(p_actor))
  RETURNING id INTO v_id;
  INSERT INTO backup_key_holder_events (tenant_id, site_id, holder_id, event, actor)
  VALUES (p_tenant, p_site, v_id, 'added', btrim(p_actor));
  RETURN v_id;
END $$;

-- The check code proves the key file or the recovery sheet was saved. It must be typed within an hour of the
-- key being handed over; after that the holder is started again.
CREATE OR REPLACE FUNCTION public.backup_key_holder_confirm(p_tenant uuid, p_site uuid, p_id uuid,
                                                            p_check_code_hash text, p_actor text)
    RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE h backup_key_holders%ROWTYPE;
BEGIN
  SELECT * INTO h FROM backup_key_holders WHERE id = p_id AND tenant_id = p_tenant AND site_id = p_site FOR UPDATE;
  IF NOT FOUND OR h.status <> 'PENDING' THEN
    RAISE EXCEPTION 'no backup key is waiting for confirmation' USING ERRCODE = 'no_data_found';
  END IF;
  IF h.created_at < now() - interval '1 hour' THEN
    RAISE EXCEPTION 'the key was handed over more than an hour ago; add the holder again' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_check_code_hash IS DISTINCT FROM h.check_code_hash THEN
    RAISE EXCEPTION 'the check code does not match' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  UPDATE backup_key_holders SET status = 'ACTIVE', confirmed_at = now(), check_code_hash = NULL WHERE id = p_id;
  INSERT INTO backup_key_holder_events (tenant_id, site_id, holder_id, event, actor)
  VALUES (p_tenant, p_site, p_id, 'confirmed', btrim(p_actor));
END $$;

CREATE OR REPLACE FUNCTION public.backup_key_holder_remove(p_tenant uuid, p_site uuid, p_id uuid, p_actor text, p_reason text)
    RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE h backup_key_holders%ROWTYPE;
BEGIN
  IF p_reason IS NULL OR length(btrim(p_reason)) NOT BETWEEN 4 AND 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  -- Serialise removals per site, so two at once cannot both pass the last-holder check.
  PERFORM 1 FROM backup_key_holders WHERE tenant_id = p_tenant AND site_id = p_site AND status = 'ACTIVE' FOR UPDATE;
  SELECT * INTO h FROM backup_key_holders WHERE id = p_id AND tenant_id = p_tenant AND site_id = p_site FOR UPDATE;
  IF NOT FOUND OR h.status = 'REMOVED' THEN
    RAISE EXCEPTION 'no such backup key holder' USING ERRCODE = 'no_data_found';
  END IF;
  IF h.status = 'ACTIVE' AND NOT EXISTS (SELECT 1 FROM backup_key_holders
                                          WHERE tenant_id = p_tenant AND site_id = p_site AND status = 'ACTIVE' AND id <> p_id) THEN
    RAISE EXCEPTION 'this is the last backup key holder: add another before removing it' USING ERRCODE = 'object_in_use';
  END IF;
  UPDATE backup_key_holders
     SET status = 'REMOVED', removed_at = now(), removed_by = btrim(p_actor), remove_reason = btrim(p_reason),
         check_code_hash = NULL
   WHERE id = p_id;
  INSERT INTO backup_key_holder_events (tenant_id, site_id, holder_id, event, actor, reason)
  VALUES (p_tenant, p_site, p_id, 'removed', btrim(p_actor), btrim(p_reason));
END $$;

-- A restore test that opened a backup: recorded against the holder whose key it was (any holder, removed ones
-- included, since their key still opens the backups made while they were active).
CREATE OR REPLACE FUNCTION public.backup_key_holder_tested(p_tenant uuid, p_site uuid, p_public_key text, p_actor text)
    RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE v_id uuid;
BEGIN
  UPDATE backup_key_holders SET last_tested_at = now()
   WHERE tenant_id = p_tenant AND site_id = p_site AND public_key = p_public_key AND status <> 'PENDING'
  RETURNING id INTO v_id;
  IF v_id IS NULL THEN
    RAISE EXCEPTION 'this key is not a registered backup key holder' USING ERRCODE = 'no_data_found';
  END IF;
  INSERT INTO backup_key_holder_events (tenant_id, site_id, holder_id, event, actor)
  VALUES (p_tenant, p_site, v_id, 'restore_tested', btrim(p_actor));
  RETURN v_id;
END $$;

-- What every backup is encrypted to: the ACTIVE holders of this site. scd passes its site. The nightly shell
-- backup does not know the site's ids and passes none: it gets the one site that has holders, and if holders
-- of more than one site are ACTIVE -- an appliance moved to another hotel -- it gets an error, never a backup
-- that the previous hotel's keys could open.
CREATE OR REPLACE FUNCTION public.backup_key_recipients(p_tenant uuid DEFAULT NULL, p_site uuid DEFAULT NULL)
    RETURNS SETOF text
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
BEGIN
  IF p_tenant IS NULL OR p_site IS NULL THEN
    IF (SELECT count(DISTINCT (tenant_id, site_id)) FROM backup_key_holders WHERE status = 'ACTIVE') > 1 THEN
      RAISE EXCEPTION 'backup key holders of more than one site are active; name the site'
        USING ERRCODE = 'cardinality_violation';
    END IF;
    RETURN QUERY SELECT public_key FROM backup_key_holders WHERE status = 'ACTIVE' ORDER BY confirmed_at;
  ELSE
    RETURN QUERY SELECT public_key FROM backup_key_holders
                  WHERE status = 'ACTIVE' AND tenant_id = p_tenant AND site_id = p_site ORDER BY confirmed_at;
  END IF;
END $$;

REVOKE ALL ON public.backup_key_holders, public.backup_key_holder_events FROM PUBLIC;
REVOKE ALL ON FUNCTION public.backup_key_holder_add(uuid,uuid,text,text,text,boolean,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.backup_key_holder_confirm(uuid,uuid,uuid,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.backup_key_holder_remove(uuid,uuid,uuid,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.backup_key_holder_tested(uuid,uuid,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.backup_key_recipients(uuid,uuid) FROM PUBLIC;

-- Least privilege, mirrored in deploy/gatep/svc-backup-keys-grants.sql. edged reads the register (not the
-- check-code hash) and writes it through the functions; scd only learns what to encrypt to.
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT (id, tenant_id, site_id, label, public_key, status, created_at, created_by, confirmed_at,
                  removed_at, removed_by, remove_reason, last_tested_at) ON public.backup_key_holders TO svc_edged;
    GRANT SELECT ON public.backup_key_holder_events TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.backup_key_holder_add(uuid,uuid,text,text,text,boolean,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.backup_key_holder_confirm(uuid,uuid,uuid,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.backup_key_holder_remove(uuid,uuid,uuid,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.backup_key_holder_tested(uuid,uuid,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.backup_key_recipients(uuid,uuid) TO svc_edged;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION public.backup_key_recipients(uuid,uuid) TO svc_scd;
  END IF;
END $g$;

COMMIT;
