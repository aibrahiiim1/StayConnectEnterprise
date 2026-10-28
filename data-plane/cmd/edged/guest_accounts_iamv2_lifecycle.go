package main

// One authority per request: when ACCOUNT is enabled every operation reads and writes iam_v2 only. There is
// no dual write and no fall back to legacy, which would make same-named accounts indistinguishable.
// IAM-v2 has no template_id or max_devices on the credential; they are not back-filled from legacy rows.

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

type contextT = context.Context

// iamv2Account is not edgeGuestAccount: absent fields are omitted, never filled with a zero time or a legacy
// value. created_at is NULL (omitted) for accounts created before it was recorded.
type iamv2Account struct {
	ID            string     `json:"id"`
	Username      string     `json:"username"`
	DisplayName   *string    `json:"display_name,omitempty"`
	Notes         *string    `json:"notes,omitempty"`
	Enabled       bool       `json:"enabled"`
	ValidFrom     *time.Time `json:"valid_from,omitempty"`
	ValidUntil    *time.Time `json:"valid_until,omitempty"`
	LastLoginAt   *time.Time `json:"last_login_at,omitempty"`
	LoginCount    int64      `json:"login_count"`
	CreatedAt     *time.Time `json:"created_at,omitempty"`
	ActiveDevices int        `json:"active_devices"`
	Authority     string     `json:"authority"`

	// A self-registered account is one a client opened on the portal; its username is their email address.
	SelfRegistered       bool       `json:"self_registered"`
	EmailVerified        bool       `json:"email_verified"`
	EmailVerifiedAt      *time.Time `json:"email_verified_at,omitempty"`
	VerificationDeadline *time.Time `json:"verification_deadline,omitempty"`
	GraceUntil           *time.Time `json:"grace_until,omitempty"`
	GraceExtensionsUsed  int        `json:"grace_extensions_used"`
	// Suspended is computed at read time and absent when the site settings could not be read.
	Suspended *bool `json:"suspended,omitempty"`
}

// accountSuspended applies the same rule as scd's sweep: unverified, past its deadline, no live grace, on a
// site that requires verification and suspends (WARN_ONLY never suspends).
func accountSuspended(a iamv2Account, set selfServiceSettings, now time.Time) bool {
	if !a.SelfRegistered || a.EmailVerifiedAt != nil {
		return false
	}
	if !set.VerificationRequired || set.SuspensionPolicy != suspensionSuspend {
		return false
	}
	if a.VerificationDeadline == nil || a.VerificationDeadline.After(now) {
		return false
	}
	return a.GraceUntil == nil || !a.GraceUntil.After(now)
}

func (a *iamv2Account) withVerificationState(set *selfServiceSettings, now time.Time) {
	a.EmailVerified = a.EmailVerifiedAt != nil
	if set != nil {
		v := accountSuspended(*a, *set, now)
		a.Suspended = &v
	}
}

// There is no account lockout: nothing sets failed_attempts or locked_until (the authenticator would honour
// locked_until, so the columns stay), and sign-in protection is per device. The projection omits locked_until
// because an always-zero "locked out" count would read as a measurement.
//
// iamv2AccountCols is shared by list, get and patch so they cannot drift apart.
const iamv2AccountCols = `id, username, display_name, notes, enabled,
                valid_from, valid_until, last_login_at, login_count, created_at,
                self_registered, email_verified_at, verification_deadline, grace_until, grace_extensions_used`

func scanIAMv2Account(row interface{ Scan(...any) error }, a *iamv2Account) error {
	if err := row.Scan(&a.ID, &a.Username, &a.DisplayName, &a.Notes, &a.Enabled,
		&a.ValidFrom, &a.ValidUntil, &a.LastLoginAt, &a.LoginCount, &a.CreatedAt,
		&a.SelfRegistered, &a.EmailVerifiedAt, &a.VerificationDeadline, &a.GraceUntil,
		&a.GraceExtensionsUsed); err != nil {
		return err
	}
	a.Authority = "iam_v2"
	a.EmailVerified = a.EmailVerifiedAt != nil
	return nil
}

// verificationSettings returns nil when the settings cannot be read; the flag is then left out, not invented.
func (s *server) verificationSettings(ctx contextT) *selfServiceSettings {
	set, err := s.selfServiceSettingsFor(ctx)
	if err != nil {
		return nil
	}
	return &set.selfServiceSettings
}

