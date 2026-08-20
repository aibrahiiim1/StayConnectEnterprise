-- DOWN IS DELIBERATELY NOT A RESTORE.
--
-- Recreating public.sessions, public.vouchers, public.ticket_templates and the rest would recreate the
-- superseded guest-IAM domain the Production baseline exists to be free of -- empty, unreadable by any
-- current code, and indistinguishable in the catalog from the real thing. A rollback that reintroduces the
-- structure without the implementation gives back the risk and none of the function.
--
-- If an installation must return to the superseded domain, that is a restore from a backup taken before the
-- upgrade, not a DDL step: the tables without their rows are not the state anyone would be rolling back to.
--
-- The dropped access-plan columns are restored as NULLABLE and WITHOUT their foreign key, because their
-- referent no longer exists. That is enough for the 0049 boundary to be crossable in both directions during
-- an upgrade rehearsal without pretending the data came back.
\set ON_ERROR_STOP on

ALTER TABLE public.auth_otps           ADD COLUMN IF NOT EXISTS template_id uuid;
ALTER TABLE public.social_oauth_states ADD COLUMN IF NOT EXISTS template_id uuid;
