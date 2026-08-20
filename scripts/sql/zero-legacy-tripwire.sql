-- THE ZERO-LEGACY TRIPWIRE.
--
-- Installed on an EMPTY database, before any install step runs, so that constructing a superseded guest-IAM
-- table is impossible rather than merely undone. It fires on every CREATE TABLE in the session and raises on
-- a superseded name, which means a baseline that built one could not finish.
--
-- Why an event trigger and not a catalog check: inspecting pg_tables afterwards cannot distinguish "never
-- created" from "created and then dropped", and those are exactly the two things the Production requirement
-- distinguishes. A create-then-delete install leaves the superseded tables in every WAL segment, in any
-- backup taken mid-install, and in the install log a reviewer reads.
\set ON_ERROR_STOP on

CREATE OR REPLACE FUNCTION public.zero_legacy_tripwire() RETURNS event_trigger LANGUAGE plpgsql AS $$
DECLARE r record;
BEGIN
  FOR r IN SELECT * FROM pg_event_trigger_ddl_commands() LOOP
    IF r.object_type = 'table'
       AND split_part(r.object_identity, '.', 1) = 'public'
       AND split_part(r.object_identity, '.', 2) IN
           ('sessions','guests','guest_accounts','vouchers','voucher_batches','ticket_templates','payments')
    THEN
      RAISE EXCEPTION 'ZERO-LEGACY TRIPWIRE: the factory-clean baseline constructed %', r.object_identity;
    END IF;
  END LOOP;
END $$;

CREATE EVENT TRIGGER zero_legacy_tripwire ON ddl_command_end
  WHEN TAG IN ('CREATE TABLE', 'CREATE TABLE AS', 'SELECT INTO')
  EXECUTE FUNCTION public.zero_legacy_tripwire();