// The search text travels in a header (X-Account-Search), not the URL: it is a username or a person's name,
// and edged logs request lines.
const (
	accountsPageSizeDefault = 50
	accountsPageSizeMax     = 200
	accountSearchHeader     = "X-Account-Search"
	accountSearchMaxLen     = 100
	// Characters; the Admin Console mirrors them as maxLength.
	accountDisplayNameMax = 120
	accountNotesMax       = 1000
)

// accountsSummary has no "locked" count; see iamv2AccountCols.
type accountsSummary struct {
	Total         int `json:"total"`
	Enabled       int `json:"enabled"`
	Disabled      int `json:"disabled"`
	DevicesOnline int `json:"devices_online"`
}

const accountFrom = `FROM iam_v2.guest_access_accounts a
	  LEFT JOIN LATERAL (
	      SELECT count(DISTINCT s.device_id)::int AS active_devices
	        FROM iam_v2.sessions s
	        JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
	       WHERE e.guest_account_id = a.id AND s.ended IS NULL) dev ON true`

// accountFilter: $1 tenant, $2 site, $3 status (enabled|disabled|removed), $4 search pattern. A removed
// account is listed only under its own filter, or it would read as one staff could re-enable.
const accountFilter = `WHERE a.tenant_id=$1 AND a.site_id=$2
	  AND ($3::text IS NULL AND NOT ` + sqlRemoved + `
	       OR ($3 = 'enabled'  AND a.enabled)
	       OR ($3 = 'disabled' AND NOT a.enabled AND NOT ` + sqlRemoved + `)
	       OR ($3 = 'removed'  AND ` + sqlRemoved + `))
	  AND ($4::text IS NULL OR a.username ILIKE $4 OR a.display_name ILIKE $4)`

func (s *server) listGuestAccountsIAMv2(w http.ResponseWriter, r *http.Request) {
	// A factory-clean appliance has no tenant/site until its signed assignment arrives; querying anyway fails
	// on a uuid compared with '' and looks like a broken appliance.
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "guest accounts")
		return
	}
	qv := r.URL.Query()
	var statusArg any
	switch v := qv.Get("status"); v {
	case "":
	case "enabled", "disabled", "removed":
		statusArg = v
	default:
		// "locked" is refused: an always-empty filter would read as "no account is locked out".
		jsonErr(w, http.StatusBadRequest, "bad_request", "status must be enabled, disabled or removed")
		return
	}
	page, size := 1, accountsPageSizeDefault
	if v := qv.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > pageMax {
			jsonErr(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("page must be a whole number from 1 to %d", pageMax))
			return
		}
		page = n
	}
	if v := qv.Get("page_size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > accountsPageSizeMax {
			jsonErr(w, http.StatusBadRequest, "bad_request",
				fmt.Sprintf("page_size must be between 1 and %d", accountsPageSizeMax))
			return
		}
		size = n
	}
	var searchArg any
	if v := headerSearchText(r, accountSearchHeader); v != "" {
		if len([]rune(v)) > accountSearchMaxLen {
			jsonErr(w, http.StatusBadRequest, "bad_request",
				fmt.Sprintf("search is limited to %d characters", accountSearchMaxLen))
			return
		}
		searchArg = "%" + likeEscape(v) + "%"
	}
	args := []any{s.tenantID, s.siteID, statusArg, searchArg}

	ctx, cancel := dbCtx(r)
	defer cancel()

	// Counters cover every matching account, not just the page.
	var sum accountsSummary
	if err := s.db.QueryRow(ctx, `SELECT count(*)::int,
	           count(*) FILTER (WHERE a.enabled)::int,
	           count(*) FILTER (WHERE NOT a.enabled)::int,
	           COALESCE(sum(dev.active_devices), 0)::int
	      `+accountFrom+`
	      `+accountFilter, args...).Scan(&sum.Total, &sum.Enabled, &sum.Disabled,
		&sum.DevicesOnline); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}

	// Accounts without a creation time sort last by username; id breaks ties so page boundaries are stable.
	rows, err := s.db.Query(ctx, `SELECT a.id, a.username, a.display_name, a.notes, a.enabled,
	           a.valid_from, a.valid_until, a.last_login_at, a.login_count, a.created_at,
	           a.self_registered, a.email_verified_at, a.verification_deadline, a.grace_until,
	           a.grace_extensions_used,
	           COALESCE(dev.active_devices, 0)
	      `+accountFrom+`
	      `+accountFilter+`
	     ORDER BY a.created_at DESC NULLS LAST, lower(a.username), a.id
	     LIMIT $5 OFFSET $6`, append(args, size+1, (page-1)*size)...)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	defer rows.Close()
	out := []iamv2Account{}
	for rows.Next() {
		var a iamv2Account
		if err := rows.Scan(&a.ID, &a.Username, &a.DisplayName, &a.Notes, &a.Enabled,
			&a.ValidFrom, &a.ValidUntil, &a.LastLoginAt, &a.LoginCount, &a.CreatedAt,
			&a.SelfRegistered, &a.EmailVerifiedAt, &a.VerificationDeadline, &a.GraceUntil,
			&a.GraceExtensionsUsed,
			&a.ActiveDevices); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "list failed")
			return
		}
		a.Authority = "iam_v2"
		out = append(out, a)
	}
	if rows.Err() != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	rows.Close()
	more := len(out) > size
	if more {
		out = out[:size]
	}
	set := s.verificationSettings(ctx)
	now := time.Now()
	for i := range out {
		out[i].withVerificationState(set, now)
	}
	// The authority is on the envelope too: an empty site has no rows to carry it.
	writeJSON(w, http.StatusOK, map[string]any{
		"data": out, "meta": listMeta{HasMore: more}, "authority": "iam_v2",
		"page": page, "page_size": size, "summary": sum,
	})
}

