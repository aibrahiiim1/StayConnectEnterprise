-- 0203 -- THE SECONDARY STORAGE CAN BE REMOVED, AND ITS CHANGES ARE KEPT (Product Owner mission, 2026-10-31).
--
-- The erasure ledger's secondary storage (0148) could be set but never removed, and nothing recorded what it had been before a
-- change. This adds:
--   * public.ledger_destination_clear(p_by): removes the setting (never the copy kept there). Erasure is then refused again, exactly
--     as on an appliance that never had one: the 0148 rule that an erasure needs two durable copies is unchanged.
--   * public.ledger_destination_changes: one append-only row per set, replace or removal (kind and the non-secret description only;
--     never a credential), written by a trigger so no path can change the setting without leaving a row.
--
-- scd now calls ledger_destination_set only after the new destination has been written and read back (safe handover), so the
-- verified_at it stamps is true when it is stamped.
--
-- Additive and reversible: the .down.sql drops the function, the trigger and the table. No data is touched.
BEGIN;

CREATE TABLE IF NOT EXISTS public.ledger_destination_changes (
  id          bigserial PRIMARY KEY,
  at          timestamptz NOT NULL DEFAULT now(),
  action      text NOT NULL CHECK (action IN ('SET','REPLACED','REMOVED')),
  kind        text,
  describe    jsonb,
  changed_by  text NOT NULL
);
COMMENT ON TABLE public.ledger_destination_changes IS
  'Every change of the erasure ledger''s secondary storage (0203): kind and non-secret settings only. Append-only.';

CREATE OR REPLACE FUNCTION public.ledger_destination_changes_append_only() RETURNS trigger
  LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $f$
BEGIN
  RAISE EXCEPTION 'secondary storage changes are append-only' USING ERRCODE = 'insufficient_privilege';
END $f$;
DROP TRIGGER IF EXISTS ledger_destination_changes_append_only ON public.ledger_destination_changes;
CREATE TRIGGER ledger_destination_changes_append_only BEFORE UPDATE OR DELETE ON public.ledger_destination_changes
  FOR EACH ROW EXECUTE FUNCTION public.ledger_destination_changes_append_only();

-- The config column holds no secret (scd seals credentials into secret_ct); the access key id of an S3 destination is an
-- identifier, not a secret, and is kept so a rotated key can be told apart.
CREATE OR REPLACE FUNCTION public.ledger_destination_record_change() RETURNS trigger
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $f$
BEGIN
  IF TG_OP = 'DELETE' THEN
    INSERT INTO public.ledger_destination_changes (action, kind, describe, changed_by)
    VALUES ('REMOVED', OLD.kind, OLD.config, COALESCE(NULLIF(current_setting('stayconnect.ledger_changed_by', true), ''), 'system'));
    RETURN OLD;
  END IF;
  IF TG_OP = 'UPDATE' AND NEW.kind IS NOT DISTINCT FROM OLD.kind AND NEW.config IS NOT DISTINCT FROM OLD.config
     AND NEW.secret_ct IS NOT DISTINCT FROM OLD.secret_ct THEN
    RETURN NEW;  -- a status note (last_ok_at, last_error), not a change of where the copy is kept
  END IF;
  INSERT INTO public.ledger_destination_changes (action, kind, describe, changed_by)
  VALUES (CASE WHEN TG_OP = 'INSERT' THEN 'SET' ELSE 'REPLACED' END, NEW.kind, NEW.config, NEW.configured_by);
  RETURN NEW;
END $f$;
DROP TRIGGER IF EXISTS ledger_destination_record_change ON public.ledger_destination;
CREATE TRIGGER ledger_destination_record_change AFTER INSERT OR UPDATE OR DELETE ON public.ledger_destination
  FOR EACH ROW EXECUTE FUNCTION public.ledger_destination_record_change();

CREATE OR REPLACE FUNCTION public.ledger_destination_clear(p_by text) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE n integer;
BEGIN
  IF p_by IS NULL OR length(btrim(p_by)) = 0 THEN
    RAISE EXCEPTION 'who removed the secondary storage is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM set_config('stayconnect.ledger_changed_by', left(btrim(p_by), 200), true);
  DELETE FROM public.ledger_destination;
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n > 0;
END $$;

CREATE OR REPLACE FUNCTION public.ledger_destination_history(p_limit integer) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
  SELECT COALESCE(jsonb_agg(jsonb_build_object('at', c.at, 'action', c.action, 'kind', c.kind, 'settings', c.describe,
                                               'by', c.changed_by) ORDER BY c.id DESC), '[]'::jsonb)
    FROM (SELECT * FROM public.ledger_destination_changes ORDER BY id DESC LIMIT greatest(1, least(COALESCE(p_limit, 20), 200))) c
$$;

REVOKE ALL ON public.ledger_destination_changes FROM PUBLIC;
REVOKE ALL ON FUNCTION public.ledger_destination_clear(text), public.ledger_destination_history(integer),
  public.ledger_destination_record_change(), public.ledger_destination_changes_append_only() FROM PUBLIC;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.ledger_destination_clear(text) TO svc_scd';
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.ledger_destination_history(integer) TO svc_scd';
  END IF;
END $g$;

COMMIT;
