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
	"fmt"
	"net/http"
	"strconv"
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
// a legacy row would restore the very dependency this trial removed. created_at exists since migration 0104
// and is NULL (omitted) for accounts created before it: they recorded no creation time and none is invented.
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
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	// Live device count for this account, from IAM-v2 session state.
	ActiveDevices int `json:"active_devices"`
	// Authority is explicit so an operator or a test never has to infer which domain owns this record.
	Authority string `json:"authority"`
}

// iamv2AccountCols is the projection shared by list, get and patch so the three cannot drift apart.
const iamv2AccountCols = `id, username, display_name, notes, enabled,
                valid_from, valid_until, last_login_at, login_count, locked_until, created_at`

func scanIAMv2Account(row interface{ Scan(...any) error }, a *iamv2Account) error {
	if err := row.Scan(&a.ID, &a.Username, &a.DisplayName, &a.Notes, &a.Enabled,
		&a.ValidFrom, &a.ValidUntil, &a.LastLoginAt, &a.LoginCount, &a.LockedUntil, &a.CreatedAt); err != nil {
		return err
	}
	a.Authority = "iam_v2"
	return nil
}

// CLIENT ACCOUNTS ARE PAGED, SEARCHED AND COUNTED ON THE SERVER, the Stays pattern (resources_phase3.go).
//
// The list used to return every account of the site, then ask for each one's live device count in a separate
// query (one round trip per account), and leave searching and counting to the browser. It now returns one page,
// newest first, with the counters computed over EVERY matching account in the same request, and the device
// counts joined in one query.
//
// THE SEARCH TEXT TRAVELS IN A HEADER (X-Account-Search), NOT THE URL: it is a username or a person's name, and
// edged logs request lines.
const (
	accountsPageSizeDefault = 50
	accountsPageSizeMax     = 200
	accountSearchHeader     = "X-Account-Search"
	accountSearchMaxLen     = 100
)

type accountsSummary struct {
	Total         int `json:"total"`
	Enabled       int `json:"enabled"`
	Disabled      int `json:"disabled"`
	Locked        int `json:"locked"`
	DevicesOnline int `json:"devices_online"`
}

// accountFrom projects one account with its live device count. The LATERAL replaces the per-row query the list
// used to run: one statement for the page, however many accounts it holds.
const accountFrom = `FROM iam_v2.guest_access_accounts a
	  LEFT JOIN LATERAL (
	      SELECT count(DISTINCT s.device_id)::int AS active_devices
	        FROM iam_v2.sessions s
	        JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
	       WHERE e.guest_account_id = a.id AND s.ended IS NULL) dev ON true`

// accountFilter: $1 tenant, $2 site, $3 status (enabled|disabled|locked), $4 search pattern.
const accountFilter = `WHERE a.tenant_id=$1 AND a.site_id=$2
	  AND ($3::text IS NULL
	       OR ($3 = 'enabled'  AND a.enabled)
	       OR ($3 = 'disabled' AND NOT a.enabled)
	       OR ($3 = 'locked'   AND a.locked_until > now()))
	  AND ($4::text IS NULL OR a.username ILIKE $4 OR a.display_name ILIKE $4)`

func (s *server) listGuestAccountsIAMv2(w http.ResponseWriter, r *http.Request) {
	// Guest accounts are tenant/site scoped, and a factory-clean appliance has neither until its signed
	// assignment arrives. Querying anyway compares a uuid column against '' and fails, which reached the
	// operator as "Could not load guest accounts -- the appliance did not answer": a report of a broken
	// appliance, on an appliance that is working exactly as intended.
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "guest accounts")
		return
	}
	qv := r.URL.Query()
	var statusArg any
	switch v := qv.Get("status"); v {
	case "":
	case "enabled", "disabled", "locked":
		statusArg = v
	default:
		jsonErr(w, http.StatusBadRequest, "bad_request", "status must be enabled, disabled or locked")
		return
	}
	page, size := 1, accountsPageSizeDefault
	if v := qv.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			jsonErr(w, http.StatusBadRequest, "bad_request", "page must be a positive whole number")
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
	if v := strings.TrimSpace(r.Header.Get(accountSearchHeader)); v != "" {
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

	// The counters are over EVERY matching account, so they describe the site, not the page on screen.
	var sum accountsSummary
	if err := s.db.QueryRow(ctx, `SELECT count(*)::int,
	           count(*) FILTER (WHERE a.enabled)::int,
	           count(*) FILTER (WHERE NOT a.enabled)::int,
	           count(*) FILTER (WHERE a.locked_until > now())::int,
	           COALESCE(sum(dev.active_devices), 0)::int
	      `+accountFrom+`
	      `+accountFilter, args...).Scan(&sum.Total, &sum.Enabled, &sum.Disabled, &sum.Locked,
		&sum.DevicesOnline); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}

	// NEWEST FIRST. Accounts created before migration 0104 have no creation time and sort after every dated
	// one, by username; id breaks the last tie so a page boundary is stable.
	rows, err := s.db.Query(ctx, `SELECT a.id, a.username, a.display_name, a.notes, a.enabled,
	           a.valid_from, a.valid_until, a.last_login_at, a.login_count, a.locked_until, a.created_at,
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
			&a.ValidFrom, &a.ValidUntil, &a.LastLoginAt, &a.LoginCount, &a.LockedUntil, &a.CreatedAt,
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
	more := len(out) > size
	if more {
		out = out[:size]
	}
	// The AUTHORITY is stated on the ENVELOPE, not only on each row: an empty site has no rows to ask.
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
	ctx, cancel := dbCtx(r)
	defer cancel()
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
