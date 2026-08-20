package main

// THE IAM-v2 GUEST-ACCOUNT LIFECYCLE.
//
// THE DEFECT THIS CLOSES
// ----------------------
// Switching only CREATE to IAM-v2 produced a split authority that was worse than either side alone:
// POST /edge/v1/guest-accounts wrote iam_v2.guest_access_accounts, while list, get, patch, set-password,
// disconnect and delete all still read and wrote public.guest_accounts. Measured on the DEVELOPMENT
// appliance: GET returned the legacy accounts (ahmed, devguest1) and did NOT return devguest2/devguest3,
// created seconds earlier through that same API. An operator created an account under IAM-v2 authority and
// watched it vanish, while every account they could still see was one IAM-v2 would refuse to authenticate.
//
// So the rule this file enforces is: ONE authority per request, chosen by the same config the authenticator
// is gated on, for EVERY operation and not just the one that happened to be implemented first.
//
//   ACCOUNT enabled  -> every operation reads and writes iam_v2 only.
//   ACCOUNT disabled -> every operation keeps its existing legacy behaviour, byte for byte.
//
// There is deliberately no dual write and no "try iam_v2, fall back to legacy" lookup. A fallback would make
// an IAM-v2 account and a legacy account with the same username indistinguishable through the API, which is
// precisely the ambiguity that let the original authority defect hide.
//
// RESPONSE SHAPE. The Hotel Admin UI is unchanged, so these handlers return the same edgeGuestAccount JSON.
// Two fields need care rather than invention:
//   * template_id: IAM-v2 has no template. What a subject may acquire is decided by package eligibility
//     rules at listing time. The field is returned EMPTY rather than back-filled from a legacy row, because
//     borrowing a legacy plan id here would recreate the dependency that was just removed.
//   * max_devices: likewise a property of the pinned package/service-plan revision at acquisition time, not
//     of the credential. Returned as null (unknown) rather than guessed.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// contextT keeps the helper signature readable; it is just context.Context.
type contextT = context.Context

const iamv2MethodAccount = iamv2.MethodAccount

// iamv2AccountAuthority reports whether IAM-v2 owns guest-account records for this request.
func (s *server) iamv2AccountAuthority() bool {
	return s.iamv2Cfg.Enabled(iamv2MethodAccount)
}

// iamv2Account is the IAM-v2 account response.
//
// It is NOT edgeGuestAccount. The legacy row carries created_at/updated_at and a template_id;
// iam_v2.guest_access_accounts records none of them -- checked against information_schema rather than
// assumed, after an earlier version of this file copied the legacy projection and failed at runtime with
// `column "created_at" does not exist`.
//
// Absent fields are OMITTED rather than filled with a zero time or a borrowed legacy value. A fabricated
// 0001-01-01 would be indistinguishable from a real timestamp to the UI, and back-filling template_id from
// a legacy row would restore the very dependency this trial removed. If the IAM-v2 schema should record
// creation time, that is a schema change to propose -- not a value to invent here.
type iamv2Account struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	DisplayName *string    `json:"display_name,omitempty"`
	Notes       *string    `json:"notes,omitempty"`
	Enabled     bool       `json:"enabled"`
	ValidFrom   *time.Time `json:"valid_from,omitempty"`
	ValidUntil  *time.Time `json:"valid_until,omitempty"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	LoginCount  int64      `json:"login_count"`
	LockedUntil *time.Time `json:"locked_until,omitempty"`
	// Live device count for this account, from IAM-v2 session state.
	ActiveDevices int `json:"active_devices"`
	// Authority is explicit so an operator or a test never has to infer which domain owns this record.
	Authority string `json:"authority"`
}

// iamv2AccountCols is the projection shared by list, get and patch so the three cannot drift apart.
const iamv2AccountCols = `id, username, display_name, notes, enabled,
                valid_from, valid_until, last_login_at, login_count, locked_until`

func scanIAMv2Account(row interface{ Scan(...any) error }, a *iamv2Account) error {
	if err := row.Scan(&a.ID, &a.Username, &a.DisplayName, &a.Notes, &a.Enabled,
		&a.ValidFrom, &a.ValidUntil, &a.LastLoginAt, &a.LoginCount, &a.LockedUntil); err != nil {
		return err
	}
	a.Authority = "iam_v2"
	return nil
}

func (s *server) listGuestAccountsIAMv2(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT `+iamv2AccountCols+`
	      FROM iam_v2.guest_access_accounts
	     WHERE tenant_id=$1 AND site_id=$2
	     ORDER BY username`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	defer rows.Close()
	out := []iamv2Account{}
	for rows.Next() {
		var a iamv2Account
		if err := scanIAMv2Account(rows, &a); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "list failed")
			return
		}
		a.ActiveDevices = s.iamv2ActiveDeviceCount(ctx, a.ID)
		out = append(out, a)
	}
	if rows.Err() != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	// The AUTHORITY is stated on the ENVELOPE, not only on each row.
	//
	// Per-row authority answers "who owns this record", which is useless to a screen that has no records: an
	// empty site cannot tell whether it is running IAM-v2 or legacy, so the operator-facing form has to guess.
	// It guessed wrong -- it kept presenting a mandatory "Guest access plan" picker that IAM-v2 ignores
	// entirely, because under IAM-v2 what a guest may acquire is decided by PACKAGE ELIGIBILITY RULES, not by
	// a plan attached to the credential. A required control whose value is discarded is worse than a missing
	// one: the operator believes they have chosen what the guest gets.
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "meta": listMeta{}, "authority": "iam_v2"})
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
	writeJSON(w, http.StatusOK, a)
}

