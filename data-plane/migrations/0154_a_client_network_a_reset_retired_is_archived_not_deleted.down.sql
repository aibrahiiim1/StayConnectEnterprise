-- Down for 0154. The archive markers are lost: every archived network reappears in the main list, still switched off. Nothing else changes.

BEGIN;

DROP TABLE IF EXISTS iam_v2.network_archive;

COMMIT;
