package main

// SESSION-AFTER-GRANT for the IAM-v2 commerce path.
//
// Confirming a free purchase produces an ENTITLEMENT -- a durable statement that this subject may have
// access. It does not produce access. The session is what the enforcement plane acts on, and until one
// exists the guest has bought something and is still offline.
//
// This is the step that was missing: authentication landed in iam_v2, commerce landed in iam_v2, and then
// nothing turned the entitlement into a session, so iam_v2.sessions stayed at its pre-existing count while
// every other IAM-v2 table moved.
//
// WHAT IT DOES NOT DO
// -------------------
// It does not write 'active'. It opens the session as PENDING_ENFORCEMENT, exactly as the accepted Phase-3
// grant path does, because at this instant nothing has been enforced: no accountable tc class exists and the
// guest is not authorized at the packet gate. Writing 'active' here would be a false statement about the
// kernel, and it is the statement every other component trusts. netd -- the only process that can see
// whether enforcement actually took -- promotes it through iam_v2.activate_session_enforcement once the
// class is classifying and the nft gate is authorizing. acctd's plan picks the session up on its own cadence
// because that plan is generic over iam_v2.sessions joined to an ACTIVE entitlement; nothing here needs to
// be special-cased for VOUCHER or ACCOUNT.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type activateReq struct {
	EntitlementID string `json:"entitlement_id"`
	DeviceID      string `json:"device_id"`
	IP            string `json:"ip"`
	MAC           string `json:"mac"`
}

