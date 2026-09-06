package main

// WHY A GUEST'S INTERNET STOPPED, IN THE ONE PLACE THEY WILL LOOK.
//
// When an Entitlement runs out of data or time, the enforcement plane ends the Session and drops the device's
// authorization, and the standing captive rules put the device straight back in front of the portal. That
// part works. What the guest sees when they get there is the ordinary sign-in page, identical to the one they
// saw when they first connected — so a package that ended looks exactly like an internet connection that
// broke, and the front desk hears about it either way.
//
// This answers the one question the portal cannot otherwise ask: did the access this device most recently
// held end because it ran out?
//
// WHAT IT DELIBERATELY DOES NOT DO:
//
//   * It creates nothing. The Phase-3 device resolver INSERTs a device row, which is right for an
//     authentication attempt and wrong for a page load — a probe from any device on the guest network would
//     mint durable identity. This resolves read-only and answers "no notice" for a device it has never seen.
//   * It returns no guest, room, reservation, stay, folio or PMS identity. The answer is one of three
//     bounded words: DATA, TIME, or nothing.
//   * It changes no accounting, termination, teardown or enforcement. It is a SELECT.

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
	// Device is the ONLY input, and the portal builds it from the appliance's own neighbour table. No
	// entitlement, session, stay or room field exists here for a guest to influence.
	Device wireDevice `json:"device"`
}

type accessStatusResp struct {
	// EndedReason is "DATA", "TIME" or empty. Nothing else is ever returned — not the entitlement, not when
	// it ended, not how much was used. The portal needs to pick one sentence; that is all this decides.
	EndedReason string `json:"ended_reason,omitempty"`
}

// accessStatusHandler serves POST /v1/phase3/access/status.
func (p *phase3Auth) accessStatusHandler(w http.ResponseWriter, r *http.Request) {
	var in accessStatusReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		// A malformed body is not something a guest can act on, and there is nothing to disclose. Answer the
		// same "no notice" as everything else rather than inventing an error surface.
		writeJSONScd(w, http.StatusOK, accessStatusResp{})
		return
	}
	reason, err := p.lastEndedReason(r.Context(), in.Device)
	if err != nil {
		// The portal must still render. A status lookup that fails degrades to the ordinary sign-in page,
		// which is exactly what the guest saw before this existed.
		slog.Info("phase3 access status unavailable", "err", err)
		writeJSONScd(w, http.StatusOK, accessStatusResp{})
		return
	}
	writeJSONScd(w, http.StatusOK, accessStatusResp{EndedReason: reason})
}

// lastEndedReason returns DATA, TIME or "" for the device the portal describes.
func (p *phase3Auth) lastEndedReason(ctx context.Context, d wireDevice) (string, error) {
	ip := net.ParseIP(strings.TrimSpace(d.IP))
	mac, err := net.ParseMAC(strings.TrimSpace(d.MAC))
	if ip == nil || ip.To4() == nil || err != nil {
		return "", nil
	}
	// Scoped to a mapped guest network for the same reason every other Phase-3 path is: a request from
	// outside every guest network has no scope to answer in.
	if nc := p.srv.resolveNetwork(ctx, ip); nc.NetworkID == "" {
		return "", nil
	}

	var reason string
	// READ-ONLY device resolution. No ON CONFLICT INSERT: an unknown device gets no notice, not a new row.
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
