package main

// Session-after-grant for the IAM-v2 commerce path: an entitlement is not access. The session opens as PENDING_ENFORCEMENT, never active,
// because nothing has been enforced yet; netd promotes it through iam_v2.activate_session_enforcement once the tc class and nft gate are live.

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
	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

type activateReq struct {
	EntitlementID string `json:"entitlement_id"`
	DeviceID      string `json:"device_id"`
	IP            string `json:"ip"`
	MAC           string `json:"mac"`
	// AuthContextID is this device's own fresh sign-in; it lets a device other than the purchaser join (see Joining below).
	AuthContextID string `json:"auth_context_id,omitempty"`
}

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

	// The Email accounts settings decide whether an unverified account past its deadline is suspended; an unreadable read enforces the strictest defaults.
	selfService := s.selfServiceSettingsOrDefault(ctx)

	var sessionID string
	var reused bool
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// The entitlement must be ours, live and in its window, checked inside the transaction so a racing revocation cannot slip a session in.
		// maxDevices NULL = no device limit; devicePolicy DISCONNECT_OLDEST admits over the limit by disconnecting the oldest device.
		var maxDevices *int
		var status, devicePolicy string
		if err := tx.QueryRow(ctx, `
		    SELECT e.status, spr.max_concurrent_devices,
		           COALESCE(spr.device_limit_policy, 'REJECT_NEW_DEVICE')
		      FROM iam_v2.entitlements e
		      LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
		     WHERE e.id=$1 AND e.tenant_id=$2 AND e.site_id=$3
		       AND (e.window_ends_at IS NULL OR e.window_ends_at > now())
		     FOR UPDATE OF e`, req.EntitlementID, s.tenID, s.siteID).Scan(&status, &maxDevices, &devicePolicy); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errEntitlementUnusable
			}
			return err
		}
		if status != "ACTIVE" {
			return errEntitlementUnusable
		}
		// A suspended Email account gets no session on any device; checked in the transaction before the reconnect shortcut so a returning device is refused like a new one.
		if suspended, err := s.entitlementAccountSuspended(ctx, tx, req.EntitlementID, selfService); err != nil {
			return err
		} else if suspended {
			return errVerificationRequired
		}

		// The device must be the one that acquired this entitlement (entitlement -> purchase -> auth_context -> device), never trusted
		// from the request body: an arbitrary device_id with a real entitlement id once consumed a slot on someone else's grant.
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
		// Joining: a device other than the purchaser may take a slot only on its OWN unconsumed, unexpired sign-in for the same subject on this
		// network; that context is spent here so one sign-in admits one device once. The body's device id alone admits nothing.
		if acquiringDevice != req.DeviceID {
			if req.AuthContextID == "" {
				return errDeviceNotOnEntitlement
			}
			// auth_context writes require the controlled operation (a database trigger refuses anything else).
			if err := writerguard.Open(ctx, tx, writerguard.CapAuthContext); err != nil {
				return err
			}
			var joined string
			err := tx.QueryRow(ctx, `
			    UPDATE iam_v2.auth_contexts ac SET consumed_at = now()
			      FROM iam_v2.entitlements e
			     WHERE ac.id = $1 AND e.id = $2
			       AND ac.tenant_id = $3 AND ac.site_id = $4
			       AND e.tenant_id = ac.tenant_id AND e.site_id = ac.site_id
			       AND ac.device_id = $5 AND ac.guest_network_id = $6
			       AND ac.consumed_at IS NULL AND ac.expires_at > now()
			       AND (e.voucher_id = ac.voucher_id OR e.guest_account_id = ac.guest_account_id
			            OR e.guest_principal_id = ac.guest_principal_id
			            OR e.anonymous_subject_id = ac.anonymous_subject_id
			            -- A room guest's verified sign-in for the same stay: the device that replaced another
			            -- on the stay's access (client_device_action REPLACE) joins it like any other client's
			            -- device (contract ONEGATE_DEVICE_MANAGEMENT §3, audit H-6).
			            OR e.stay_id = ac.stay_id)
			    RETURNING ac.id::text`,
				req.AuthContextID, req.EntitlementID, s.tenID, s.siteID, req.DeviceID, nc.NetworkID).Scan(&joined)
			if errors.Is(err, pgx.ErrNoRows) {
				return errDeviceNotOnEntitlement
			}
			if err != nil {
				return err
			}
		}

		// The rest of the identity must cohere: device identity is (tenant, site, appliance, MAC) and the session is enforced on the bridge
		// for the source IP, so MAC and network must match the device's records. The IP is not pinned: DHCP reassigns it within a network.
		var deviceMAC, deviceTenant, deviceSite string
		if err := tx.QueryRow(ctx, `
		    SELECT mac::text, tenant_id::text, site_id::text
		      FROM iam_v2.devices WHERE id = $1`, req.DeviceID).Scan(&deviceMAC, &deviceTenant, &deviceSite); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errDeviceNotOnEntitlement
			}
			return err
		}
		// Tenant/site come from this appliance's config, never the request.
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

		// Reconnect/idempotency: the same device at the same address gets its existing session, so a retried request does not consume another
		// slot. At a NEW address (audit B-19) the old session still authorizes the old address, so end it through the controlled writer and
		// open a fresh one; pinned columns are accounting identity and are not rewritten in place.
		stale, err := s.liveSessionsOfDevice(ctx, tx, req.EntitlementID, req.DeviceID)
		if err != nil {
			return err
		}
		for _, ls := range stale {
			if sessionID == "" && ls.ip == ip.String() && ls.iface == nc.Bridge {
				sessionID = ls.id
				reused = true
				continue
			}
			if _, err := s.endSessionControlled(ctx, tx, ls.id, endReasonAddressChanged); err != nil {
				return err
			}
		}
		if reused {
			return nil
		}

		// Device limit against the PINNED plan revision, counted in the inserting transaction so two simultaneous logins cannot both see one slot left.
		var inUse int
		if err := tx.QueryRow(ctx, `
		    SELECT count(DISTINCT device_id) FROM iam_v2.sessions
		     WHERE entitlement_id=$1 AND ended IS NULL AND state IN ('active','PENDING_ENFORCEMENT')`,
			req.EntitlementID).Scan(&inUse); err != nil {
			return err
		}
		// DISCONNECT_OLDEST admits the new device by disconnecting the oldest inside authorize_entitlement_device under the entitlement lock; other policies refuse at the limit.
		if maxDevices != nil && inUse >= *maxDevices && devicePolicy != "DISCONNECT_OLDEST" {
			return &maxDevicesError{Limit: *maxDevices, Current: inUse}
		}

		// Device admission: the database refuses a session for a device with no authorization binding, so admission is its own step.
		if _, err := tx.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`,
			req.EntitlementID, req.DeviceID); err != nil {
			// The database limit is authoritative: an admitted device keeps its slot after signing out, which svc_scd cannot see; answer it as the device limit.
			if strings.Contains(err.Error(), "MAX_DEVICES_REACHED") {
				limit := 0
				if maxDevices != nil {
					limit = *maxDevices
				}
				return &maxDevicesError{Limit: limit, Current: limit}
			}
			return err
		}

		// credential_method records how this device signed in: its own sign-in when it joined with one, else the sign-in that acquired the entitlement, else the authority name.
		var method string
		if err := tx.QueryRow(ctx, `
		    SELECT COALESCE(
		             (SELECT NULLIF(ac.credential,'') FROM iam_v2.auth_contexts ac WHERE ac.id = NULLIF($2,'')::uuid),
		             (SELECT NULLIF(ac.credential,'') FROM iam_v2.purchases p
		                JOIN iam_v2.auth_contexts ac ON ac.id = p.auth_context_id
		               WHERE p.id = e.purchase_id),
		             CASE WHEN e.voucher_id IS NOT NULL THEN 'VOUCHER'
		                  WHEN e.guest_account_id IS NOT NULL THEN 'ACCOUNT'
		                  WHEN e.anonymous_subject_id IS NOT NULL THEN 'OPEN'
		                  ELSE 'PRINCIPAL' END)
		      FROM iam_v2.entitlements e WHERE e.id=$1`, req.EntitlementID, req.AuthContextID).Scan(&method); err != nil {
			return err
		}
		// The appliance-wide admission lock is taken as late as possible and held only for the count and the insert: every sign-in on the
		// appliance queues behind it, so the statements above must not run while it is held (capacity acceptance, 2026-10-30).
		if err := reserveLicensedSlot(ctx, tx, s.applID, s.lic.MaxConcurrentOnlineGuests()); err != nil {
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
		if errors.Is(err, errVerificationRequired) {
			// Exactly this body: portald matches it to show the verify / resend / one-more-hour page.
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "VERIFICATION_REQUIRED"})
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

	// Wait for enforcement before answering: the row is durable but the guest is not online until netd converges, and a connected page for
	// dropped packets is a lie. Report the state actually reached; the wait is bounded by the caller deadline.
	state := s.awaitIAMv2Enforcement(ctx, sessionID)
	if !reused && state == "active" {
		// SessionsStarted counts only sessions that actually started.
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

// endReasonAddressChanged ends a session when the device returns at a different address or bridge (audit B-19).
const endReasonAddressChanged = "ADDRESS_CHANGED"

type liveDeviceSession struct{ id, ip, iface string }

// liveSessionsOfDevice runs inside the activation transaction, which holds the entitlement row lock.
func (s *server) liveSessionsOfDevice(ctx context.Context, tx pgx.Tx, entitlementID, deviceID string) ([]liveDeviceSession, error) {
	rows, err := tx.Query(ctx, `
	    SELECT id::text, COALESCE(host(ip), ''), COALESCE(ingress_interface, '')
	      FROM iam_v2.sessions
	     WHERE entitlement_id=$1 AND device_id=$2 AND ended IS NULL
	       AND state IN ('active','PENDING_ENFORCEMENT')
	     ORDER BY started DESC`, entitlementID, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []liveDeviceSession
	for rows.Next() {
		var l liveDeviceSession
		if err := rows.Scan(&l.id, &l.ip, &l.iface); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

var errEntitlementUnusable = errors.New("entitlement not usable")

// errVerificationRequired: the subject is an Email account suspended for Internet (unverified past its deadline, no grace running).
var errVerificationRequired = errors.New("the account's email address must be verified")

var errDeviceNotOnEntitlement = errors.New("device did not acquire this entitlement")

var errIdentityMismatch = errors.New("device identity does not match the presented network identity")

type maxDevicesError struct{ Limit, Current int }

func (e *maxDevicesError) Error() string { return "max devices reached" }

var _ = context.Background

// awaitIAMv2Enforcement polls durable session state until netd has promoted the session or the caller budget runs out, returning the
// state actually observed; the database is the only place that can answer whether promotion committed.
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

// licenseCapacityError: admitting this session would pass the licensed concurrent-guest limit. Distinct from maxDevicesError, since plan device limit and property capacity lead to different actions.
type licenseCapacityError struct {
	Limit   int64
	Current int64
}

func (e *licenseCapacityError) Error() string { return "licensed concurrent-guest capacity reached" }
