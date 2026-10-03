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
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	// Live device count for this account, from IAM-v2 session state.
	ActiveDevices int `json:"active_devices"`
	// Authority is explicit so an operator or a test never has to infer which domain owns this record.
	Authority string `json:"authority"`
}

// THERE IS NO ACCOUNT LOCKOUT ON THIS SURFACE, AND THERE NEVER WAS ONE.
//
// iam_v2.guest_access_accounts carries failed_attempts and locked_until, and the authenticator honours
// locked_until if it is ever set (internal/iamv2/adapters.go denies with reason "locked"). Nothing in the
// product sets either of them. The accepted protection against repeated failed sign-ins is DEVICE-BASED —
// site_guest_signin_protection, guest_signin_restrictions and sign_in_attempts (migration 0068), served by
// resources_signin_protection.go — which restricts the DEVICE that is guessing, not the account it is guessing
// at.
//
// So locked_until was a column that could only ever read NULL, and this API reported it: a "Locked out"
// counter that was structurally zero, a status=locked filter that could only return nothing, and a row badge
// that could never appear. An always-zero counter is worse than no counter, because an operator reads "0
// locked out" as a measurement and concludes nobody is being locked out — which is true of the account model
// and tells them nothing about the devices that ARE being restricted.
//
// The projection therefore does not carry it. The COLUMNS STAY: the authenticator reads them and would honour
// a value, so dropping them would remove a working deny path to tidy a display field. What is removed is the
// claim that this screen measures something.
//
// iamv2AccountCols is the projection shared by list, get and patch so the three cannot drift apart.
const iamv2AccountCols = `id, username, display_name, notes, enabled,
                valid_from, valid_until, last_login_at, login_count, created_at`

