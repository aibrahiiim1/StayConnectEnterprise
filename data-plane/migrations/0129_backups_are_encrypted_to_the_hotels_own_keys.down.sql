-- 0129 down -- backups are no longer encrypted by the appliance; KEEP the register of key holders.
--
-- The functions go, so scd and the nightly backup find no recipients and write backups in the clear again,
-- as before 0129. Kept: the holder register and its event log -- the record of which keys older backups are
-- encrypted to, which a rollback must not lose. Encrypted backups stay encrypted and open with those keys.

BEGIN;

DROP FUNCTION IF EXISTS public.backup_key_recipients(uuid,uuid);
DROP FUNCTION IF EXISTS public.backup_key_holder_tested(uuid,uuid,text,text);
DROP FUNCTION IF EXISTS public.backup_key_holder_remove(uuid,uuid,uuid,text,text);
DROP FUNCTION IF EXISTS public.backup_key_holder_confirm(uuid,uuid,uuid,text,text);
DROP FUNCTION IF EXISTS public.backup_key_holder_add(uuid,uuid,text,text,text,boolean,text);

COMMIT;
