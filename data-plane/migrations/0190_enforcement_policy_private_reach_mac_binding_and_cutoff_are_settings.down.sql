-- Removing the enforcement policy. WHAT THIS COSTS: netd and acctd fall back to their built-in defaults (private
-- destinations denied, MAC binding on, 5 s cut-off) because they treat a missing table as "defaults"; any hotel
-- allow-list and the change history are dropped with the tables -- copy them first if wanted.
BEGIN;
DROP FUNCTION IF EXISTS public.enforcement_policy_set(text,text,text,text);
DROP TRIGGER IF EXISTS enforcement_policy_changes_append_only ON public.enforcement_policy_changes;
DROP TABLE IF EXISTS public.enforcement_policy_changes;
DROP FUNCTION IF EXISTS public.enforcement_policy_changes_append_only();
DROP TABLE IF EXISTS public.enforcement_policy;
COMMIT;
