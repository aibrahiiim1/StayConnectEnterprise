package main

// Automatic reconnection for room sign-in (Product Owner, 2026-11-05). A device that room sign-in let in, and whose session the
// network then ended (its lease lapsed or was reissued, its address changed), is put back on the SAME entitlement without the
// room details when the portal sees it again. Nothing is granted: no quote, purchase, entitlement or device authorization is written;
// the session runs on the authorization the device already holds, so the device limit and the after-check-out rule are unchanged.
// The usage, allowance and window are the entitlement's and are not touched.
//
// Every condition below must hold, and each fails closed:
//   - DHCP says the address is leased to this MAC right now ("owned"; "unknown" is a refusal, never a reconnect on doubt);
//   - the device holds an open authorization on an ACTIVE, in-window ROOM entitlement whose stay may still sign in
//     (in house, or checked out while its After Check-out Access is valid);
//   - the device's last session belonged to that stay, ran on this same client network, and was ended by the network itself
//     (rejoinableEndReasons): never by the guest (LOGOUT), staff (ADMIN), a device release, a replacement or the package ending,
//     each of which needs a fresh sign-in;
//   - the device has no live session; the licence and the room sign-in switches allow a sign-in now.
//
// Residual limit: a MAC is not a person. A device that copies an authorized device's MAC on the same client network while that
// device is away could take its place; the conditions above bound it (a DHCP lease to that MAC, the same network, a network-ended
// last session, no live session of the real device), and its use is metered to that package as the real device's would be.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/onegate/onegate/data-plane/internal/authctx"
)

// rejoinableEndReasons are the end reasons a reconnection may follow: the network ended the session, nobody chose to.
var rejoinableEndReasons = []string{"ADDRESS_NO_LONGER_OWNED", "ADDRESS_CHANGED", "SUPERSEDED_ON_DEVICE_MOVE", "SUPERSEDED_ON_ADDRESS"}

// rejoinMethod labels the session so Client activity and the reports can tell an automatic reconnection from a sign-in.
const rejoinMethod = "REJOIN"

const (
	rejoinOutcomeRejoined    = "REJOINED"
	rejoinOutcomeNotEligible = "NOT_ELIGIBLE"
)

type rejoinReq struct {
	Device wireDevice `json:"device"`
}

type rejoinResp struct {
	Outcome   string `json:"outcome"`
	SessionID string `json:"session_id,omitempty"`
}

// rejoinCandidateSQL finds the entitlement a device may rejoin, locked; no row means it may not. $1 tenant, $2 site, $3 device,
// $4 the device's bridge now, $5 rejoinableEndReasons.
const rejoinCandidateSQL = `
	WITH last AS (
	  SELECT se.entitlement_id, se.end_reason, se.ended, se.ingress_interface
	    FROM iam_v2.sessions se
	   WHERE se.tenant_id = $1 AND se.site_id = $2 AND se.device_id = $3
	   ORDER BY se.started DESC, se.id DESC
	   LIMIT 1)
	SELECT e.id::text
	  FROM last
	  JOIN iam_v2.entitlements le ON le.id = last.entitlement_id AND le.tenant_id = $1 AND le.site_id = $2
	  JOIN iam_v2.entitlements e ON e.stay_id = le.stay_id AND e.tenant_id = $1 AND e.site_id = $2
	  JOIN iam_v2.stays st ON st.id = e.stay_id AND st.tenant_id = e.tenant_id AND st.site_id = e.site_id
	  JOIN iam_v2.entitlement_device_authorizations a
	    ON a.entitlement_id = e.id AND a.tenant_id = e.tenant_id AND a.device_id = $3 AND a.deauthorized_at IS NULL
	 WHERE last.ended IS NOT NULL AND last.end_reason = ANY($5::text[])
	   AND COALESCE(last.ingress_interface, '') = $4
	   AND e.stay_id IS NOT NULL
	   AND e.status = 'ACTIVE' AND (e.window_ends_at IS NULL OR e.window_ends_at > now())
	   AND (st.status IN ('IN_HOUSE','POST_STAY_ACTIVE') OR (st.status = 'CHECKED_OUT' AND ` + authctx.AfterCheckoutLive + `))
	 ORDER BY e.activated_at DESC NULLS LAST, e.id DESC
	 LIMIT 1
	 FOR UPDATE OF e`

// rejoinHandler answers REJOINED with the device's enforced session, or NOT_ELIGIBLE; the reason is for the log only.
func (p *phase3Auth) rejoinHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		p.rejoinRefused(w, "malformed_request")
		return
	}
	runRetried(w, r, func(w http.ResponseWriter, r *http.Request) { p.rejoinOnce(w, r, body) })
}