func (s *server) getGuestAccountIAMv2(w http.ResponseWriter, r *http.Request) {
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
	a.ActiveDevices = s.iamv2ActiveDeviceCount(ctx, a.ID)
	a.withVerificationState(s.verificationSettings(ctx), time.Now())
	writeJSON(w, http.StatusOK, a)
}

// iamv2ActiveDeviceCount is a best-effort display figure (0 on failure), not an enforcement input.
func (s *server) iamv2ActiveDeviceCount(ctx contextT, accountID string) int {
	var n int
	err := s.db.QueryRow(ctx, `
	    SELECT count(DISTINCT s.device_id)
	      FROM iam_v2.sessions s
	      JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
	     WHERE e.guest_account_id = $1 AND s.ended IS NULL`, accountID).Scan(&n)
	if err != nil {
		return 0
	}
	return n
}

type accountPatchOut struct {
	iamv2Account
	// Present only when the enabled flag changed.
	PackagesSuspended    *int `json:"packages_suspended,omitempty"`
	PackagesResumed      *int `json:"packages_resumed,omitempty"`
	DisconnectedSessions *int `json:"disconnected_sessions,omitempty"`
	DisconnectFailures   *int `json:"disconnect_failures,omitempty"`
}

func accountValidityError(from, until *time.Time) string {
	if from != nil && until != nil && !until.After(*from) {
		return "valid until must be later than valid from"
	}
	return ""
}

const accountRemovedMessage = "this account has been removed; it cannot be enabled, renamed or edited again"