func scanIAMv2Account(row interface{ Scan(...any) error }, a *iamv2Account) error {
	if err := row.Scan(&a.ID, &a.Username, &a.DisplayName, &a.Notes, &a.Enabled,
		&a.ValidFrom, &a.ValidUntil, &a.LastLoginAt, &a.LoginCount, &a.CreatedAt); err != nil {
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

// accountsSummary counts only what this domain can actually answer for. There is deliberately no "locked"
// count: see the note on iamv2AccountCols.
type accountsSummary struct {
	Total         int `json:"total"`
	Enabled       int `json:"enabled"`
	Disabled      int `json:"disabled"`
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

// accountFilter: $1 tenant, $2 site, $3 status (enabled|disabled), $4 search pattern.
const accountFilter = `WHERE a.tenant_id=$1 AND a.site_id=$2
	  AND ($3::text IS NULL
	       OR ($3 = 'enabled'  AND a.enabled)
	       OR ($3 = 'disabled' AND NOT a.enabled))
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
	case "enabled", "disabled":
		statusArg = v
	default:
		// "locked" is refused rather than quietly accepted and answered with an empty page: a filter that
		// always matches nothing reads as "no account is locked out", which is a measurement this domain
		// cannot make. Repeated-failure protection is per DEVICE (migration 0068).
		jsonErr(w, http.StatusBadRequest, "bad_request", "status must be enabled or disabled")
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

	// The counters are over EVERY matching account, so they describe the site, not the page on screen.
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

	// NEWEST FIRST. Accounts created before migration 0104 have no creation time and sort after every dated
	// one, by username; id breaks the last tie so a page boundary is stable.
	rows, err := s.db.Query(ctx, `SELECT a.id, a.username, a.display_name, a.notes, a.enabled,
	           a.valid_from, a.valid_until, a.last_login_at, a.login_count, a.created_at,
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
		// DISCONNECT THIS ACCOUNT'S DEVICES NOW, the checkbox on the Set-password dialog.
		//
		// The field was missing, and decodeJSON uses DisallowUnknownFields, so the Admin Console's
		// set-password call — which always sends disconnect_sessions, true or false — was answered
		// 400 "bad body". Setting a client account's password from the Admin Console did not work at
		// all; the defect was not that a ticked box was ignored.
		DisconnectSessions bool `json:"disconnect_sessions,omitempty"`
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
	// The account must exist BEFORE anything is disconnected, so a disconnect can never run for an id the
	// password update would then refuse.
	var username string
	if err := s.db.QueryRow(ctx, `SELECT username FROM iam_v2.guest_access_accounts
	     WHERE id=$1 AND tenant_id=$2 AND site_id=$3`, id, s.tenantID, s.siteID).Scan(&username); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}

	// THE DISCONNECT RUNS BEFORE THE PASSWORD CHANGE, and that ordering is deliberate.
	//
	// endAccountSessions refuses up front — before it ends anything — in the two cases it cannot honour
	// (no enforcement channel, a live session with no address). Refusing first means a refusal leaves the
	// password UNCHANGED and every device still online, which is a state the operator can retry from. The
	// other order would reset the password, then refuse, and the dialog's own promise ("the old one stops
	// working immediately") would already be half-done.
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
	// failed_attempts and locked_until are reset defensively, not because anything sets them: nothing in the
	// product does (see the note on iamv2AccountCols). The authenticator would honour a locked_until that
	// somehow existed, so clearing it here cannot leave an operator with an account they reset and still
	// cannot use.
	//
	// NEVER the password or the hash.
	s.audit(r, "guest_account.password_set", "guest_account", id,
		map[string]any{"username": username, "authority": "iam_v2"})
	if generated {
		out["generated_password"] = in.Password
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------------------------------------
// ENDING ONE CLIENT ACCOUNT'S LIVE SESSIONS.
//
// WHAT THIS DOES AND, MORE IMPORTANTLY, WHAT IT MUST NOT DO. It ends the account's LIVE SESSIONS. It does not
// touch the Entitlement (not its status, not its expiry), it does not touch consumed_data_bytes or
// consumed_online_seconds, and it writes no history. The guest signs in again with the new password and
// resumes on the SAME Entitlement with the SAME remaining allowance — which is what the dialog promises
// ("The account still works, so they can sign in again") and the reason a disconnect is not a revocation.
//
// IT GOES THROUGH scd, the one operator session-termination path, exactly as resources_sessions.go's
// per-session Disconnect does: POST /v1/sessions/revoke {ip, reason:"admin"}. scd owns nftables and tc, so it
// is the only component that can actually take the device off the network; it denies the address on the
// device's bridge, deletes its shaping class, and only then ends the session row (state='ended', ended,
// end_reason). edged never writes that row itself.
//
// The superseded implementation was a single UPDATE from edged that set `ended` and `end_reason =
// 'admin_disconnect'` and left `state` at 'active'. Three things were wrong with it at once: the device kept
// its nftables authorization and its traffic (nothing had told scd), the session stayed 'active' so every
// state-filtered view still counted it, and 'admin_disconnect' is not in the end-reason vocabulary the rest of
// the system uses. It reported a number of "disconnected sessions" that described rows it had edited, not
// devices that had lost access.
//
// THE COUNT IS MEASURED, NOT ASSUMED. The number returned is how many of this account's sessions are
// 'ended' AFTER the revocations, read back from the database. A revoke that scd refuses, or that leaves the
// row untouched, is counted as a failure rather than quietly folded into the success total.
type accountDisconnectResult struct {
	Ended  int
	Failed int
}

// liveAccountSession is one live session of a client account with the address enforcement is keyed on.
type liveAccountSession struct {
	id string
	ip string
}

// endAccountSessions ends every live session of ONE client account, scoped to this tenant, this site and this
// account. A non-zero HTTP status is a REFUSAL, and a refusal is always issued before anything is ended.
func (s *server) endAccountSessions(r *http.Request, ctx contextT, accountID, username string) (
	accountDisconnectResult, int, string, string) {
	var res accountDisconnectResult

	// SCOPED THREE WAYS. The entitlement join is what links a session to an account (an Entitlement has
	// exactly one subject, by the ent_one_subject constraint), and tenant_id/site_id are compared on BOTH
	// sides so a session can never be reached through an Entitlement of another site.
	rows, err := s.db.Query(ctx, `
	    SELECT s.id::text, COALESCE(host(s.ip), '')
	      FROM iam_v2.sessions s
	      JOIN iam_v2.entitlements e
	        ON e.tenant_id = s.tenant_id AND e.site_id = s.site_id AND e.id = s.entitlement_id
	     WHERE s.tenant_id = $1 AND s.site_id = $2 AND e.guest_account_id = $3
	       AND s.ended IS NULL AND s.state IN ('active','PENDING_ENFORCEMENT')
	     ORDER BY s.started`, s.tenantID, s.siteID, accountID)
	if err != nil {
		return res, http.StatusInternalServerError, "internal", "could not read this account's sessions"
	}
	var live []liveAccountSession
	for rows.Next() {
		var ls liveAccountSession
		if err := rows.Scan(&ls.id, &ls.ip); err != nil {
			rows.Close()
			return res, http.StatusInternalServerError, "internal", "could not read this account's sessions"
		}
		live = append(live, ls)
	}
	rows.Close()
	if rows.Err() != nil {
		return res, http.StatusInternalServerError, "internal", "could not read this account's sessions"
	}
	if len(live) == 0 {
		// A NO-OP IS A SUCCESS WITH A COUNT OF ZERO, not a refusal. Nothing is audited for a disconnect that
		// disconnected nothing.
		return res, 0, "", ""
	}

	// REFUSAL 1: no enforcement channel. Without scd nothing can be taken off the network, and ending the
	// rows alone would be exactly the lie this replaces.
	if s.scd == nil {
		return res, http.StatusServiceUnavailable, "enforcement_unavailable",
			"this appliance cannot withdraw network access right now, so no device was disconnected"
	}
	// REFUSAL 2: a live session with no recorded address. Enforcement is keyed on the address, so there is no
	// honest way to disconnect it from here.
	noAddr := 0
	for _, ls := range live {
		if ls.ip == "" {
			noAddr++
		}
	}
	if noAddr > 0 {
		return res, http.StatusConflict, "conflict", fmt.Sprintf(
			"%d of this account's live sessions have no recorded address, so their network access cannot be "+
				"withdrawn from here. Nothing was disconnected; end them from Active sessions.", noAddr)
	}

	// One revoke per ADDRESS: scd revokes by address, and two sessions on one address would otherwise be
	// revoked twice, the second call reporting a failure for a device that is already off.
	seen := map[string]bool{}
	for _, ls := range live {
		if seen[ls.ip] {
			continue
		}
		seen[ls.ip] = true
		// The verdict is deliberately not branched on. A device scd could not take off the network is a
		// reported FAILURE, not a reason to leave the account's other devices online, and whether it came
		// off is measured from the session rows below rather than taken from this answer.
		_, _, _ = s.scd.call(r.Context(), http.MethodPost, "/v1/sessions/revoke",
			map[string]string{"ip": ls.ip, "reason": "admin"})
	}

	// READ BACK what actually ended, over exactly the sessions this call set out to end.
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

// disconnectGuestAccountSessionsIAMv2 is the row action: end this account's live sessions and nothing else.
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
