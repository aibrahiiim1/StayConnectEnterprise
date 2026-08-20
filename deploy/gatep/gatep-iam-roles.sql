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
GRANT REFERENCES ON public.guest_networks TO iam_v2_owner;
