-- Scratch-only role model (NOLOGIN; NO real credentials). Proves production-role isolation.
DO $$ BEGIN CREATE ROLE iam_v2_owner        NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN CREATE ROLE iam_v2_migrator     NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN CREATE ROLE iam_v2_svc_scd      NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN CREATE ROLE iam_v2_svc_edged    NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN CREATE ROLE iam_v2_svc_acctd    NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN CREATE ROLE iam_v2_svc_portald  NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN CREATE ROLE iam_v2_svc_hoteladm NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- migrator may SET ROLE to owner (so objects it creates are owned by the owner)
GRANT iam_v2_owner TO iam_v2_migrator;

-- least privilege: strip PUBLIC create rights; owner needs create-on-db + REFERENCES on the platform anchor
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT CREATE ON DATABASE iam_scratch TO iam_v2_owner;
GRANT REFERENCES ON public.guest_networks TO iam_v2_owner;   -- to build the cross-schema FK only
-- (schema-scoped default privileges are applied in roles_apply.sh AFTER the schema is created)
