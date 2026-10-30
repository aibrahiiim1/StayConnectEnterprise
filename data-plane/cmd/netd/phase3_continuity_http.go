package main

// The two routes of the continuity governor.
//
//	POST /v1/phase3/continuity/revoke   (classFlows: scd)   a durable, immediate "this session / entitlement / address must not be
//	                                                          kept alive by outage continuity", independent of acctd and the database.
//	GET  /v1/phase3/continuity          (classAdmin)        what the governor holds and has done.

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"
)

type continuityRevokeReq struct {
	SessionIDs     []string `json:"session_ids"`
	EntitlementIDs []string `json:"entitlement_ids"`
	IP             string   `json:"ip"`
	Bridge         string   `json:"bridge"`
	Reason         string   `json:"reason"`
}

func (s *server) continuityRevoke(w http.ResponseWriter, r *http.Request) {
	var req continuityRevokeReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed body"})
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || len(reason) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "reason (a short machine code) is required"})
		return
	}
	if len(req.SessionIDs)+len(req.EntitlementIDs) == 0 && req.IP == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name a session, an entitlement or an address"})
		return
	}
	if req.IP != "" && net.ParseIP(req.IP).To4() == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ip must be an IPv4 address"})
		return
	}
	p := s.phase3
	if p == nil {
		writeJSON(w, http.StatusOK, map[string]any{"journalled": 0})
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	var jerr error
	at := time.Now().UTC()
	for _, id := range req.SessionIDs {
		if e := p.contRevoke(r.Context(), contRevocation{SessionID: id, Reason: reason, At: at}); e != nil {
			jerr = e
		}
		n++
	}
	for _, id := range req.EntitlementIDs {
		if e := p.contRevoke(r.Context(), contRevocation{EntitlementID: id, Reason: reason, At: at}); e != nil {
			jerr = e
		}
		n++
	}
	if req.IP != "" {
		if e := p.contRevoke(r.Context(), contRevocation{IP: req.IP, Bridge: req.Bridge, Reason: reason, At: at}); e != nil {
			jerr = e
		}
		n++
	}
	if jerr != nil {
		// Applied in memory, not proven on disk: the caller must retry, not assume.
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "revocation journal not written: " + jerr.Error(), "journalled": 0, "applied": n})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"journalled": n})
}

func (s *server) continuityStatus(w http.ResponseWriter, r *http.Request) {
	if s.phase3 == nil {
		writeJSON(w, http.StatusOK, contStatus{})
		return
	}
	writeJSON(w, http.StatusOK, s.phase3.contStatus())
}
