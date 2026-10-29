-- 0062 down -- a deleted appliance's history is cascaded away with it again (0022/0023/0050). The archive is dropped.
BEGIN;
DROP TABLE IF EXISTS appliance_history_archive;
COMMIT;
