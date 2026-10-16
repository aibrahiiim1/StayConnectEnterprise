package main

// MANAGING ONE CLIENT ACCOUNT: what it holds, and what staff may do to it.
//
// THE DEFECT THIS CLOSES. Delete was a bare DELETE. An account that had ever signed in is referenced by its
// auth contexts (a foreign key with no ON DELETE action), so the delete failed and reached the operator as
// "account not found"; an account a client had registered with their email could then never be cleared, and the
// same address could never register again. Had the reference not been there, the entitlements' ON DELETE
// CASCADE would have erased the package history instead -- the opposite failure.
//
// So removal has two outcomes, and the operator is told which one happened:
//
//   DELETED  the account was never used (no auth context, no entitlement): the row goes.
//   REMOVED  the account has history: its devices are disconnected, it is disabled, and its username is moved to
//            "<username>.removed-<id8>.invalid". The address (or username) is free again at once -- the per-site
//            uniqueness compares usernames -- while every purchase, entitlement and session row keeps pointing
//            at the account it belonged to. ".invalid" is the RFC 2606 reserved TLD, so no mail can ever be
//            sent to a removed account's username, and validUsername refuses it for any live account.
//
// Mark verified and New verification period adjust a self-service account's verification state (contract
// ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE §3). Neither touches an entitlement: verification only gates Internet.
//
// Releasing a device's place on a package is the Devices surface's (client_devices_admin.go, migration 0118):
// each device listed here carries its id for it.

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const removedUsernameTLD = ".invalid"

// removedUsername is where a removed account's username moves: unique by the account id, recognisably the old
// name, undeliverable as an address, and still a valid self-service username (lowercase, contains '@').
func removedUsername(username, id string) string {
	short := strings.ReplaceAll(id, "-", "")
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("%s.removed-%s%s", strings.ToLower(strings.TrimSpace(username)), short, removedUsernameTLD)
}

// isRemovedUsername reports whether a username carries the removal marker.
func isRemovedUsername(u string) bool {
	u = strings.ToLower(u)
	return strings.HasSuffix(u, removedUsernameTLD) && strings.Contains(u, ".removed-")
}

// sqlRemoved is the same test in SQL, on alias a.
const sqlRemoved = `(NOT a.enabled AND lower(a.username) LIKE '%.removed-%.invalid')`

type manageDevice struct {
	ID              string     `json:"id"`
	MAC             string     `json:"mac"`
	Status          string     `json:"status"`
	Online          bool       `json:"online"`
	FirstAuthorized *time.Time `json:"first_authorized,omitempty"`
	LastAuthorized  *time.Time `json:"last_authorized,omitempty"`
}

type manageEntitlement struct {
	ID             string         `json:"id"`
	Status         string         `json:"status"`
	TerminalReason *string        `json:"terminal_reason,omitempty"`
	PackageName    string         `json:"package_name"`
	ActivatedAt    *time.Time     `json:"activated_at,omitempty"`
	WindowEndsAt   *time.Time     `json:"window_ends_at,omitempty"`
	TerminatedAt   *time.Time     `json:"terminated_at,omitempty"`
	ConsumedBytes  int64          `json:"consumed_data_bytes"`
	DataQuotaBytes *int64         `json:"data_quota_bytes,omitempty"`
	OnlineSeconds  int64          `json:"consumed_online_seconds"`
	MaxDevices     *int           `json:"max_devices,omitempty"`
	Devices        []manageDevice `json:"devices"`
}

const manageEntitlementLimit = 10