func (s *server) patchGuestAccountIAMv2(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Optional fields are cleared by an explicit null; username and enabled cannot be null.
	var in struct {
		Username    *string             `json:"username,omitempty"`
		DisplayName optional[string]    `json:"display_name"`
		Notes       optional[string]    `json:"notes"`
		TemplateID  *string             `json:"template_id,omitempty"`
		Enabled     optional[bool]      `json:"enabled"`
		ValidFrom   optional[time.Time] `json:"valid_from"`
		ValidUntil  optional[time.Time] `json:"valid_until"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if in.Enabled.cleared() {
		jsonErr(w, http.StatusBadRequest, "bad_request", "enabled must be true or false")
		return
	}
	if in.Username != nil {
		*in.Username = strings.TrimSpace(*in.Username)
		if !validUsername(*in.Username) {
			jsonErr(w, http.StatusBadRequest, "bad_request",
				"username must be 1-64 chars: letters, digits, . _ - @ (no spaces)")
			return
		}
	}
	for _, f := range []*optional[string]{&in.DisplayName, &in.Notes} {
		if f.Value != nil {
			v := strings.TrimSpace(*f.Value)
			if v == "" {
				f.Value = nil
			} else {
				f.Value = &v
			}
		}
	}
	if in.DisplayName.Value != nil && utf8.RuneCountInString(*in.DisplayName.Value) > accountDisplayNameMax {
		jsonErr(w, http.StatusBadRequest, "bad_request",
			fmt.Sprintf("the name is limited to %d characters", accountDisplayNameMax))
		return
	}
	if in.Notes.Value != nil && utf8.RuneCountInString(*in.Notes.Value) > accountNotesMax {
		jsonErr(w, http.StatusBadRequest, "bad_request",
			fmt.Sprintf("notes are limited to %d characters", accountNotesMax))
		return
	}
	// template_id has no IAM-v2 meaning; refuse it rather than silently accept a field that does nothing.
	if in.TemplateID != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request",
			"template_id has no meaning under IAM-v2 authority: eligibility is decided by package rules")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()

	var cur struct {
		username        string
		selfReg, enable bool
		from, until     *time.Time
	}
	if err := s.db.QueryRow(ctx, `SELECT username, self_registered, enabled, valid_from, valid_until
	     FROM iam_v2.guest_access_accounts WHERE id=$1 AND tenant_id=$2 AND site_id=$3`, id, s.tenantID, s.siteID).
		Scan(&cur.username, &cur.selfReg, &cur.enable, &cur.from, &cur.until); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	// A removed account is final: every edit is refused, not only enabling or renaming.
	if !cur.enable && isRemovedUsername(cur.username) {
		jsonErr(w, http.StatusConflict, "removed", accountRemovedMessage)
		return
	}
	// A self-service username is the client's email address; renaming would move the account and its
	// entitlements to an address the owner never proved and send the next reset link there.
	if in.Username != nil && cur.selfReg && !strings.EqualFold(cur.username, *in.Username) {
		jsonErr(w, http.StatusForbidden, "self_service_account",
			"a self-service account's username is the client's email address and cannot be changed by staff")
		return
	}
	if msg := accountValidityError(in.ValidFrom.or(cur.from), in.ValidUntil.or(cur.until)); msg != "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	disabling := in.Enabled.Value != nil && !*in.Enabled.Value && cur.enable
	enabling := in.Enabled.Value != nil && *in.Enabled.Value && !cur.enable

	// Disabling ends the account's sessions. Refuse before writing anything: an appliance that cannot take a
	// device off the network must not leave the account disabled on paper with devices still connected.
	var live []liveAccountSession
	if disabling {
		var st int
		var code, msg string
		if live, st, code, msg = s.accountLiveSessions(ctx, id); st != 0 {
			jsonErr(w, st, code, msg)
			return
		}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the account could not be updated")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var out accountPatchOut
	err = scanIAMv2Account(tx.QueryRow(ctx, `
	    UPDATE iam_v2.guest_access_accounts
	       SET username     = COALESCE($4, username),
	           display_name = CASE WHEN $5 THEN $6 ELSE display_name END,
	           notes        = CASE WHEN $7 THEN $8 ELSE notes END,
	           enabled      = COALESCE($9, enabled),
	           valid_from   = CASE WHEN $10 THEN $11 ELSE valid_from END,
	           valid_until  = CASE WHEN $12 THEN $13 ELSE valid_until END
	     WHERE id=$1 AND tenant_id=$2 AND site_id=$3
	 RETURNING `+iamv2AccountCols,
		id, s.tenantID, s.siteID, in.Username,
		in.DisplayName.Set, in.DisplayName.Value, in.Notes.Set, in.Notes.Value,
		in.Enabled.Value,
		in.ValidFrom.Set, in.ValidFrom.Value, in.ValidUntil.Set, in.ValidUntil.Value), &out.iamv2Account)
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "username already exists")
			return
		}
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	// The package follows the account in the same transaction (disable suspends, enable resumes), through the
	// audited transition writer; the window keeps running either way.
	if disabling || enabling {
		var suspended, resumed int
		if err := tx.QueryRow(ctx, `SELECT suspended, resumed
		      FROM iam_v2.account_entitlements_follow_access($1::uuid,$2::uuid,$3::uuid)`,
			s.tenantID, s.siteID, id).Scan(&suspended, &resumed); err != nil {
			verb := "resumed"
			if disabling {
				verb = "suspended"
			}
			jsonErr(w, http.StatusInternalServerError, "internal",
				"the account's package could not be "+verb+"; nothing was changed")
			return
		}
		if disabling {
			out.PackagesSuspended = &suspended
		} else {
			out.PackagesResumed = &resumed
		}
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the account could not be updated")
		return
	}

	payload := map[string]any{"username": out.Username, "authority": "iam_v2"}
	if disabling {
		res, _, _, _ := s.revokeAccountSessions(r, ctx, id, out.Username, live)
		out.DisconnectedSessions = &res.Ended
		if res.Failed > 0 {
			out.DisconnectFailures = &res.Failed
		}
		payload["packages_suspended"] = *out.PackagesSuspended
		payload["disconnected_sessions"] = res.Ended
		payload["failed_sessions"] = res.Failed
		s.audit(r, "guest_account.disabled", "guest_account", id, payload)
	} else if enabling {
		payload["packages_resumed"] = *out.PackagesResumed
		s.audit(r, "guest_account.enabled", "guest_account", id, payload)
	}
	s.audit(r, "guest_account.updated", "guest_account", out.ID,
		map[string]any{"username": out.Username, "authority": "iam_v2"})
	writeJSON(w, http.StatusOK, out)
}

func (s *server) setGuestAccountPasswordIAMv2(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Password string `json:"password"`
		Generate bool   `json:"generate,omitempty"`
		// decodeJSON disallows unknown fields, and the console always sends this one.
		DisconnectSessions bool `json:"disconnect_sessions,omitempty"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	// Staff never set a self-service account's password: it recovers only by the reset link to its verified
	// address, and a staff-set one would be a second recovery path proving nothing. Checked before generating.
	var selfRegistered, enabledNow bool
	var currentName string
	if err := s.db.QueryRow(ctx, `SELECT self_registered, enabled, username FROM iam_v2.guest_access_accounts
	     WHERE id=$1 AND tenant_id=$2 AND site_id=$3`, id, s.tenantID, s.siteID).
		Scan(&selfRegistered, &enabledNow, &currentName); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	if !enabledNow && isRemovedUsername(currentName) {
		jsonErr(w, http.StatusConflict, "removed", accountRemovedMessage)
		return
	}
	if st, code, msg := selfServicePasswordRefusal(selfRegistered); st != 0 {
		jsonErr(w, st, code, msg)
		return
	}
	generated := false
	if in.Generate && in.Password == "" {
		pw, ok := s.generateAccountPassword(w, ctx)
		if !ok {
			return
		}
		in.Password, generated = pw, true
	}
	if ok, msg := validPassword(in.Password); !ok {
		jsonErr(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "hash failed")
		return
	}
	var username string
	if err := s.db.QueryRow(ctx, `SELECT username FROM iam_v2.guest_access_accounts
	     WHERE id=$1 AND tenant_id=$2 AND site_id=$3`, id, s.tenantID, s.siteID).Scan(&username); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}

	// Disconnect before the password change: a refusal then leaves the password unchanged and the operator can
	// retry, whereas the other order would reset it and then refuse.
	out := map[string]any{"status": "password_set"}
	if in.DisconnectSessions {
		res, st, code, msg := s.endAccountSessions(r, ctx, id, username)
		if st != 0 {
			jsonErr(w, st, code, msg)
			return
		}
		out["disconnected_sessions"] = res.Ended
		if res.Failed > 0 {
			out["disconnect_failures"] = res.Failed
		}
	}

	if err := s.db.QueryRow(ctx, `
	    UPDATE iam_v2.guest_access_accounts
	       SET password_hash=$4, failed_attempts=0, locked_until=NULL
	     WHERE id=$1 AND tenant_id=$2 AND site_id=$3 RETURNING username`,
		id, s.tenantID, s.siteID, hash).Scan(&username); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "password not set")
		return
	}
	// The counters are reset defensively so a stray locked_until cannot leave a reset account unusable.
	// The audit never carries the password or hash.
	s.audit(r, "guest_account.password_set", "guest_account", id,
		map[string]any{"username": username, "authority": "iam_v2"})
	if generated {
		out["generated_password"] = in.Password
	}
	writeJSON(w, http.StatusOK, out)
}

