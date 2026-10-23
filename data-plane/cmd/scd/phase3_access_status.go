package main

// Answers the one question the portal cannot otherwise ask: did the access this device most recently held end because it ran
// out? Otherwise a finished package looks identical to a broken connection.
// It creates nothing (the Phase-3 device resolver INSERTs, so a probe from any device would mint durable identity; an unseen
// device gets "no notice"), returns no guest, room, stay, folio or PMS identity (only DATA, TIME or nothing), and changes no
// accounting or enforcement. It is a SELECT.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

type accessStatusReq struct {
	// Device is the only input, built by the portal from the appliance's own neighbour table; a guest can influence no entitlement, session, stay or room field.
	Device wireDevice `json:"device"`
}

type accessStatusResp struct {
	// EndedReason is "DATA", "TIME" or empty; nothing else is ever returned.
	EndedReason string `json:"ended_reason,omitempty"`
}

func (p *phase3Auth) accessStatusHandler(w http.ResponseWriter, r *http.Request) {
	var in accessStatusReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		// A malformed body gets the same "no notice" as everything else rather than an error surface.
		writeJSONScd(w, http.StatusOK, accessStatusResp{})
		return
	}
	reason, err := p.lastEndedReason(r.Context(), in.Device)
	if err != nil {
		// A failed lookup degrades to the ordinary sign-in page.
		slog.Info("phase3 access status unavailable", "err", err)
		writeJSONScd(w, http.StatusOK, accessStatusResp{})
		return
	}
	writeJSONScd(w, http.StatusOK, accessStatusResp{EndedReason: reason})
}

func (p *phase3Auth) lastEndedReason(ctx context.Context, d wireDevice) (string, error) {
	ip := net.ParseIP(strings.TrimSpace(d.IP))
	mac, err := net.ParseMAC(strings.TrimSpace(d.MAC))
	if ip == nil || ip.To4() == nil || err != nil {
		return "", nil
	}
	// Scoped to a mapped guest network like every Phase-3 path: a request from outside every guest network has no scope to answer in.
	if nc := p.srv.resolveNetwork(ctx, ip); nc.NetworkID == "" {
		return "", nil
	}

	var reason string
	// Read-only device resolution: an unknown device gets no notice, not a new row.
	err = p.srv.db.QueryRow(ctx, `
		WITH dev AS (
		  SELECT id FROM iam_v2.devices
		   WHERE tenant_id=$1 AND site_id=$2 AND appliance_id=$3 AND mac=$4::macaddr
		)
		SELECT COALESCE(e.terminal_reason,'')
		  FROM iam_v2.entitlements e
		  JOIN iam_v2.sessions s
		    ON s.entitlement_id = e.id AND s.device_id = (SELECT id FROM dev)
		 WHERE e.tenant_id=$1 AND e.site_id=$2
		   AND e.status = 'TERMINATED'
		   AND e.terminal_reason IN ('DATA','TIME')
		   -- ONCE THEY ARE BACK ON, THE NOTICE IS OVER. A device holding live access is not a device whose
		   -- access ended, so a new grant clears the message by making this row unselectable rather than by
		   -- anyone remembering to dismiss it.
		   AND NOT EXISTS (
		         SELECT 1 FROM iam_v2.entitlements live
		           JOIN iam_v2.sessions ls ON ls.entitlement_id = live.id
		          WHERE ls.device_id = (SELECT id FROM dev)
		            AND live.status IN ('PENDING','ACTIVE','SUSPENDED'))
		 ORDER BY e.terminated_at DESC NULLS LAST
		 LIMIT 1`,
		p.srv.tenID, p.srv.siteID, p.srv.applID, mac.String()).Scan(&reason)
	if err != nil {
		// No row is the ordinary answer for a device that has never run out, and is not an error.
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return reason, nil
}