// getGuestAccountManage: the account, its verification state and its recent packages with their devices.
func (s *server) getGuestAccountManage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var a iamv2Account
	if err := scanIAMv2Account(s.db.QueryRow(ctx, `SELECT `+iamv2AccountCols+`
	     FROM iam_v2.guest_access_accounts WHERE id=$1 AND tenant_id=$2 AND site_id=$3`,
		id, s.tenantID, s.siteID), &a); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	a.ActiveDevices = s.iamv2ActiveDeviceCount(ctx, id)
	a.withVerificationState(s.verificationSettings(ctx), time.Now())

	rows, err := s.db.Query(ctx, `
	    SELECT e.id::text, e.status, e.terminal_reason, COALESCE(NULLIF(ipr.display->>'name', ''), ''),
	           e.activated_at, e.window_ends_at, e.terminated_at, e.consumed_data_bytes, e.data_quota_bytes,
	           e.consumed_online_seconds, spr.max_concurrent_devices
	      FROM iam_v2.entitlements e
	      LEFT JOIN iam_v2.internet_package_revisions ipr ON ipr.id = e.package_revision_id
	      LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
	     WHERE e.tenant_id=$1 AND e.site_id=$2 AND e.guest_account_id=$3
	     ORDER BY (e.status IN ('PENDING','ACTIVE','SUSPENDED')) DESC, e.activated_at DESC NULLS LAST, e.id
	     LIMIT $4`, s.tenantID, s.siteID, id, manageEntitlementLimit)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "could not read this account's packages")
		return
	}
	ents := []manageEntitlement{}
	for rows.Next() {
		var e manageEntitlement
		if err := rows.Scan(&e.ID, &e.Status, &e.TerminalReason, &e.PackageName, &e.ActivatedAt,
			&e.WindowEndsAt, &e.TerminatedAt, &e.ConsumedBytes, &e.DataQuotaBytes, &e.OnlineSeconds,
			&e.MaxDevices); err != nil {
			rows.Close()
			jsonErr(w, http.StatusInternalServerError, "internal", "could not read this account's packages")
			return
		}
		e.Devices = []manageDevice{}
		ents = append(ents, e)
	}
	rows.Close()
	if rows.Err() != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "could not read this account's packages")
		return
	}
	for i := range ents {
		drows, err := s.db.Query(ctx, `
		    SELECT d.id::text, d.mac::text, ed.status, ed.first_authorized, ed.last_authorized,
		           EXISTS (SELECT 1 FROM iam_v2.sessions s
		                    WHERE s.entitlement_id = ed.entitlement_id AND s.device_id = ed.device_id
		                      AND s.ended IS NULL)
		      FROM iam_v2.entitlement_devices ed
		      JOIN iam_v2.devices d ON d.tenant_id = ed.tenant_id AND d.site_id = ed.site_id AND d.id = ed.device_id
		     WHERE ed.tenant_id=$1 AND ed.site_id=$2 AND ed.entitlement_id=$3::uuid
		     ORDER BY ed.first_authorized NULLS LAST, d.mac`, s.tenantID, s.siteID, ents[i].ID)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "could not read this account's devices")
			return
		}
		for drows.Next() {
			var d manageDevice
			if err := drows.Scan(&d.ID, &d.MAC, &d.Status, &d.FirstAuthorized, &d.LastAuthorized, &d.Online); err != nil {
				drows.Close()
				jsonErr(w, http.StatusInternalServerError, "internal", "could not read this account's devices")
				return
			}
			ents[i].Devices = append(ents[i].Devices, d)
		}
		drows.Close()
	}
	var total int
	_ = s.db.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.entitlements
	     WHERE tenant_id=$1 AND site_id=$2 AND guest_account_id=$3`, s.tenantID, s.siteID, id).Scan(&total)
	// What Remove will do. An account with no package may still have sign-in history (auth contexts edged
	// cannot read); the delete then finds that out and removes instead, and says so.
	removal := "delete"
	if total > 0 || a.LoginCount > 0 {
		removal = "remove"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account": a, "entitlements": ents, "entitlements_total": total,
		"removed": isRemovedUsername(a.Username) && !a.Enabled, "removal": removal,
	})
}

// deleteGuestAccountIAMv2 deletes an account that was never used, and removes (retires) one with history.
func (s *server) deleteGuestAccountIAMv2(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var username string
	var enabled bool
	if err := s.db.QueryRow(ctx, `SELECT username, enabled FROM iam_v2.guest_access_accounts
	     WHERE id=$1 AND tenant_id=$2 AND site_id=$3`, id, s.tenantID, s.siteID).Scan(&username, &enabled); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	if !enabled && isRemovedUsername(username) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "removed", "history_kept": true, "disconnected_sessions": 0})
		return
	}
	var ents int
	if err := s.db.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.entitlements
	     WHERE tenant_id=$1 AND site_id=$2 AND guest_account_id=$3`, s.tenantID, s.siteID, id).Scan(&ents); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "could not read this account's packages")
		return
	}
	if ents == 0 {
		// Never held a package. The row goes unless a sign-in recorded an auth context, which the foreign key
		// reports; the account is then removed instead, keeping that history.
		tag, err := s.db.Exec(ctx, `DELETE FROM iam_v2.guest_access_accounts
		     WHERE id=$1 AND tenant_id=$2 AND site_id=$3`, id, s.tenantID, s.siteID)
		if err == nil && tag.RowsAffected() == 1 {
			s.audit(r, "guest_account.deleted", "guest_account", id,
				map[string]any{"username": username, "authority": "iam_v2"})
			writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
			return
		}
		if err != nil && !isFKViolation(err) {
			jsonErr(w, http.StatusInternalServerError, "internal", "the account could not be deleted")
			return
		}
	}

	// HISTORY KEPT: disconnect first (a refusal leaves the account exactly as it was), then retire the row.
	res, st, code, msg := s.endAccountSessions(r, ctx, id, username)
	if st != 0 {
		jsonErr(w, st, code, msg)
		return
	}
	// ITS DEVICES' PLACES ARE FREED. Nobody can sign in to a removed account again, so a place one of its live
	// packages holds is held for nothing -- and a device showing "Holds a place" on a package no one can use is
	// exactly the confusion this removes. The places go through the audited staff release (0118); the package and
	// its history stay.
	opID, opLabel, ok := deviceActor(w, r)
	if !ok {
		return
	}
	released := s.releaseAccountPlaces(r, ctx, id, opID, opLabel)
	note := "Removed by staff on " + time.Now().UTC().Format("2006-01-02") + "; the history of this account is kept."
	tag, err := s.db.Exec(ctx, `UPDATE iam_v2.guest_access_accounts
	       SET enabled = false, username = $4, grace_until = NULL,
	           notes = CASE WHEN COALESCE(notes, '') = '' THEN $5 ELSE notes || E'\n' || $5 END
	     WHERE id=$1 AND tenant_id=$2 AND site_id=$3`, id, s.tenantID, s.siteID, removedUsername(username, id), note)
	if err != nil || tag.RowsAffected() != 1 {
		jsonErr(w, http.StatusInternalServerError, "internal", "the account could not be removed")
		return
	}
	s.audit(r, "guest_account.removed", "guest_account", id, map[string]any{
		"username": username, "authority": "iam_v2", "history_kept": true,
		"disconnected_sessions": res.Ended, "failed_sessions": res.Failed, "places_released": released,
	})
	out := map[string]any{"status": "removed", "history_kept": true, "disconnected_sessions": res.Ended,
		"places_released": released}
	if res.Failed > 0 {
		out["disconnect_failures"] = res.Failed
	}
	writeJSON(w, http.StatusOK, out)
}

