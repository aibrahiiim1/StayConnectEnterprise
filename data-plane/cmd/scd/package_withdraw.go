package main

// The kernel half of staff End Package (migration 0202): the database has already ended the entitlement and its sessions; this
// withdraws each ended session's address from the gate at once instead of waiting for enforcement to converge. It acts only on a
// session of this site that IS ended, and never on an address another live session now holds, so it cannot touch another guest.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"
)

type withdrawReq struct {
	Sessions []string `json:"sessions"`
	// Addresses: an erasure clears an ended session's address in the same transaction, so the caller sends what it read before.
	// Used only for an ended session whose own row holds no address any more.
	Addresses map[string]string `json:"addresses,omitempty"`
}

type withdrawResult struct {
	Session string `json:"session"`
	State   string `json:"state"` // withdrawn | no_address | address_reused | not_ended | failed
	Detail  string `json:"detail,omitempty"`
}

func (s *server) withdrawEndedSessions(w http.ResponseWriter, r *http.Request) {
	var in withdrawReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&in); err != nil || len(in.Sessions) == 0 || len(in.Sessions) > 500 {
		httpErr(w, http.StatusBadRequest, "between 1 and 500 session ids are required")
		return
	}
	if s.db == nil {
		httpErr(w, http.StatusServiceUnavailable, "the database is not available")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
	defer cancel()
	out := make([]withdrawResult, 0, len(in.Sessions))
	for _, id := range in.Sessions {
		res := withdrawResult{Session: id}
		var ip *string
		var state string
		var reused bool
		err := s.db.QueryRow(ctx, `
			SELECT host(s.ip), s.state,
			       EXISTS (SELECT 1 FROM iam_v2.sessions o
			                WHERE o.tenant_id = s.tenant_id AND o.site_id = s.site_id AND o.id <> s.id AND o.ip = s.ip
			                  AND o.ended IS NULL AND o.state IN ('active','PENDING_ENFORCEMENT'))
			  FROM iam_v2.sessions s
			 WHERE s.id = $1::uuid AND s.tenant_id = $2 AND s.site_id = $3`, id, s.tenID, s.siteID).Scan(&ip, &state, &reused)
		switch {
		case err != nil:
			res.State, res.Detail = "failed", "the session could not be read"
		case state != "ended":
			res.State = "not_ended"
		case (ip == nil || *ip == "") && in.Addresses[id] == "":
			res.State = "no_address"
		case ip == nil || *ip == "":
			// The row was erased: the sent address is withdrawn only if no live session of this site holds it now.
			given := in.Addresses[id]
			if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM iam_v2.sessions o
				WHERE o.tenant_id = $2 AND o.site_id = $3 AND o.ip = $1::inet AND o.ended IS NULL AND o.state IN ('active','PENDING_ENFORCEMENT'))`,
				given, s.tenID, s.siteID).Scan(&reused); err != nil {
				res.State, res.Detail = "failed", "the address could not be checked"
				break
			}
			res = s.withdrawAddress(ctx, res, given, reused)
		default:
			res = s.withdrawAddress(ctx, res, *ip, reused)
		}
		out = append(out, res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

func (s *server) withdrawAddress(ctx context.Context, res withdrawResult, ip string, reused bool) withdrawResult {
	addr := net.ParseIP(ip)
	switch {
	case reused:
		// Another live session holds the address now: withdrawing it would cut off that guest.
		res.State = "address_reused"
	case addr == nil:
		res.State = "no_address"
	default:
		if gerr := s.denyAtGateChecked(ctx, addr); gerr != nil {
			res.State, res.Detail = "failed", gerr.Error()
		} else {
			res.State = "withdrawn"
		}
	}
	return res
}
