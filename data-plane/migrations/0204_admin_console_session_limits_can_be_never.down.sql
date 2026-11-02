-- 0204 down: restores the 0132 bounds. A row that says "never" (0) is first set to the widest limit those bounds allow,
-- so the constraint can be re-added.
UPDATE public.operator_session_policy SET idle_timeout_minutes = 240 WHERE idle_timeout_minutes = 0;
UPDATE public.operator_session_policy SET max_session_hours = 24 WHERE max_session_hours = 0;
ALTER TABLE public.operator_session_policy DROP CONSTRAINT IF EXISTS osessp_idle;
ALTER TABLE public.operator_session_policy
  ADD CONSTRAINT osessp_idle CHECK (idle_timeout_minutes BETWEEN 5 AND 240);
ALTER TABLE public.operator_session_policy DROP CONSTRAINT IF EXISTS osessp_max;
ALTER TABLE public.operator_session_policy
  ADD CONSTRAINT osessp_max CHECK (max_session_hours BETWEEN 1 AND 24);
