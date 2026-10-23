package main

// scd owns the two ledger copies, the sealing of storage credentials and the backups; this only authorises, audits and relays.
// Credentials travel one way (browser to scd) and are never returned or audited.

import (
	"net/http"
)

func (s *server) relayJSON(w http.ResponseWriter, r *http.Request, method, path string, body any) (int, []byte, bool) {
	code, raw, err := s.scd.call(r.Context(), method, path, body)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", "the appliance's backup service could not be reached")
		return 0, nil, false
	}
	return code, raw, true
}

func writeRaw(w http.ResponseWriter, code int, raw []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(raw)
}

func (s *server) ledgerStatusGet(w http.ResponseWriter, r *http.Request) {
	path := "/v1/ledger/status"
	if r.URL.Query().Get("fresh") == "1" {
		path += "?fresh=1"
	}
	if code, raw, ok := s.relayJSON(w, r, http.MethodGet, path, nil); ok {
		writeRaw(w, code, raw)
	}
}

func (s *server) readLedgerBody(w http.ResponseWriter, r *http.Request, needPassword bool) (map[string]any, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var in map[string]any
	if err := decodeJSON(r, &in); err != nil || in == nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return nil, false
	}
	pw, _ := in["password"].(string)
	delete(in, "password")
	if needPassword && !s.reauth(r, pw) {
		s.reauthRefused(w, r, "password confirmation required")
		return nil, false
	}
	// "password" is the operator's step-up password; the SFTP server's travels as "sftp_password" so the two cannot
	// collide, and scd knows it as "password".
	if v, ok := in["sftp_password"]; ok {
		delete(in, "sftp_password")
		in["password"] = v
	}
	return in, true
}

func (s *server) ledgerDestinationProbe(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readLedgerBody(w, r, false)
	if !ok {
		return
	}
	if code, raw, ok := s.relayJSON(w, r, http.MethodPost, "/v1/ledger/destination/probe", in); ok {
		writeRaw(w, code, raw)
	}
}

func (s *server) ledgerDestinationTest(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readLedgerBody(w, r, false)
	if !ok {
		return
	}
	in["test_only"] = true
	if code, raw, ok := s.relayJSON(w, r, http.MethodPut, "/v1/ledger/destination", in); ok {
		writeRaw(w, code, raw)
	}
}

func (s *server) ledgerDestinationSave(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readLedgerBody(w, r, true)
	if !ok {
		return
	}
	delete(in, "test_only")
	in["by"] = protectionActor(sessFrom(r.Context()))
	kind, _ := in["kind"].(string)
	code, raw, ok := s.relayJSON(w, r, http.MethodPut, "/v1/ledger/destination", in)
	if !ok {
		return
	}
	if code == http.StatusOK {
		s.audit(r, "backup.ledger_storage_changed", "ledger", "", map[string]any{"kind": kind})
	} else {
		s.audit(r, "backup.ledger_storage_refused", "ledger", "", map[string]any{"kind": kind, "status": code})
	}
	writeRaw(w, code, raw)
}

func (s *server) ledgerRecover(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readLedgerBody(w, r, true)
	if !ok {
		return
	}
	code, raw, ok := s.relayJSON(w, r, http.MethodPost, "/v1/ledger/recover", map[string]any{"identity": in["identity"]})
	if !ok {
		return
	}
	s.audit(r, "backup.ledger_recovered", "ledger", "", map[string]any{"status": code})
	writeRaw(w, code, raw)
}

func (s *server) backupStatusGet(w http.ResponseWriter, r *http.Request) {
	if code, raw, ok := s.relayJSON(w, r, http.MethodGet, "/v1/backup/status", nil); ok {
		writeRaw(w, code, raw)
	}
}

const earlierBackupsConfirmation = "remove earlier backups"

// earlierBackupsRemove is the only way the earlier backups are deleted (rotation and retention never touch them).
// scd refuses unless a verified current backup exists and every stored backup matches its checksum.
func (s *server) earlierBackupsRemove(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readLedgerBody(w, r, true)
	if !ok {
		return
	}
	if c, _ := in["confirm"].(string); c != earlierBackupsConfirmation {
		jsonErr(w, http.StatusBadRequest, "confirm_mismatch", "type \""+earlierBackupsConfirmation+"\" to confirm")
		return
	}
	code, raw, ok := s.relayJSON(w, r, http.MethodPost, "/v1/backup/earlier/remove", map[string]any{"by": protectionActor(sessFrom(r.Context()))})
	if !ok {
		return
	}
	s.audit(r, "backup.earlier_removed", "backup", "", map[string]any{"status": code})
	writeRaw(w, code, raw)
}
