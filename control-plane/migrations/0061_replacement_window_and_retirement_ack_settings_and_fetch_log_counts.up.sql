-- 0061 -- REPLACEMENT WINDOW AND RETIREMENT ACKNOWLEDGEMENT TIMEOUT AS SETTINGS; FETCH LOG KEEPS NOT-MODIFIED COUNTS (AD-54)
--
-- CLAUDE.md 0C: operational values an administrator may reasonably change ship as persisted, audited settings.
--
--   replacement_window_hours   how long an appliance marked for replacement stays licensed until its replacement is
--                              activated at the same site. Default 72 h, 1..720 (was a constant in internal/api).
--   retirement_ack_minutes     how long Central waits for a retiring appliance's signed acknowledgement before it
--                              raises terminal_delivery_failed. Default 10 min, 1..120 (was a constant).
--
-- Security limits (signature checks, certificate lifetimes, the emergency path) stay fixed in code.
--
-- appliance_assignment_fetch_log: every 30-second poll used to add a row. Served and denied fetches are still one
-- row each; "not-modified" fetches now share one row per appliance, outcome and hour, counted in `hits`.
--
-- Additive: two columns with defaults on a one-row table, one column with a default on the log.

BEGIN;

ALTER TABLE central_security_settings
  ADD COLUMN IF NOT EXISTS replacement_window_hours int NOT NULL DEFAULT 72
    CHECK (replacement_window_hours BETWEEN 1 AND 720),
  ADD COLUMN IF NOT EXISTS retirement_ack_minutes int NOT NULL DEFAULT 10
    CHECK (retirement_ack_minutes BETWEEN 1 AND 120);
COMMENT ON COLUMN central_security_settings.replacement_window_hours IS
  'Hours an appliance marked for replacement stays licensed until its replacement is activated (0061, AD-54). Default 72, 1..720.';
COMMENT ON COLUMN central_security_settings.retirement_ack_minutes IS
  'Minutes Central waits for a retiring appliance''s signed acknowledgement before raising terminal_delivery_failed (0061, AD-54). Default 10, 1..120.';

ALTER TABLE appliance_assignment_fetch_log ADD COLUMN IF NOT EXISTS hits int NOT NULL DEFAULT 1 CHECK (hits >= 1);
COMMENT ON COLUMN appliance_assignment_fetch_log.hits IS
  'How many fetches this row stands for: 1 for served and denied, the hour''s count for a not-modified row (0061, AD-54).';

COMMIT;
