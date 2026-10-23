package main

// Removal has two outcomes. DELETED: the account was never used, so the row goes. REMOVED: it has history, so
// its devices are disconnected, it is disabled and its username moves to "<username>.removed-<id8>.invalid",
// freeing the address while purchase, entitlement and session rows keep pointing at it. ".invalid" is the RFC
// 2606 reserved TLD, so no mail is ever sent there, and validUsername refuses it for live accounts. A bare
// DELETE would fail on the auth-context foreign key, or cascade-erase the package history.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const removedUsernameTLD = ".invalid"

// removedUsername is unique by account id, recognisably the old name and undeliverable.
func removedUsername(username, id string) string {
	short := strings.ReplaceAll(id, "-", "")
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("%s.removed-%s%s", strings.ToLower(strings.TrimSpace(username)), short, removedUsernameTLD)
}

func isRemovedUsername(u string) bool {
	u = strings.ToLower(u)
	return strings.HasSuffix(u, removedUsernameTLD) && strings.Contains(u, ".removed-")
}

// sqlRemoved is isRemovedUsername in SQL, on alias a.
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
	// An account with no package may still have sign-in history edged cannot read; the delete then removes instead.
	removal := "delete"
	if total > 0 || a.LoginCount > 0 {
		removal = "remove"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account": a, "entitlements": ents, "entitlements_total": total,
		"removed": isRemovedUsername(a.Username) && !a.Enabled, "removal": removal,
	})
}

// deleteGuestAccountIAMv2 deletes an unused account and removes one with history. Removal is irreversible, so it
// requires the password step-up.
func (s *server) deleteGuestAccountIAMv2(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Password string `json:"password"`
	}
	// An empty body counts as a missing password.
	if err := decodeJSON(r, &in); err != nil && !errors.Is(err, io.EOF) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if _, ok := s.stepUpActor(w, r, in.Password); !ok {
		return
	}
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
		// A foreign-key violation means a sign-in recorded an auth context; remove instead, keeping that history.
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

	// Disconnect first: a refusal leaves the account exactly as it was.
	res, st, code, msg := s.endAccountSessions(r, ctx, id, username)
	if st != 0 {
		jsonErr(w, st, code, msg)
		return
	}
	// Nobody can sign in to a removed account, so its devices' places are freed through the audited staff release.
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

// markGuestAccountEmailVerified lets staff vouch for an address. That is the code-reveal power, not the
// guest-accounts one (the voucher print station lacks it), so it needs code-reveal WRITE, the operator's
// password and a bounded reason, all audited.
func (s *server) markGuestAccountEmailVerified(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !permFor(sessRoles(r), "code-reveal", permWrite) {
		jsonErr(w, http.StatusForbidden, "forbidden",
			"role lacks access to code-reveal: marking an address verified vouches for it on the client's behalf")
		return
	}
	var in struct {
		Password string `json:"password"`
		Reason   string `json:"reason"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	reason, okReason := reasonText(in.Reason)
	if !okReason {
		jsonErr(w, http.StatusBadRequest, "reason_required", reasonBoundsMessage)
		return
	}
	if _, ok := s.stepUpActor(w, r, in.Password); !ok {
		return
	}
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
		map[string]any{"username": username, "authority": "iam_v2", "by": "staff", "reason": reason})
	writeJSON(w, http.StatusOK, map[string]any{"status": "verified"})
}

// restartGuestAccountVerification moves the deadline to now plus the site setting and restores the grace hour.
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

// releaseAccountPlaces returns how many places were freed; one that cannot be freed is left as it is.
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
