-- Reverses 0201: the transfer function goes; the Go caller then needs the previous (direct-write) implementation.
BEGIN;
DROP FUNCTION IF EXISTS iam_v2.cross_pms_transfer(uuid, uuid, uuid, uuid, uuid, text, integer);
COMMIT;