// iamv2ActiveDeviceCount counts distinct devices currently online for this account, from IAM-v2 session
// state. Best effort: a counting failure must not fail the whole list, so it reports 0 and the caller still
// gets its accounts. It is a display field, not an enforcement input -- the device LIMIT is enforced in the
// entitlement/session domain, not here.
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

func (s *server) patchGuestAccountIAMv2(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Username    *string    `json:"username,omitempty"`
		DisplayName *string    `json:"display_name,omitempty"`
		Notes       *string    `json:"notes,omitempty"`
		TemplateID  *string    `json:"template_id,omitempty"`
		Enabled     *bool      `json:"enabled,omitempty"`
		ValidFrom   *time.Time `json:"valid_from,omitempty"`
		ValidUntil  *time.Time `json:"valid_until,omitempty"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
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
	// template_id is accepted by the UI form but has no IAM-v2 meaning. Refuse it explicitly rather than
	// ignoring it: silently accepting a field that does nothing is how the legacy dependency survived.
	if in.TemplateID != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request",
			"template_id has no meaning under IAM-v2 authority: eligibility is decided by package rules")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	// COALESCE keeps every omitted field at its current value, so a partial PATCH cannot blank a column.
	var a iamv2Account
	err := scanIAMv2Account(s.db.QueryRow(ctx, `
	    UPDATE iam_v2.guest_access_accounts
	       SET username     = COALESCE($4, username),
	           display_name = COALESCE($5, display_name),
	           notes        = COALESCE($6, notes),
	           enabled      = COALESCE($7, enabled),
	           valid_from   = COALESCE($8, valid_from),
	           valid_until  = COALESCE($9, valid_until)
	     WHERE id=$1 AND tenant_id=$2 AND site_id=$3
	 RETURNING `+iamv2AccountCols,
		id, s.tenantID, s.siteID, in.Username, in.DisplayName, in.Notes,
		in.Enabled, in.ValidFrom, in.ValidUntil), &a)
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "username already exists")
			return
		}
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	s.audit(r, "guest_account.updated", "guest_account", a.ID,
		map[string]any{"username": a.Username, "authority": "iam_v2"})
	writeJSON(w, http.StatusOK, a)
}

func (s *server) setGuestAccountPasswordIAMv2(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Password string `json:"password"`
		Generate bool   `json:"generate,omitempty"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	generated := false
	if in.Generate && in.Password == "" {
		pw, err := generatePassword()
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "generate failed")
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
	ctx, cancel := dbCtx(r)
	defer cancel()
	// A password reset also clears the lockout counters: an operator resetting a password is resolving the
	// situation that caused the lockout, and leaving the account locked would make the reset appear to fail.
	var username string
	if err := s.db.QueryRow(ctx, `
	    UPDATE iam_v2.guest_access_accounts
	       SET password_hash=$4, failed_attempts=0, locked_until=NULL
	     WHERE id=$1 AND tenant_id=$2 AND site_id=$3 RETURNING username`,
		id, s.tenantID, s.siteID, hash).Scan(&username); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	// NEVER the password or the hash.
	s.audit(r, "guest_account.password_set", "guest_account", id,
		map[string]any{"username": username, "authority": "iam_v2"})
	out := map[string]any{"status": "password_set"}
	if generated {
		out["generated_password"] = in.Password
	}
	writeJSON(w, http.StatusOK, out)
}

// disconnectGuestAccountSessionsIAMv2 ends the account's live IAM-v2 sessions. It deliberately does NOT
// touch public.sessions: for an IAM-v2-authoritative account there is nothing of its own in the legacy
// session table, and reaching into it would be the bridge this trial exists to avoid.
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
	var n int64
	tag, err := s.db.Exec(ctx, `
	    UPDATE iam_v2.sessions s
	       SET ended = now(), end_reason = 'admin_disconnect'
	      FROM iam_v2.entitlements e
	     WHERE e.id = s.entitlement_id AND e.guest_account_id = $1 AND s.ended IS NULL`, id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "disconnect failed")
		return
	}
	n = tag.RowsAffected()
	s.audit(r, "guest_account.sessions_disconnected", "guest_account", id,
		map[string]any{"username": username, "disconnected_sessions": n, "authority": "iam_v2"})
	writeJSON(w, http.StatusOK, map[string]any{"status": "disconnected", "disconnected_sessions": n})
}

func (s *server) deleteGuestAccountIAMv2(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var username string
	if err := s.db.QueryRow(ctx, `DELETE FROM iam_v2.guest_access_accounts
	     WHERE id=$1 AND tenant_id=$2 AND site_id=$3 RETURNING username`,
		id, s.tenantID, s.siteID).Scan(&username); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	s.audit(r, "guest_account.deleted", "guest_account", id,
		map[string]any{"username": username, "authority": "iam_v2"})
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// acctAuthority is REMOVED. It dispatched each guest-account route between a legacy handler and an IAM-v2
// one. With the superseded implementation gone there is nothing to dispatch between, and a switch with one
// destination is dead code that quietly asserts a second destination might return.
