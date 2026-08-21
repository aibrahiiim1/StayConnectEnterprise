-- Dropping this loses the record of which appliance proved possession of which key, and with it the audit
-- trail for every offline activation. It is reversible only in the sense that the table can be recreated
-- empty; the evidence does not come back.
DROP INDEX IF EXISTS offline_req_open_idx;
DROP INDEX IF EXISTS offline_req_appl_idx;
DROP TABLE IF EXISTS offline_activation_requests;
