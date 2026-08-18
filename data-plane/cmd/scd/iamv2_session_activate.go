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
		var dev *maxDevicesError
		if errors.As(err, &dev) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "MAX_DEVICES_REACHED", "limit": dev.Limit, "current": dev.Current,
				"authority": "iam_v2",
			})
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

	// The session is durable. It is NOT yet enforced, and saying otherwise is the whole failure this design
	// avoids, so the response reports the state honestly and the caller decides whether to wait.
	if !reused {
		s.met.SessionsStarted.WithLabelValues("iamv2").Inc()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"state":      "PENDING_ENFORCEMENT",
		"reused":     reused,
		"authority":  "iam_v2",
		"bridge":     nc.Bridge,
	})
}

var errEntitlementUnusable = errors.New("entitlement not usable")

type maxDevicesError struct{ Limit, Current int }

func (e *maxDevicesError) Error() string { return "max devices reached" }

var _ = context.Background