// selfServiceTarget loads a live (not removed) self-registered account for the verification actions.
func (s *server) selfServiceTarget(w http.ResponseWriter, r *http.Request, ctx contextT, id string) (username string, verified, ok bool) {
	var selfReg, enabled bool
	if err := s.db.QueryRow(ctx, `SELECT username, self_registered, enabled, email_verified_at IS NOT NULL
	     FROM iam_v2.guest_access_accounts WHERE id=$1 AND tenant_id=$2 AND site_id=$3`,
		id, s.tenantID, s.siteID).Scan(&username, &selfReg, &enabled, &verified); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return "", false, false
	}
	if !selfReg {
		jsonErr(w, http.StatusConflict, "not_self_service",
			"only an account the client registered with their email address has an email to verify")
		return "", false, false
	}
	if !enabled && isRemovedUsername(username) {
		jsonErr(w, http.StatusConflict, "removed", "this account has been removed")
		return "", false, false
	}
	return username, verified, true
}

// markGuestAccountEmailVerified: staff vouch for the address (for example, the client showed it to the desk).
// Restores normal status at once; the package is untouched.
func (s *server) markGuestAccountEmailVerified(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	username, verified, ok := s.selfServiceTarget(w, r, ctx, id)
	if !ok {
		return
	}
	if verified {
		writeJSON(w, http.StatusOK, map[string]any{"status": "already_verified"})
		return
	}
	if _, err := s.db.Exec(ctx, `UPDATE iam_v2.guest_access_accounts
	       SET email_verified_at = now(), grace_until = NULL
	     WHERE id=$1 AND tenant_id=$2 AND site_id=$3 AND email_verified_at IS NULL`,
		id, s.tenantID, s.siteID); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the address could not be marked verified")
		return
	}
	s.audit(r, "guest_account.email_marked_verified", "guest_account", id,
		map[string]any{"username": username, "authority": "iam_v2", "by": "staff"})
	writeJSON(w, http.StatusOK, map[string]any{"status": "verified"})
}

