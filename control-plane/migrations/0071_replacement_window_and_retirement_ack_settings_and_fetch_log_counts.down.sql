-- 0071 down -- the replacement window (72 h) and the retirement acknowledgement timeout (10 min) are constants again.
BEGIN;
ALTER TABLE appliance_assignment_fetch_log DROP COLUMN IF EXISTS hits;
ALTER TABLE central_security_settings DROP COLUMN IF EXISTS retirement_ack_minutes;
ALTER TABLE central_security_settings DROP COLUMN IF EXISTS replacement_window_hours;
COMMIT;