// activateIAMv2Session turns an entitlement into a session, idempotently.
func (s *server) activateIAMv2Session(w http.ResponseWriter, r *http.Request) {
	var req activateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if req.EntitlementID == "" || req.DeviceID == "" {
		httpErr(w, http.StatusBadRequest, "entitlement_id and device_id are required")
		return
	}
	ip := net.ParseIP(req.IP)
	if ip == nil || ip.To4() == nil {
		httpErr(w, http.StatusBadRequest, "bad ip")
		return
	}
	mac, err := net.ParseMAC(req.MAC)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "bad mac")
		return
	}
	if !s.licenseGate(w, "") {
		return
	}
	ctx := r.Context()
	nc := s.resolveNetwork(ctx, ip)
	if nc.NetworkID == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "NO_GUEST_NETWORK", "authority": "iam_v2"})
		return
	}

	var sessionID string
	var reused bool
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// The entitlement must be ours, live, and still within its window. Checked inside the transaction so
		// a revocation racing this activation cannot slip a session in behind it.
		var maxDevices int
		var status string
		if err := tx.QueryRow(ctx, `
		    SELECT e.status, COALESCE(spr.max_concurrent_devices, 1)
		      FROM iam_v2.entitlements e
		      LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
		     WHERE e.id=$1 AND e.tenant_id=$2 AND e.site_id=$3
		       AND (e.window_ends_at IS NULL OR e.window_ends_at > now())
		     FOR UPDATE OF e`, req.EntitlementID, s.tenID, s.siteID).Scan(&status, &maxDevices); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errEntitlementUnusable
			}
			return err
		}
		if status != "ACTIVE" {
			return errEntitlementUnusable
		}

		// THE DEVICE MUST BE THE ONE THAT ACQUIRED THIS ENTITLEMENT.
		//
		// Found by adversarial probe, not by review: passing an arbitrary device_id with a real entitlement id
		// activated a session and consumed a device slot on someone else's grant. The endpoint trusted the
		// device from the request body, while the accepted Phase-3 grant path derives device identity from the
		// connection and says explicitly that it is "never from the body".
		//
		// The socket is root-only and portald passes the device from its server-side commerce session, so the
		// browser could not reach this. That is an argument for the caller being careful, not for the boundary
		// being absent -- an entitlement is a grant to a subject on the device that acquired it, and any
		// future caller getting this wrong should be refused rather than trusted.
		//
		// entitlement -> purchase -> auth_context -> device is the chain that records who actually acquired it.
		var acquiringDevice string
		if err := tx.QueryRow(ctx, `
		    SELECT ac.device_id::text
		      FROM iam_v2.entitlements e
		      JOIN iam_v2.purchases p     ON p.id  = e.purchase_id
		      JOIN iam_v2.auth_contexts ac ON ac.id = p.auth_context_id
		     WHERE e.id = $1`, req.EntitlementID).Scan(&acquiringDevice); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// No recorded acquisition chain: refuse rather than guess which device may join.
				return errDeviceNotOnEntitlement
			}
			return err
		}
		if acquiringDevice != req.DeviceID {
			return errDeviceNotOnEntitlement
		}

		// ...AND THE REST OF THE IDENTITY MUST COHERE.
		//
		// Binding the device alone still let a caller present that device with somebody else's MAC, or on a
		// guest network it was never seen on. Each of those is a different lie about who is being admitted:
		//
		//   MAC     -- device identity IS (tenant, site, appliance, MAC) in this domain, so a device row and a
		//              MAC that disagree are not the same device, whatever the id says.
		//   network -- the session is enforced on the bridge derived from the source IP. Admitting a device
		//              onto a network it never appeared on would enforce it somewhere its appearance record
		//              cannot account for.
		//
		// The IP is deliberately NOT pinned to the appearance record: DHCP legitimately reassigns it within a
		// network, and the source IP is already server-derived from the connection rather than the body. The
		// network it resolves to is the invariant that matters.
		var deviceMAC, deviceTenant, deviceSite string
		if err := tx.QueryRow(ctx, `
		    SELECT mac::text, tenant_id::text, site_id::text
		      FROM iam_v2.devices WHERE id = $1`, req.DeviceID).Scan(&deviceMAC, &deviceTenant, &deviceSite); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errDeviceNotOnEntitlement
			}
			return err
		}
		// Tenant/site come from this appliance's own config, never the request, so a device belonging to
		// another owner cannot be activated here even with a valid id.
		if deviceTenant != s.tenID || deviceSite != s.siteID {
			return errDeviceNotOnEntitlement
		}
		if !strings.EqualFold(deviceMAC, mac.String()) {
			return errIdentityMismatch
		}
		var seenOnNetwork bool
		if err := tx.QueryRow(ctx, `
		    SELECT EXISTS(SELECT 1 FROM iam_v2.device_network_appearances
		                   WHERE device_id = $1 AND guest_network_id = $2)`,
			req.DeviceID, nc.NetworkID).Scan(&seenOnNetwork); err != nil {
			return err
		}
		if !seenOnNetwork {
			return errIdentityMismatch
		}

		// RECONNECT / IDEMPOTENCY. The same device returning to the same entitlement gets the session it
		// already has rather than a second one. Without this, a retried request -- a lost response, a guest
		// reloading the page -- would consume another device slot against its own limit.
		if err := tx.QueryRow(ctx, `
		    SELECT id::text FROM iam_v2.sessions
		     WHERE entitlement_id=$1 AND device_id=$2 AND ended IS NULL
		       AND state IN ('active','PENDING_ENFORCEMENT')
		     LIMIT 1`, req.EntitlementID, req.DeviceID).Scan(&sessionID); err == nil {
			reused = true
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		// LICENSED CONCURRENT-GUEST CAP. Scope, serialization and rationale live with the function.
		if err := reserveLicensedSlot(ctx, tx, s.applID, s.lic.MaxConcurrentOnlineGuests()); err != nil {
			return err
		}

		// DEVICE LIMIT, enforced against the PINNED plan revision, counted inside the same transaction that
		// inserts. Counting outside it is the classic way two simultaneous logins both see "one slot left".
		var inUse int
		if err := tx.QueryRow(ctx, `
		    SELECT count(DISTINCT device_id) FROM iam_v2.sessions
		     WHERE entitlement_id=$1 AND ended IS NULL AND state IN ('active','PENDING_ENFORCEMENT')`,
			req.EntitlementID).Scan(&inUse); err != nil {
			return err
		}
		if inUse >= maxDevices {
			return &maxDevicesError{Limit: maxDevices, Current: inUse}
		}

		// DEVICE ADMISSION. A session's device must first hold an authorization binding on the entitlement --
		// the database refuses the insert otherwise ("session for a device with no authorization binding on
		// entitlement ..."), which is the accepted domain making the ordering explicit rather than trusting
		// callers to remember it. Admission is its own step precisely so the set of devices an entitlement has
		// admitted survives sessions coming and going.
		if _, err := tx.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`,
			req.EntitlementID, req.DeviceID); err != nil {
			return err
		}

		// credential_method records which authority admitted this device, so an operator reading the session
		// can tell a voucher guest from an account guest without joining back through the entitlement.
		var method string
		if err := tx.QueryRow(ctx, `
		    SELECT CASE WHEN voucher_id IS NOT NULL THEN 'VOUCHER'
		                WHEN guest_account_id IS NOT NULL THEN 'ACCOUNT'
		                ELSE 'PRINCIPAL' END
		      FROM iam_v2.entitlements WHERE id=$1`, req.EntitlementID).Scan(&method); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
		    INSERT INTO iam_v2.sessions
		      (tenant_id, site_id, entitlement_id, device_id, credential_method, state, started,
		       ip, mac, ingress_interface)
		    VALUES ($1,$2,$3,$4,$5,'PENDING_ENFORCEMENT',now(),$6::inet,$7::macaddr,$8)
		 RETURNING id::text`,
			s.tenID, s.siteID, req.EntitlementID, req.DeviceID, method,
			ip.String(), mac.String(), nc.Bridge).Scan(&sessionID)
	})
	if err != nil {
		var cap *licenseCapacityError
		if errors.As(err, &cap) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "LICENSE_CAPACITY_REACHED", "limit": cap.Limit, "current": cap.Current,
				"authority": "iam_v2",
			})
			return
		}
		var dev *maxDevicesError
		if errors.As(err, &dev) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "MAX_DEVICES_REACHED", "limit": dev.Limit, "current": dev.Current,
				"authority": "iam_v2",
			})
			return
		}
		if errors.Is(err, errIdentityMismatch) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "DEVICE_IDENTITY_MISMATCH", "authority": "iam_v2"})
			return
		}
		if errors.Is(err, errDeviceNotOnEntitlement) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "DEVICE_NOT_ON_ENTITLEMENT", "authority": "iam_v2"})
			return
		}
		if errors.Is(err, errEntitlementUnusable) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "ENTITLEMENT_UNUSABLE", "authority": "iam_v2"})
			return
		}
		slog.Error("iamv2 session activation failed", "err", err)
		httpErr(w, http.StatusInternalServerError, "activation failed")
		return
	}

	// WAIT FOR ENFORCEMENT BEFORE ANSWERING.
	//
	// The session row is durable at this point, but the guest is not online: no accountable tc class exists
	// and nft has not authorized them yet. netd converges on its own cadence. Returning here and letting the
	// portal show "connected" would reproduce, one layer up, the exact defect the portal seam just fixed --
	// a success page for a guest whose packets are still being dropped.
	//
	// So the caller is told the truth: the state actually reached. The wait is bounded by the caller's own
	// deadline, because a wait longer than the caller's budget is not a longer wait -- the context is already
	// cancelled and it ends immediately, on precisely the requests it was meant to protect.
	state := s.awaitIAMv2Enforcement(ctx, sessionID)
	if !reused && state == "active" {
		// SessionsStarted counts sessions that actually started. Incrementing on a row that never got enforced
		// would put the same lie in the metrics that it would have put on the success page.
		s.met.SessionsStarted.WithLabelValues("iamv2").Inc()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"state":      state,
		"enforced":   state == "active",
		"reused":     reused,
		"authority":  "iam_v2",
		"bridge":     nc.Bridge,
	})
}