// selfServicePasswordRefusal returns (0, "", "") for a staff-created account; a self-registered one is refused
// with words the desk can repeat to the client.
func selfServicePasswordRefusal(selfRegistered bool) (int, string, string) {
	if !selfRegistered {
		return 0, "", ""
	}
	return http.StatusForbidden, "self_service_account",
		"This is a self-service email account. Staff cannot set or show its password: the client resets it " +
			"with the \"Forgot password\" link on the sign-in page, which emails them a reset link."
}

// scd owns the tokens, the outbox and the resend limits; no token or link passes through edged.
const scdResendVerificationPath = "/v1/admin/accounts/%s/resend-verification"

func (s *server) resendGuestAccountVerification(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Validate before placing it in scd's path.
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var username string
	var selfRegistered, verified, enabledNow bool
	if err := s.db.QueryRow(ctx, `SELECT username, self_registered, email_verified_at IS NOT NULL, enabled
	      FROM iam_v2.guest_access_accounts WHERE id=$1 AND tenant_id=$2 AND site_id=$3`,
		id, s.tenantID, s.siteID).Scan(&username, &selfRegistered, &verified, &enabledNow); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	if !enabledNow && isRemovedUsername(username) {
		jsonErr(w, http.StatusConflict, "removed", accountRemovedMessage)
		return
	}
	if !selfRegistered {
		jsonErr(w, http.StatusConflict, "not_self_service",
			"only a self-service email account has an address to verify")
		return
	}
	if verified {
		jsonErr(w, http.StatusConflict, "already_verified", "this account's email address is already verified")
		return
	}
	if s.relayResendVerification(w, r, id) {
		s.audit(r, "guest_account.verification_resent", "guest_account", id,
			map[string]any{"username": username, "authority": "iam_v2"})
	}
}

