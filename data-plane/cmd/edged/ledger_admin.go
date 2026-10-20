package main

// THE ERASURE LEDGER AND THE DAILY BACKUP, ON THE BACKUPS SCREEN (GDPR final mission).
//
//	GET  /edge/v1/backups/ledger                       the state of the two copies, and whether erasure is available
//	POST /edge/v1/backups/ledger/destination/probe     an SFTP server's host key fingerprint, to confirm before saving
//	POST /edge/v1/backups/ledger/destination/test      try a secondary storage without saving it
//	PUT  /edge/v1/backups/ledger/destination           save it (password step-up) and make the first copy
//	POST /edge/v1/backups/ledger/recover               bring the ledger back from the secondary storage (password + a key holder's key)
//	GET  /edge/v1/backups/daily                        the daily backups, their checks and the next due
//	POST /edge/v1/backups/daily/run                    take one now (password step-up)
//
// scd owns the two copies, the sealing of the storage credentials and the backups; this only authorises, audits and relays.
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

// readLedgerBody reads a JSON object, takes the step-up password out of it, and returns the rest for scd.
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
		// What was chosen, never the credentials.
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

func (s *server) dailyBackupGet(w http.ResponseWriter, r *http.Request) {
	if code, raw, ok := s.relayJSON(w, r, http.MethodGet, "/v1/backup/daily", nil); ok {
		writeRaw(w, code, raw)
	}
}

func (s *server) dailyBackupRun(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.readLedgerBody(w, r, true); !ok {
		return
	}
	code, raw, ok := s.relayJSON(w, r, http.MethodPost, "/v1/backup/daily/run", map[string]any{})
	if !ok {
		return
	}
	s.audit(r, "backup.daily_run", "backup", "", map[string]any{"status": code})
	writeRaw(w, code, raw)
}
