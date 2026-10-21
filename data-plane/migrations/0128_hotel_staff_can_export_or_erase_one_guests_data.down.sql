-- 0128 down -- staff can no longer look up, export or erase one guest from Hotel Admin; KEEP the erasure record.
--
-- The functions go. Kept: iam_v2.guest_data_erasures, the proof of which erasures ran -- a rollback must not
-- erase that. ERASED DATA IS NOT RESTORED: there is nothing to restore it from.

BEGIN;

DROP FUNCTION IF EXISTS public.guest_data_erase(uuid,uuid,text,uuid,text,text);
DROP FUNCTION IF EXISTS public.guest_data_export(uuid,uuid,text,uuid);
DROP FUNCTION IF EXISTS iam_v2.erase_guest_records(uuid,uuid,text,uuid,text,text,jsonb);
DROP FUNCTION IF EXISTS iam_v2.guest_data_export_records(uuid,uuid,text,uuid);
DROP FUNCTION IF EXISTS iam_v2.guest_data_scope(uuid,uuid,text,uuid);
DROP FUNCTION IF EXISTS iam_v2.guest_data_find(uuid,uuid,text);

COMMIT;
