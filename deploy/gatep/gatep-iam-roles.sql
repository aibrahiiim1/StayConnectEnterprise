-- Gate P — IAM-v2 domain roles (site DB). Idempotent. NO passwords here.
--
-- WHY THIS FILE EXISTS
-- --------------------
-- gatep-roles.sql creates the four runtime SERVICE roles. It does not create the roles that OWN the IAM-v2
-- domain, and until now those existed only in iam_v2_scratch/roles.sql -- a file whose own header says
-- "Scratch-only role model". A factory-clean install that sourced its production ownership from a file
-- documented as scratch would be building the security model out of a test fixture.
--
-- Ownership here is load-bearing, not cosmetic. The IAM-v2 boundary functions are SECURITY DEFINER and
-- execute as their OWNER, so which role owns them decides what they can reach. Objects created by the wrong
-- role produce a schema that passes a table count and fails the security model.
--
-- NOLOGIN throughout: these roles own and migrate, they are never connected as. Passwords for the LOGIN
-- service roles are set separately by gatep-set-passwords.sh, which computes a SCRAM verifier on the
-- appliance so cleartext never reaches SQL, argv or a log.

\set ON_ERROR_STOP on

DO $$ BEGIN CREATE ROLE iam_v2_owner    NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN CREATE ROLE iam_v2_migrator NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- The migrator may SET ROLE to the owner, so every object it creates is owned by the owner rather than by
-- whoever happened to run the install. This is what makes ownership reproducible instead of incidental.
GRANT iam_v2_owner TO iam_v2_migrator;

ALTER ROLE iam_v2_owner    NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOREPLICATION;
ALTER ROLE iam_v2_migrator NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOREPLICATION;

-- PUBLIC must not be able to create in public: an unprivileged role that can create objects can shadow a
-- table name a SECURITY DEFINER function resolves.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;

-- The owner must be able to CREATE the iam_v2 schema in this database. Written against current_database()
-- rather than a hardcoded name so the same file serves the appliance, a rebuilt appliance and a clean-room
-- reconstruction without editing.
DO $$ BEGIN
  EXECUTE format('GRANT CREATE ON DATABASE %I TO iam_v2_owner', current_database());
END $$;

-- The owner needs REFERENCES on the platform anchor to build the one cross-schema foreign key:
-- iam_v2.guest_network_pms_map (tenant_id, site_id, guest_network_id)
--   -> public.guest_networks (tenant_id, site_id, id), through guest_networks_tsi_anchor.
-- REFERENCES only -- not SELECT, not INSERT. The IAM domain anchors to the platform, it does not read it.
-- GUARDED so this file is ORDER-INDEPENDENT.
--
-- Two install paths need it in opposite orders. The UPGRADE path runs this before migration 0009, by which
-- point 0002 has created public.guest_networks. The factory-clean BASELINE path carries its own privileges,
-- which name iam_v2_owner, so the roles must exist BEFORE the baseline is applied -- and at that moment
-- guest_networks does not exist yet.
--
-- Rather than pick an order that breaks one of them, the grant is skipped when its target is absent and the
-- file is applied again after the schema exists. It is idempotent either way.
DO $$ BEGIN
  IF to_regclass('public.guest_networks') IS NOT NULL THEN
    EXECUTE 'GRANT REFERENCES ON public.guest_networks TO iam_v2_owner';
  ELSE
    RAISE NOTICE 'public.guest_networks does not exist yet; re-run this file after the schema is built';
  END IF;
END $$;

