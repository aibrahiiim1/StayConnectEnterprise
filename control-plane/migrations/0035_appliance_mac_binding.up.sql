-- Hardware MAC identity for appliances. The WAN MAC is the licensing hardware
-- anchor (a mismatch/rebind signal, never the sole trust anchor); the LAN MAC
-- is inventory. The StayConnect serial + hardware_fingerprint already exist
-- (serial column; hardware_fingerprint from 0022) — they were previously
-- write-dead and are now populated by signed registration/enrollment.
ALTER TABLE appliances
  ADD COLUMN IF NOT EXISTS wan_mac text,
  ADD COLUMN IF NOT EXISTS lan_mac text;

-- Registration/appearance metadata used by the Pending Activation view. Most
-- exist already; add the ones that don't so a token-less appearance can record
-- everything the operator needs to identify the box.
ALTER TABLE appliances
  ADD COLUMN IF NOT EXISTS last_seen_at timestamptz;

COMMENT ON COLUMN appliances.wan_mac IS 'Permanent physical WAN NIC MAC (licensing hardware anchor, normalized aa:bb:...).';
COMMENT ON COLUMN appliances.lan_mac IS 'Permanent physical LAN NIC MAC (inventory).';
