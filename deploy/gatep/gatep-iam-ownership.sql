-- Gate P — assert authoritative ownership of the IAM-v2 domain. Idempotent.
--
-- WHY THIS STEP EXISTS
-- --------------------
-- Migrations 0009 onwards create and alter iam_v2 objects, and they must run as the PLATFORM role because
-- they also maintain public.schema_migrations and public objects -- running them as iam_v2_owner fails
-- immediately with "permission denied for table schema_migrations". The consequence is that a factory-clean
-- build leaves those objects owned by whoever ran the install, while the accepted baseline owns them as
-- iam_v2_owner.
--
-- That is not cosmetic. The IAM boundary functions are SECURITY DEFINER and execute as their OWNER, so
-- ownership decides what they can reach. A schema that matches on every column, constraint and index and is
-- wrong on ownership is a different security model wearing the same shape.
--
-- So ownership is ASSERTED here rather than inherited: every table, view, sequence and function in iam_v2 is
-- reassigned to iam_v2_owner, and the assertion at the end refuses to pass if anything is left behind.
\set ON_ERROR_STOP on

DO $$
DECLARE r record;
BEGIN
  -- Tables, views and materialised views.
  FOR r IN
    SELECT c.relname, c.relkind FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'iam_v2' AND c.relkind IN ('r','v','m','p')
       AND pg_get_userbyid(c.relowner) <> 'iam_v2_owner'
  LOOP
    IF r.relkind = 'v' THEN
      EXECUTE format('ALTER VIEW iam_v2.%I OWNER TO iam_v2_owner', r.relname);
    ELSIF r.relkind = 'm' THEN
      EXECUTE format('ALTER MATERIALIZED VIEW iam_v2.%I OWNER TO iam_v2_owner', r.relname);
    ELSE
      EXECUTE format('ALTER TABLE iam_v2.%I OWNER TO iam_v2_owner', r.relname);
    END IF;
  END LOOP;

  -- Sequences. A sequence owned by the wrong role can be altered by that role, which is a write path into a
  -- table's identity column that the table's own privileges do not describe.
  FOR r IN
    SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'iam_v2' AND c.relkind = 'S'
       AND pg_get_userbyid(c.relowner) <> 'iam_v2_owner'
  LOOP
    EXECUTE format('ALTER SEQUENCE iam_v2.%I OWNER TO iam_v2_owner', r.relname);
  END LOOP;

  -- Functions and procedures. This is the one that matters most: a SECURITY DEFINER function executes as its
  -- owner, so the wrong owner here silently changes the privilege the whole boundary runs with.
  FOR r IN
    SELECT p.oid, p.proname, pg_get_function_identity_arguments(p.oid) AS args, p.prokind
      FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
     WHERE n.nspname = 'iam_v2' AND pg_get_userbyid(p.proowner) <> 'iam_v2_owner'
  LOOP
    IF r.prokind = 'p' THEN
      EXECUTE format('ALTER PROCEDURE iam_v2.%I(%s) OWNER TO iam_v2_owner', r.proname, r.args);
    ELSE
      EXECUTE format('ALTER FUNCTION iam_v2.%I(%s) OWNER TO iam_v2_owner', r.proname, r.args);
    END IF;
  END LOOP;
END $$;

-- The schema itself.
ALTER SCHEMA iam_v2 OWNER TO iam_v2_owner;

-- FAIL CLOSED. An ownership step that silently leaves objects behind is worse than none, because the next
-- reader assumes the model holds.
DO $$
DECLARE bad int; detail text;
BEGIN
  SELECT count(*), string_agg(x.name, ', ') INTO bad, detail FROM (
    SELECT n.nspname||'.'||c.relname AS name FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
      WHERE n.nspname='iam_v2' AND c.relkind IN ('r','v','m','p','S')
        AND pg_get_userbyid(c.relowner) <> 'iam_v2_owner'
    UNION ALL
    SELECT n.nspname||'.'||p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
      WHERE n.nspname='iam_v2' AND pg_get_userbyid(p.proowner) <> 'iam_v2_owner'
  ) x;
  IF bad > 0 THEN
    RAISE EXCEPTION 'GATE-P BLOCKER: % iam_v2 object(s) are not owned by iam_v2_owner: %', bad, left(detail, 400);
  END IF;
END $$;

-- Every SECURITY DEFINER function in iam_v2 must now execute as iam_v2_owner. Asserted separately from the
-- ownership sweep above: the sweep is what we did, this is what must be true afterwards.
DO $$
DECLARE bad int;
BEGIN
  SELECT count(*) INTO bad FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
   WHERE n.nspname='iam_v2' AND p.prosecdef AND pg_get_userbyid(p.proowner) <> 'iam_v2_owner';
  IF bad > 0 THEN
    RAISE EXCEPTION 'GATE-P BLOCKER: % SECURITY DEFINER function(s) in iam_v2 execute as the wrong owner', bad;
  END IF;
END $$;