// relayResendVerification reports whether scd accepted. A scd 404 means the route is not served, not a missing account.
func (s *server) relayResendVerification(w http.ResponseWriter, r *http.Request, id string) bool {
	if s.scd == nil {
		jsonErr(w, http.StatusServiceUnavailable, "scd_unreachable", "the sign-in service could not be reached")
		return false
	}
	op := ""
	if sess := sessFrom(r.Context()); sess != nil {
		op = protectionActor(sess)
	}
	st, raw, err := s.scd.call(r.Context(), http.MethodPost, fmt.Sprintf(scdResendVerificationPath, id),
		map[string]any{"operator": op})
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", "the sign-in service could not be reached")
		return false
	}
	switch {
	case st == http.StatusNotFound:
		jsonErr(w, http.StatusNotImplemented, "resend_unavailable",
			"This appliance's sign-in service cannot resend verification emails yet.")
		return false
	case st >= 200 && st < 300:
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "queued",
			"notice": "The verification email is queued. It is sent as soon as the mail server accepts it."})
		return true
	default:
		s.relayScd(w, st, raw)
		return false
	}
}

// Ending an account's live sessions touches neither the Entitlement nor consumed usage nor history; the guest
// resumes on the same Entitlement. It goes through scd (POST /v1/sessions/revoke), the only component that
// owns nftables and tc and ends the session row; edged never writes that row. The count is measured by
// reading back how many sessions are 'ended', so a revoke scd refuses counts as a failure.
type accountDisconnectResult struct {
	Ended  int
	Failed int
}

type liveAccountSession struct {
	id string
	ip string
}

// endAccountSessions: a non-zero HTTP status is a refusal, always issued before anything is ended.
func (s *server) endAccountSessions(r *http.Request, ctx contextT, accountID, username string) (
	accountDisconnectResult, int, string, string) {
	live, st, code, msg := s.accountLiveSessions(ctx, accountID)
	if st != 0 {
		return accountDisconnectResult{}, st, code, msg
	}
	return s.revokeAccountSessions(r, ctx, accountID, username, live)
}