func (p *phase3Auth) rejoinRefused(w http.ResponseWriter, why string) {
	slog.Info("phase3 rejoin: not eligible", "reason", why)
	writeJSONScd(w, http.StatusOK, rejoinResp{Outcome: rejoinOutcomeNotEligible})
}

func (p *phase3Auth) rejoinOnce(w http.ResponseWriter, r *http.Request, body []byte) {
	var req rejoinReq
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		p.rejoinRefused(w, "malformed_request")
		return
	}
	ctx := r.Context()
	if _, refused := p.pmsLicenseRefusal(); refused {
		p.rejoinRefused(w, "license")
		return
	}
	methods, ok := p.srv.guestMethodSwitches(ctx)
	if !ok || !methodSwitchOn(methods, guestMethodPMS) {
		p.rejoinRefused(w, "room_sign_in_off")
		return
	}
	dev, err := p.device(ctx, req.Device)
	if err != nil {
		p.rejoinRefused(w, "device: "+err.Error())
		return
	}
	if !p.srv.networkOffers(ctx, dev.GuestNetwork, keyPMS) {
		p.rejoinRefused(w, "room_sign_in_not_offered_on_network")
		return
	}
	// Ownership is required, not merely "not foreign": a reconnection is never made on doubt.
	if st := p.srv.addressState(ctx, dev.IP, dev.MAC); st != addressOwned {
		p.rejoinRefused(w, "address_not_owned: "+st)
		return
	}
	var live bool
	if err := p.srv.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM iam_v2.sessions
		WHERE tenant_id=$1 AND site_id=$2 AND device_id=$3 AND ended IS NULL AND state IN ('active','PENDING_ENFORCEMENT'))`,
		p.srv.tenID, p.srv.siteID, dev.DeviceID).Scan(&live); err != nil || live {
		p.rejoinRefused(w, "live_session_or_unreadable")
		return
	}

	tx, err := p.srv.db.Begin(ctx)
	if err != nil {
		p.rejoinRefused(w, "begin")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ent string
	err = tx.QueryRow(ctx, rejoinCandidateSQL, p.srv.tenID, p.srv.siteID, dev.DeviceID, p.bridgeFor(ctx, dev.IP), rejoinableEndReasons).Scan(&ent)
	if err != nil {
		noteRetry(ctx, err)
		if errors.Is(err, pgx.ErrNoRows) {
			p.rejoinRefused(w, "no_rejoinable_entitlement")
		} else {
			p.rejoinRefused(w, "candidate: "+err.Error())
		}
		return
	}
	// Again under the entitlement lock: two reconnections of one device arriving together must not both open a session.
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM iam_v2.sessions
		WHERE tenant_id=$1 AND site_id=$2 AND device_id=$3 AND ended IS NULL AND state IN ('active','PENDING_ENFORCEMENT'))`,
		p.srv.tenID, p.srv.siteID, dev.DeviceID).Scan(&live); err != nil || live {
		noteRetry(ctx, err)
		p.rejoinRefused(w, "live_session_under_lock")
		return
	}
	// The authorization must still be open now, under the lock: a staff release racing this probe closes it without touching the
	// entitlement row. Nothing here may open one: authorize_entitlement_device would start a new interval and silently undo the
	// release, so a reconnection only ever uses the authorization the device already holds, and the session insert's own guard
	// refuses a device without one.
	var authorized bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM iam_v2.entitlement_device_authorizations
		WHERE tenant_id=$1 AND entitlement_id=$2::uuid AND device_id=$3::uuid AND deauthorized_at IS NULL)`,
		p.srv.tenID, ent, dev.DeviceID).Scan(&authorized); err != nil || !authorized {
		noteRetry(ctx, err)
		p.rejoinRefused(w, "authorization_closed")
		return
	}
	if err := reserveLicensedSlot(ctx, tx, p.srv.applID, p.srv.lic.MaxConcurrentOnlineGuests()); err != nil {
		noteRetry(ctx, err)
		p.rejoinRefused(w, "capacity: "+err.Error())
		return
	}
	sessionID, err := p.openSessionWithMethodTx(ctx, tx, ent, dev, rejoinMethod)
	if err != nil {
		noteRetry(ctx, err)
		p.rejoinRefused(w, "session: "+err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		noteRetry(ctx, err)
		p.rejoinRefused(w, "commit")
		return
	}
	if enforced, werr := p.awaitEnforced(ctx, sessionID); werr != nil || !enforced {
		// Committed: netd admits or ends it on its own; the portal shows the sign-in form meanwhile.
		p.rejoinRefused(w, "enforcement_not_confirmed")
		return
	}
	slog.Info("phase3 rejoin: device reconnected to its entitlement", "entitlement", ent, "session", sessionID)
	writeJSONScd(w, http.StatusOK, rejoinResp{Outcome: rejoinOutcomeRejoined, SessionID: sessionID})
}
