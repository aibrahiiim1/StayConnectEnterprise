-- 0204 -- THE ADMIN CONSOLE SESSION LIMITS CAN BE "NEVER" (Product Owner, 2026-11-02).
-- edge-migrate: platform
--
-- 0132 bounded "sign out after inactivity" to 5-240 minutes and "longest session" to 1-24 hours. The Product Owner asked for a
-- "never" choice for each. Never is stored as 0 in the same two columns; every other value keeps its old bounds, so no
-- existing row changes. edged reads 0 as "no limit" (cmd/edged/operator_mfa.go). The change functions need no edit: they rely on
-- these CHECKs, which are the only place the bounds live in the database.
ALTER TABLE public.operator_session_policy DROP CONSTRAINT IF EXISTS osessp_idle;
ALTER TABLE public.operator_session_policy
  ADD CONSTRAINT osessp_idle CHECK (idle_timeout_minutes = 0 OR idle_timeout_minutes BETWEEN 5 AND 240);
ALTER TABLE public.operator_session_policy DROP CONSTRAINT IF EXISTS osessp_max;
ALTER TABLE public.operator_session_policy
  ADD CONSTRAINT osessp_max CHECK (max_session_hours = 0 OR max_session_hours BETWEEN 1 AND 24);

COMMENT ON TABLE public.operator_session_policy IS
  'Admin Console session limits (0132, 0204): signed out after idle_timeout_minutes without activity and '
  'max_session_hours after signing in, whatever the activity; 0 in either column means never. One row for the appliance; '
  'no row means the defaults (30 minutes, 12 hours).';