// restartGuestAccountVerification gives an unverified account a new verification period: the deadline moves to
// now + the site's deadline setting and the grace hour is available again. Lifts a suspension; the package is
// untouched.
func (s *server) restartGuestAccountVerification(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	username, verified, ok := s.selfServiceTarget(w, r, ctx, id)
	if !ok {
		return
	}
	if verified {
		jsonErr(w, http.StatusConflict, "already_verified", "this address is already verified")
		return
	}
	set, err := s.selfServiceSettingsFor(ctx)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "settings_unavailable",
			"the site's Email accounts settings could not be read, so no new deadline was set")
		return
	}
	hours := set.VerificationDeadlineHours
	var deadline time.Time
	if err := s.db.QueryRow(ctx, `UPDATE iam_v2.guest_access_accounts
	       SET verification_deadline = now() + make_interval(hours => $4), grace_until = NULL,
	           grace_extensions_used = 0, verification_reminder_sent_at = NULL
	     WHERE id=$1 AND tenant_id=$2 AND site_id=$3 AND email_verified_at IS NULL
	 RETURNING verification_deadline`, id, s.tenantID, s.siteID, hours).Scan(&deadline); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the verification period could not be restarted")
		return
	}
	s.audit(r, "guest_account.verification_restarted", "guest_account", id, map[string]any{
		"username": username, "authority": "iam_v2", "deadline_hours": hours,
	})
	writeJSON(w, http.StatusOK, map[string]any{"status": "restarted", "verification_deadline": deadline})
}

// releaseAccountPlaces frees every place a device holds on the account's live packages (the account's sessions
// are already ended). Returns how many were freed; one that cannot be freed is left as it is.
func (s *server) releaseAccountPlaces(r *http.Request, ctx contextT, accountID, opID, opLabel string) int {
	rows, err := s.db.Query(ctx, `SELECT ed.entitlement_id::text, ed.device_id::text
	     FROM iam_v2.entitlement_devices ed JOIN iam_v2.entitlements e ON e.id = ed.entitlement_id
	     WHERE e.tenant_id=$1 AND e.site_id=$2 AND e.guest_account_id=$3::uuid AND ed.status='AUTHORIZED'
	       AND e.status IN ('ACTIVE','PENDING','SUSPENDED')`, s.tenantID, s.siteID, accountID)
	if err != nil {
		return 0
	}
	type place struct{ ent, dev string }
	var all []place
	for rows.Next() {
		var p place
		if rows.Scan(&p.ent, &p.dev) == nil {
			all = append(all, p)
		}
	}
	rows.Close()
	n := 0
	for _, p := range all {
		if outcome, st, _, _ := s.releaseOne(r, ctx, p.dev, p.ent, opID, opLabel, "Account removed"); st == 0 && outcome == "OK" {
			n++
		}
	}
	return n
}
