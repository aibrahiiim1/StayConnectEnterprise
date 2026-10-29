BEGIN;
DROP FUNCTION IF EXISTS public.auth_throttle_charge(text[],text[],text[],integer[],integer[],boolean[],timestamptz,timestamptz,integer);
COMMIT;