var errEntitlementUnusable = errors.New("entitlement not usable")

// errDeviceNotOnEntitlement is returned when the device did not acquire the entitlement it is activating.
var errDeviceNotOnEntitlement = errors.New("device did not acquire this entitlement")

// errIdentityMismatch is returned when the device exists and acquired the entitlement, but the presented
// MAC or guest network does not match what that device actually is or where it has been seen.
var errIdentityMismatch = errors.New("device identity does not match the presented network identity")

type maxDevicesError struct{ Limit, Current int }

func (e *maxDevicesError) Error() string { return "max devices reached" }

var _ = context.Background

// awaitIAMv2Enforcement polls durable session state until netd has promoted the session, or the caller's
// budget runs out. It returns the state actually observed -- never an optimistic one.
//
// Polling durable state rather than trusting a signal is deliberate: netd promotes through a controlled
// writer, and the database is the only place that can answer whether the promotion actually committed.
func (s *server) awaitIAMv2Enforcement(ctx context.Context, sessionID string) string {
	deadline := time.Now().Add(iamv2EnforcementWaitMax)
	if d, ok := ctx.Deadline(); ok {
		if reserved := d.Add(-iamv2EnforcementReserve); reserved.Before(deadline) {
			deadline = reserved
		}
	}
	state := "PENDING_ENFORCEMENT"
	for {
		var st string
		var ended *time.Time
		if err := s.db.QueryRow(ctx,
			`SELECT state, ended FROM iam_v2.sessions WHERE id=$1`, sessionID).Scan(&st, &ended); err == nil {
			state = st
			// A session that ended while we waited must not be reported as progressing toward active.
			if st == "active" || ended != nil {
				return st
			}
		}
		if !time.Now().Add(iamv2EnforcementPoll).Before(deadline) {
			return state
		}
		select {
		case <-ctx.Done():
			return state
		case <-time.After(iamv2EnforcementPoll):
		}
	}
}

const (
	// Bounded so nothing can pin a request forever when there is no caller deadline to derive from.
	iamv2EnforcementWaitMax = 8 * time.Second
	// Answer before the caller stops listening, rather than at the same instant.
	iamv2EnforcementReserve = 150 * time.Millisecond
	// Short relative to the enforcement producer's one-second cadence.
	iamv2EnforcementPoll = 100 * time.Millisecond
)

// licenseCapacityError reports that admitting this session would take the site past the concurrent-guest
// limit in its local signed licence. It carries the limit and the current count so the portal can say which
// wall the guest hit, and it is deliberately distinct from maxDevicesError: "your plan allows fewer devices"
// and "this property is at its licensed capacity" are different facts and lead to different actions.
type licenseCapacityError struct {
	Limit   int64
	Current int64
}

func (e *licenseCapacityError) Error() string { return "licensed concurrent-guest capacity reached" }