-- AND public.operators, FOR THE SAME REASON AND BY THE SAME PATTERN.
--
-- iam_v2 tables declare FOREIGN KEYs to public.operators so that a request body cannot invent an operator:
-- migration 0030 did it for appliance_product_setting_changes, and the voucher reveal audit does it for
-- operator_id. Creating such a constraint needs REFERENCES on the TARGET table, which the cluster's
-- administrative role owns.
--
-- BOTH INSTALL PATHS HID THE OMISSION, in opposite ways. A factory-clean reconstruction runs the numbered
-- migrations as a SUPERUSER -- the platform migrations create extensions -- so REFERENCES is never checked.
-- The appliance's UPGRADE path runs them through scripts/edge-migrate.sh with a least-privilege
-- --apply-role, which is the entire point of that runner, and there the same migration fails with
-- "permission denied for table operators". Measured on PRE-LIVE: migration 0087 was refused for exactly
-- this, and the runner correctly left nothing applied.
--
-- It is granted HERE rather than in gatep-grants.sql because this file runs BEFORE the numbered migrations
-- on both paths and that one runs after. A privilege a migration needs is useless if it arrives later.
--
-- REFERENCES IS NARROW: it permits a constraint pointing AT the table and nothing else. Not SELECT (which
-- gatep-grants.sql grants separately, for the actor check), not INSERT, not UPDATE, not DELETE.
DO $$ BEGIN
  IF to_regclass('public.operators') IS NOT NULL THEN
    EXECUTE 'GRANT REFERENCES ON public.operators TO iam_v2_owner';
  ELSE
    RAISE NOTICE 'public.operators does not exist yet; re-run this file after the schema is built';
  END IF;
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- EVERY iam_v2 TABLE BELONGS TO iam_v2_owner, AND ONLY THIS FILE CAN MAKE THAT TRUE
-- ---------------------------------------------------------------------------------------------------------
-- ALTER TABLE ... OWNER TO requires the executing role to be a member of BOTH the current owner and the
-- new one, so a table that ended up belonging to the administrative role can be reassigned only by the
-- administrative role. A migration cannot do it, which is why migration 0090 asserts the invariant and this
-- file establishes it.
--
-- FOUR TABLES ON PRE-LIVE NEEDED IT, from two causes, neither visible to a table count:
--   * three created by a migration running as the applying role instead of the owner (0085, 0087), and
--   * iam_v2.backfill_0066_identity_text, owned by the administrative role because migration 0066 was
--     applied BY HAND rather than through scripts/edge-migrate.sh -- the same event that left 0066 with no
--     ledger row.
--
-- It matters because iam_v2_rollback is a member of iam_v2_owner and of nothing else: a table owned by
-- anybody else cannot be dropped by the guarded rollback path, so a rollback would work for most of the
-- schema and fail on exactly the newest part of it.
--
-- WRITTEN AS A SWEEP, not a list of names, because the next one will have a name nobody has written yet.
-- It is idempotent and it reports what it moved.
DO $ownsweep$
DECLARE r record; n int := 0;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN RETURN; END IF;
  IF to_regnamespace('iam_v2') IS NULL THEN RETURN; END IF;
  FOR r IN
    SELECT c.relname
      FROM pg_class c JOIN pg_namespace n2 ON n2.oid = c.relnamespace
     WHERE n2.nspname = 'iam_v2' AND c.relkind = 'r'
       AND pg_get_userbyid(c.relowner) <> 'iam_v2_owner'
  LOOP
    EXECUTE format('ALTER TABLE iam_v2.%I OWNER TO iam_v2_owner', r.relname);
    RAISE NOTICE 'reassigned iam_v2.% to iam_v2_owner', r.relname;
    n := n + 1;
  END LOOP;
  IF n = 0 THEN RAISE NOTICE 'every iam_v2 table already belongs to iam_v2_owner'; END IF;
END $ownsweep$;

-- ---------------------------------------------------------------------------------------------------------
-- THE ROLLBACK ROLE, WHICH THE DOWN-MIGRATION RUNNER REQUIRES AND NOTHING CREATED
-- ---------------------------------------------------------------------------------------------------------
-- scripts/edge-migrate.sh --down requires the OPPOSITE ledger privilege from the forward path: SELECT and
-- DELETE on public.schema_migrations, where the forward path refuses DELETE on a live site. That is
-- deliberate and it is the strongest guard the runner has -- the role that migrates forward structurally
-- cannot roll back -- but it only works if a role with that privilege EXISTS.
--
-- On PRE-LIVE it did not. Measured: neither iam_v2_migrator nor iam_v2_owner holds DELETE on the ledger, so
-- the guarded rollback path delivered in increment 2 could not be run on the appliance at all. A mechanism
-- with no provisioning is the same defect as a policy with no mechanism, one layer down.
--
-- NOLOGIN, like the others: it is reached by SET ROLE from an administrative connection, never connected as.
--
-- IT IS A MEMBER OF iam_v2_owner because a down migration DROPS objects the owner owns, and only the owner
-- (or a member) may drop them. That is the same reason the down self-test's fixture grants the apply role to
-- the rollback role -- a gap that self-test found the hard way, by refusing a legitimate rollback with
-- "must be owner of table".
DO $$ BEGIN CREATE ROLE iam_v2_rollback NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END $$;
ALTER ROLE iam_v2_rollback NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOREPLICATION;
GRANT iam_v2_owner TO iam_v2_rollback;

-- The ledger privileges that define it. DELETE is the one that matters: it is what the down runner verifies
-- it has, and what the forward runner verifies it does NOT have.
DO $$ BEGIN
  IF to_regclass('public.schema_migrations') IS NOT NULL THEN
    EXECUTE 'GRANT SELECT, DELETE ON public.schema_migrations TO iam_v2_rollback';
  ELSE
    RAISE NOTICE 'public.schema_migrations does not exist yet; re-run this file after the schema is built';
  END IF;
END $$;