// accountLiveSessions decides before anything is changed whether the sessions can be ended; a non-zero status
// is the refusal. Split from the revoke so Disable can refuse first, commit, then end the sessions.
func (s *server) accountLiveSessions(ctx contextT, accountID string) ([]liveAccountSession, int, string, string) {
	// tenant_id and site_id are compared on both sides so a session is never reached through another site's Entitlement.
	rows, err := s.db.Query(ctx, `
	    SELECT s.id::text, COALESCE(host(s.ip), '')
	      FROM iam_v2.sessions s
	      JOIN iam_v2.entitlements e
	        ON e.tenant_id = s.tenant_id AND e.site_id = s.site_id AND e.id = s.entitlement_id
	     WHERE s.tenant_id = $1 AND s.site_id = $2 AND e.guest_account_id = $3
	       AND s.ended IS NULL AND s.state IN ('active','PENDING_ENFORCEMENT')
	     ORDER BY s.started`, s.tenantID, s.siteID, accountID)
	if err != nil {
		return nil, http.StatusInternalServerError, "internal", "could not read this account's sessions"
	}
	var live []liveAccountSession
	for rows.Next() {
		var ls liveAccountSession
		if err := rows.Scan(&ls.id, &ls.ip); err != nil {
			rows.Close()
			return nil, http.StatusInternalServerError, "internal", "could not read this account's sessions"
		}
		live = append(live, ls)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, http.StatusInternalServerError, "internal", "could not read this account's sessions"
	}
	if len(live) == 0 {
		// A no-op is a success with a count of zero, and is not audited.
		return nil, 0, "", ""
	}

	// Without scd nothing can be taken off the network; ending the rows alone would be a lie.
	if s.scd == nil {
		return nil, http.StatusServiceUnavailable, "enforcement_unavailable",
			"this appliance cannot withdraw network access right now, so no device was disconnected"
	}
	// Enforcement is keyed on the address, so a session without one cannot be disconnected from here.
	noAddr := 0
	for _, ls := range live {
		if ls.ip == "" {
			noAddr++
		}
	}
	if noAddr > 0 {
		return nil, http.StatusConflict, "conflict", fmt.Sprintf(
			"%d of this account's live sessions have no recorded address, so their network access cannot be "+
				"withdrawn from here. Nothing was disconnected; end them from Active sessions.", noAddr)
	}
	return live, 0, "", ""
}

func (s *server) revokeAccountSessions(r *http.Request, ctx contextT, accountID, username string,
	live []liveAccountSession) (accountDisconnectResult, int, string, string) {
	var res accountDisconnectResult
	if len(live) == 0 {
		return res, 0, "", ""
	}

	// One revoke per address: scd revokes by address, so a second call would report a failure for a device already off.
	seen := map[string]bool{}
	for _, ls := range live {
		if seen[ls.ip] {
			continue
		}
		seen[ls.ip] = true
		// The verdict is not branched on: one failure must not leave the other devices online, and the outcome is read back below.
		_, _, _ = s.scd.call(r.Context(), http.MethodPost, "/v1/sessions/revoke",
			map[string]string{"ip": ls.ip, "reason": "admin"})
	}

	ids := make([]string, 0, len(live))
	for _, ls := range live {
		ids = append(ids, ls.id)
	}
	if err := s.db.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.sessions
	     WHERE tenant_id=$1 AND site_id=$2 AND id = ANY($3::uuid[]) AND state='ended'`,
		s.tenantID, s.siteID, ids).Scan(&res.Ended); err != nil {
		return res, http.StatusInternalServerError, "internal",
			"the devices were asked to disconnect but the result could not be confirmed"
	}
	res.Failed = len(live) - res.Ended

	s.audit(r, "guest_account.sessions_disconnected", "guest_account", accountID, map[string]any{
		"username": username, "authority": "iam_v2",
		"disconnected_sessions": res.Ended, "failed_sessions": res.Failed,
	})
	return res, 0, "", ""
}

func (s *server) disconnectGuestAccountSessionsIAMv2(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var username string
	if err := s.db.QueryRow(ctx, `SELECT username FROM iam_v2.guest_access_accounts
	     WHERE id=$1 AND tenant_id=$2 AND site_id=$3`, id, s.tenantID, s.siteID).Scan(&username); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	res, st, code, msg := s.endAccountSessions(r, ctx, id, username)
	if st != 0 {
		jsonErr(w, st, code, msg)
		return
	}
	out := map[string]any{"status": "disconnected", "disconnected_sessions": res.Ended}
	if res.Failed > 0 {
		out["disconnect_failures"] = res.Failed
	}
	writeJSON(w, http.StatusOK, out)
}
