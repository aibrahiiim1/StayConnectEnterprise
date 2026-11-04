package main

// Client activity: one read-only timeline of what happened to clients (connections, packages, devices, accounts,
// room sign-ins, stays and staff actions on clients), newest first. Served under the sessions resource; each source is
// added only when the reader may also read its own screen, and Hotel sources only where the Hotel module is on.
// Privacy: no guest name, voucher code, credential or email; a client is a room number, a username or a kind.
// Privileges: every iam_v2 relation read must be SELECT-granted to svc_edged (client_activity_test.go checks it).

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	clientActivityMaxSpan = 92 * 24 * time.Hour
	clientActivityMaxQ    = 64
)

var clientActivityCategories = map[string]bool{
	"connection": true, "package": true, "device": true, "account": true,
	"sign_in": true, "stay": true, "staff": true,
}

// clientStaffActions are the audit actions that act on a client; a family is listed only with read access to its screen.
var clientStaffActions = []struct {
	action, resource string
	hotel            bool
}{
	{"session.disconnected", "sessions", false},
	{"package.ended", "sessions", false},
	{"package.reset", "sessions", false},
	{"client_device.disconnected", "sessions", false},
	{"client_device.place_released", "sessions", false},
	{"client_device.signin_required", "sessions", false},
	{"client_device.prepared_for_test", "sessions", false},
	{"guest_account.created", "guest-accounts", false},
	{"guest_account.updated", "guest-accounts", false},
	{"guest_account.enabled", "guest-accounts", false},
	{"guest_account.disabled", "guest-accounts", false},
	{"guest_account.password_set", "guest-accounts", false},
	{"guest_account.verification_resent", "guest-accounts", false},
	{"guest_account.verification_restarted", "guest-accounts", false},
	{"guest_account.email_marked_verified", "guest-accounts", false},
	{"guest_account.sessions_disconnected", "guest-accounts", false},
	{"guest_account.deleted", "guest-accounts", false},
	{"guest_account.removed", "guest-accounts", false},
	{"voucher.issued", "vouchers", false},
	{"voucher.revoked", "vouchers", false},
	{"voucher_batch.revoked", "vouchers", false},
	{"voucher.code_revealed", "vouchers", false},
	{"voucher.codes_exported", "vouchers", false},
	{"guest_data.erased", "guest-data-rights", false},
	{"guest_signin_restriction.release", "guest-signin-protection", true},
	{"guest_signin_network_pause.end", "guest-signin-protection", true},
	{"guest_signin_network_pause.confirm_device", "guest-signin-protection", true},
	{"guest_signin_attempt.credentials_viewed", "guest-signin-attempts", true},
}

type clientActivityFilter struct {
	From, To time.Time
	Category string
	Problems bool
	Q        string
	Limit    int
	Offset   int
}

func parseClientActivityFilter(q url.Values, now time.Time) (clientActivityFilter, error) {
	f := clientActivityFilter{To: now.UTC(), Limit: activityDefaultLimit}
	// The page's anchor (ms since the epoch) fixes a preset window's end, so paging walks one stable window.
	if v := q.Get("at"); v != "" {
		ms, err := strconv.ParseInt(v, 10, 64)
		at := time.UnixMilli(ms).UTC()
		if err != nil || at.After(f.To.Add(time.Minute)) || f.To.Sub(at) > clientActivityMaxSpan {
			return f, errors.New("at must be a recent time in milliseconds since the epoch")
		}
		f.To = at
	}
	switch rng := q.Get("range"); rng {
	case "", "24h":
		f.From = f.To.Add(-24 * time.Hour)
	case "1h":
		f.From = f.To.Add(-time.Hour)
	case "7d":
		f.From = f.To.Add(-7 * 24 * time.Hour)
	case "30d":
		f.From = f.To.Add(-30 * 24 * time.Hour)
	case "custom":
		from, err1 := time.Parse(time.RFC3339, q.Get("from"))
		to, err2 := time.Parse(time.RFC3339, q.Get("to"))
		if err1 != nil || err2 != nil {
			return f, errors.New("a custom range needs from and to as RFC 3339 times")
		}
		f.From, f.To = from.UTC(), to.UTC()
	default:
		return f, fmt.Errorf("range must be 1h, 24h, 7d, 30d or custom, not %q", rng)
	}
	if !f.From.Before(f.To) {
		return f, errors.New("the range must start before it ends")
	}
	if f.To.Sub(f.From) > clientActivityMaxSpan {
		return f, errors.New("the range may cover at most 92 days")
	}
	if v := q.Get("category"); v != "" && v != "all" {
		if !clientActivityCategories[v] {
			return f, fmt.Errorf("unknown category %q", v)
		}
		f.Category = v
	}
	f.Problems = q.Get("problems") == "1" || q.Get("problems") == "true"
	p, err := parsePage(q, 0)
	if err != nil {
		return f, err
	}
	f.Limit, f.Offset = p.Size, p.Offset()
	return f, nil
}

// clientActivitySearchHeader carries the search text; it never travels in the URL, which edged logs.
const clientActivitySearchHeader = "X-Client-Activity-Search"

// clientActivityScope is what the reader may see: each flag is a source.
type clientActivityScope struct {
	Hotel, SignIns, Stays, Accounts, Staff bool
	StaffActions                           []string
}

func clientActivityScopeFor(roles []string, hotel bool) clientActivityScope {
	can := func(res string) bool { return permFor(roles, res, permRead) }
	sc := clientActivityScope{
		Hotel:    hotel,
		SignIns:  hotel && can("guest-signin-attempts"),
		Stays:    hotel && can("pms-stays"),
		Accounts: can("guest-accounts"),
		Staff:    can("audit"),
	}
	if sc.Staff {
		for _, a := range clientStaffActions {
			if (!a.hotel || hotel) && can(a.resource) {
				sc.StaffActions = append(sc.StaffActions, a.action)
			}
		}
	}
	return sc
}

// Every source yields the same columns, in this order.
const clientActivityCols = `at, cat, kind, sev, detail, client_kind, client_label, package, mac, ip, network, ref_session, ref_entitlement, ref_stay, actor, rid`

// clientOf describes the client behind entitlement e (stay st, account ga, package ipr/ip).
const clientOf = `CASE WHEN e.id IS NULL THEN '' WHEN e.stay_id IS NOT NULL THEN 'room' WHEN e.guest_account_id IS NOT NULL THEN 'account'
	            WHEN e.voucher_id IS NOT NULL THEN 'voucher' WHEN e.guest_principal_id IS NOT NULL THEN 'guest'
	            WHEN e.anonymous_subject_id IS NOT NULL THEN 'open' ELSE '' END,
	       COALESCE(NULLIF(st.normalized_room_number,''), ga.username, ''),
	       COALESCE(NULLIF(ipr.display->>'name',''), ip.code, '')`

// staffClientOf is clientOf for a staff action, whose client may also be the account it targeted (gat).
var staffClientOf = strings.NewReplacer(
	"CASE WHEN e.id IS NULL", "CASE WHEN gat.id IS NOT NULL THEN 'account' WHEN e.id IS NULL",
	"COALESCE(NULLIF(st.normalized_room_number,''), ga.username, '')", "COALESCE(gat.username, NULLIF(st.normalized_room_number,''), ga.username, '')",
).Replace(clientOf)

func entitlementJoins(on string) string {
	return `
	  LEFT JOIN iam_v2.entitlements e ON e.tenant_id = $1 AND e.site_id = $2 AND e.id = ` + on + `
	  LEFT JOIN iam_v2.stays st ON st.tenant_id = $1 AND st.site_id = $2 AND st.id = e.stay_id
	  LEFT JOIN iam_v2.guest_access_accounts ga ON ga.tenant_id = $1 AND ga.site_id = $2 AND ga.id = e.guest_account_id
	  LEFT JOIN iam_v2.internet_package_revisions ipr ON ipr.tenant_id = $1 AND ipr.site_id = $2 AND ipr.id = e.package_revision_id
	  LEFT JOIN iam_v2.internet_packages ip ON ip.tenant_id = $1 AND ip.site_id = $2 AND ip.id = ipr.package_id`
}

const sessionNetworkJoin = `
	  LEFT JOIN public.guest_networks gn ON gn.tenant_id = s.tenant_id AND gn.site_id = s.site_id AND gn.bridge_name = NULLIF(s.ingress_interface,'')`

// clientActivitySources returns the UNION ALL of the sources in scope; $1 tenant, $2 site, $3 from, $4 to.
func clientActivitySources(sc clientActivityScope, args *[]any) string {
	arg := func(v any) string { *args = append(*args, v); return "$" + strconv.Itoa(len(*args)) }
	parts := []string{
		`SELECT s.started, 'connection', 'CONNECTED', 'ok', COALESCE(s.credential_method,''), ` + clientOf + `,
	       COALESCE(s.mac::text,''), COALESCE(host(s.ip),''), COALESCE(gn.name,''), s.id::text, COALESCE(e.id::text,''), COALESCE(st.id::text,''), '', 'c1' || s.id::text
	  FROM (SELECT * FROM iam_v2.sessions s0 WHERE s0.tenant_id = $1 AND s0.site_id = $2 AND s0.started >= $3 AND s0.started < $4) s` +
			entitlementJoins("s.entitlement_id") + sessionNetworkJoin,

		`SELECT s.ended, 'connection', 'DISCONNECTED',
	       CASE WHEN s.end_reason IN ('ADMIN','ENFORCEMENT_TORN_DOWN','DEVICE_LIMIT_OLDEST_DISCONNECTED') THEN 'warning' ELSE 'info' END,
	       COALESCE(s.end_reason,''), ` + clientOf + `,
	       COALESCE(s.mac::text,''), COALESCE(host(s.ip),''), COALESCE(gn.name,''), s.id::text, COALESCE(e.id::text,''), COALESCE(st.id::text,''), '', 'c2' || s.id::text
	  FROM (SELECT * FROM iam_v2.sessions s0 WHERE s0.tenant_id = $1 AND s0.site_id = $2 AND s0.ended >= $3 AND s0.ended < $4) s` +
			entitlementJoins("s.entitlement_id") + sessionNetworkJoin,

		`SELECT e.activated_at, 'package', 'PACKAGE_STARTED', 'ok', COALESCE(p.trigger,''), ` + clientOf + `,
	       '', '', '', '', e.id::text, COALESCE(st.id::text,''), '', 'p1' || e.id::text
	  FROM iam_v2.entitlements e
	  LEFT JOIN iam_v2.purchases p ON p.tenant_id = $1 AND p.site_id = $2 AND p.id = e.purchase_id
	  LEFT JOIN iam_v2.stays st ON st.tenant_id = $1 AND st.site_id = $2 AND st.id = e.stay_id
	  LEFT JOIN iam_v2.guest_access_accounts ga ON ga.tenant_id = $1 AND ga.site_id = $2 AND ga.id = e.guest_account_id
	  LEFT JOIN iam_v2.internet_package_revisions ipr ON ipr.tenant_id = $1 AND ipr.site_id = $2 AND ipr.id = e.package_revision_id
	  LEFT JOIN iam_v2.internet_packages ip ON ip.tenant_id = $1 AND ip.site_id = $2 AND ip.id = ipr.package_id
	 WHERE e.tenant_id = $1 AND e.site_id = $2 AND e.activated_at >= $3 AND e.activated_at < $4`,

		`SELECT e.terminated_at, 'package', 'PACKAGE_ENDED',
	       CASE WHEN e.terminal_reason IN ('DATA','TIME','ADMIN') THEN 'warning' ELSE 'info' END,
	       COALESCE(e.terminal_reason,''), ` + clientOf + `,
	       '', '', '', '', e.id::text, COALESCE(st.id::text,''), '', 'p2' || e.id::text
	  FROM iam_v2.entitlements e
	  LEFT JOIN iam_v2.stays st ON st.tenant_id = $1 AND st.site_id = $2 AND st.id = e.stay_id
	  LEFT JOIN iam_v2.guest_access_accounts ga ON ga.tenant_id = $1 AND ga.site_id = $2 AND ga.id = e.guest_account_id
	  LEFT JOIN iam_v2.internet_package_revisions ipr ON ipr.tenant_id = $1 AND ipr.site_id = $2 AND ipr.id = e.package_revision_id
	  LEFT JOIN iam_v2.internet_packages ip ON ip.tenant_id = $1 AND ip.site_id = $2 AND ip.id = ipr.package_id
	 WHERE e.tenant_id = $1 AND e.site_id = $2 AND e.terminated_at >= $3 AND e.terminated_at < $4`,

		`SELECT dsa.acted_at, 'device', 'DEVICE_' || dsa.action,
	       CASE WHEN dsa.outcome = 'OK' THEN 'info' ELSE 'warning' END,
	       COALESCE(dsa.outcome, ''), ` + clientOf + `,
	       COALESCE(d.mac::text,''), COALESCE(host(d.last_ip),''), '', '', COALESCE(e.id::text,''), COALESCE(st.id::text,''),
	       COALESCE(NULLIF(dsa.operator_label,''), CASE WHEN dsa.actor = 'CLIENT' THEN '' ELSE dsa.actor END, ''), 'd1' || dsa.id::text
	  FROM iam_v2.device_slot_actions dsa
	  LEFT JOIN iam_v2.devices d ON d.tenant_id = $1 AND d.site_id = $2 AND d.id = dsa.device_id` + entitlementJoins("dsa.entitlement_id") + `
	 WHERE dsa.tenant_id = $1 AND dsa.site_id = $2 AND dsa.acted_at >= $3 AND dsa.acted_at < $4`,
	}
	if sc.Accounts {
		parts = append(parts,
			`SELECT ga.created_at, 'account', 'ACCOUNT_CREATED', 'info', CASE WHEN ga.self_registered THEN 'SELF' ELSE 'STAFF' END,
	       'account', ga.username, '', COALESCE(ga.created_by_device_mac::text,''), '', '', '', '', '', '', 'a1' || ga.id::text
	  FROM iam_v2.guest_access_accounts ga
	 WHERE ga.tenant_id = $1 AND ga.site_id = $2 AND ga.created_at >= $3 AND ga.created_at < $4`,
			`SELECT ga.email_verified_at, 'account', 'ACCOUNT_VERIFIED', 'ok', '',
	       'account', ga.username, '', '', '', '', '', '', '', '', 'a2' || ga.id::text
	  FROM iam_v2.guest_access_accounts ga
	 WHERE ga.tenant_id = $1 AND ga.site_id = $2 AND ga.email_verified_at >= $3 AND ga.email_verified_at < $4`)
	}
	if sc.SignIns {
		parts = append(parts,
			`SELECT sia.occurred_at, 'sign_in', 'ROOM_SIGN_IN',
	       CASE WHEN sia.result = 'VERIFIED' THEN 'ok'
	            WHEN sia.result IN ('SERVICE_UNAVAILABLE','ROUTING_OR_INTERFACE_FAILURE','MIRROR_STALE_OR_MISSING_CHANGE') THEN 'error'
	            ELSE 'warning' END,
	       sia.result, 'room', COALESCE(NULLIF(sia.submitted_room,''), st.normalized_room_number, ''), '',
	       COALESCE(sia.device_mac::text,''), COALESCE(host(sia.device_ip),''), COALESCE(sia.guest_network_name,''),
	       COALESCE(sia.session_id::text,''), COALESCE(sia.entitlement_id::text,''), COALESCE(st.id::text,''), '', 'r1' || sia.id::text
	  FROM iam_v2.sign_in_attempts sia
	  LEFT JOIN iam_v2.stays st ON st.tenant_id = $1 AND st.site_id = $2 AND st.id = sia.matched_stay_id
	 WHERE sia.tenant_id = $1 AND sia.site_id = $2 AND sia.occurred_at >= $3 AND sia.occurred_at < $4`)
	}
	if sc.Stays {
		parts = append(parts,
			// The first record the appliance received for a stay: the check-in, or the resync that brought an earlier one.
			`SELECT fe.at, 'stay', 'STAY_RECEIVED', 'info', '', 'room', COALESCE(st.normalized_room_number,''), '', '', '',
	       COALESCE(pi.display_label,''), '', '', st.id::text, '', 's1' || st.id::text
	  FROM iam_v2.stays st
	  CROSS JOIN LATERAL (SELECT sevt.received_at AS at FROM iam_v2.stay_events sevt
	                       WHERE sevt.tenant_id = $1 AND sevt.site_id = $2 AND sevt.stay_id = st.id ORDER BY sevt.received_at LIMIT 1) fe
	  LEFT JOIN iam_v2.pms_interfaces pi ON pi.tenant_id = $1 AND pi.site_id = $2 AND pi.id = st.pms_interface_id
	 WHERE st.tenant_id = $1 AND st.site_id = $2 AND fe.at >= $3 AND fe.at < $4`,
			`SELECT st.effective_checkout_at, 'stay', 'CHECKED_OUT', 'info', '', 'room', COALESCE(st.normalized_room_number,''), '', '', '',
	       COALESCE(pi.display_label,''), '', '', st.id::text, '', 's2' || st.id::text
	  FROM iam_v2.stays st
	  LEFT JOIN iam_v2.pms_interfaces pi ON pi.tenant_id = $1 AND pi.site_id = $2 AND pi.id = st.pms_interface_id
	 WHERE st.tenant_id = $1 AND st.site_id = $2 AND st.effective_checkout_at >= $3 AND st.effective_checkout_at < $4`)
	}
	if sc.Staff && len(sc.StaffActions) > 0 {
		acts := arg(sc.StaffActions)
		parts = append(parts,
			`SELECT al.ts, 'staff', al.action, 'info', COALESCE(al.payload->>'reason',''), `+staffClientOf+`,
	       COALESCE(s.mac::text, d.mac::text, ''), COALESCE(host(s.ip),''), '', COALESCE(s.id::text,''), COALESCE(e.id::text,''), COALESCE(st.id::text,''),
	       COALESCE(NULLIF(op.display_name,''), op.email, al.actor_type, ''), 'x1' || al.ts::text || al.action || COALESCE(al.target_id,'')
	  FROM public.audit_log al
	  LEFT JOIN public.operators op ON al.actor_type = 'operator' AND op.id::text = al.actor_id
	  LEFT JOIN iam_v2.sessions s ON al.target_type = 'session' AND s.tenant_id = $1 AND s.site_id = $2 AND s.id::text = al.target_id
	  LEFT JOIN iam_v2.devices d ON al.target_type = 'device' AND d.tenant_id = $1 AND d.site_id = $2 AND d.id::text = al.target_id
	  LEFT JOIN iam_v2.guest_access_accounts gat ON al.target_type = 'guest_account' AND gat.tenant_id = $1 AND gat.site_id = $2 AND gat.id::text = al.target_id`+
				entitlementJoins("COALESCE(s.entitlement_id, CASE WHEN al.target_type = 'entitlement' AND al.target_id ~ '^[0-9a-f-]{36}$' THEN al.target_id::uuid END)")+`
	 WHERE al.tenant_id = $1 AND al.ts >= $3 AND al.ts < $4 AND al.action = ANY(`+acts+`::text[])`)
	}
	for i, p := range parts {
		// Name the columns once, on the first branch, so every branch lines up with clientActivityCols.
		if i == 0 {
			parts[i] = `SELECT * FROM (` + p + `) AS b0(` + clientActivityCols + `)`
		}
	}
	return strings.Join(parts, "\n\tUNION ALL\n\t")
}

// clientActivitySQL: the filtered union as CTE u (category, problems and search applied), paged or counted by the caller.
func clientActivitySQL(f clientActivityFilter, sc clientActivityScope, tenantID, siteID string, withCategory bool) (string, []any) {
	args := []any{tenantID, siteID, f.From, f.To}
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	src := clientActivitySources(sc, &args)
	where := []string{"a.at IS NOT NULL"}
	if withCategory && f.Category != "" {
		where = append(where, "a.cat = "+arg(f.Category))
	}
	if f.Problems {
		where = append(where, "a.sev IN ('warning','error')")
	}
	if f.Q != "" {
		p := arg(likeEscape(f.Q))
		where = append(where, "(a.client_label ILIKE '%' || "+p+" || '%' OR a.mac ILIKE '%' || "+p+" || '%' OR a.ip ILIKE '%' || "+p+
			" || '%' OR a.package ILIKE '%' || "+p+" || '%' OR a.actor ILIKE '%' || "+p+" || '%')")
	}
	return `WITH a AS (
	` + src + `
	), u AS (SELECT a.* FROM a WHERE ` + strings.Join(where, " AND ") + `)`, args
}

type clientActivityRow struct {
	At            time.Time `json:"at"`
	Category      string    `json:"category"`
	Kind          string    `json:"kind"`
	Severity      string    `json:"severity"`
	Detail        string    `json:"detail,omitempty"`
	ClientKind    string    `json:"client_kind,omitempty"`
	ClientLabel   string    `json:"client_label,omitempty"`
	Package       string    `json:"package,omitempty"`
	MAC           string    `json:"mac,omitempty"`
	IP            string    `json:"ip,omitempty"`
	Network       string    `json:"network,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	EntitlementID string    `json:"entitlement_id,omitempty"`
	StayID        string    `json:"stay_id,omitempty"`
	Actor         string    `json:"actor,omitempty"`
	ID            string    `json:"id"`
}

type clientActivityResponse struct {
	Data       []clientActivityRow `json:"data"`
	Meta       activityMeta        `json:"meta"`
	Total      int64               `json:"total"`
	Page       int                 `json:"page"`
	PageSize   int                 `json:"page_size"`
	Categories map[string]int64    `json:"categories"`
	Problems   int64               `json:"problems"`
	Range      activityRange       `json:"range"`
	// Sources says what this reader is shown, so the page can say what is left out.
	Sources map[string]bool `json:"sources"`
}

func (s *server) listClientActivity(w http.ResponseWriter, r *http.Request) {
	f, err := parseClientActivityFilter(r.URL.Query(), time.Now())
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if f.Q, err = boundedSearch(headerSearchText(r, clientActivitySearchHeader)); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var roles []string
	if sess := sessFrom(r.Context()); sess != nil {
		roles = sess.Roles
	}
	sc := clientActivityScopeFor(roles, s.roomSignInInScope(r.Context()))
	ctx, cancel := dbCtx(r)
	defer cancel()

	out := clientActivityResponse{Data: []clientActivityRow{}, Categories: map[string]int64{},
		Range: activityRange{From: f.From, To: f.To},
		Sources: map[string]bool{"hotel": sc.Hotel, "room_sign_ins": sc.SignIns,
			"stays": sc.Stays, "accounts": sc.Accounts, "staff_actions": sc.Staff}}
	out.Meta.Limit, out.Meta.Offset = f.Limit, f.Offset

	base, args := clientActivitySQL(f, sc, s.tenantID, s.siteID, true)
	args = append(args, f.Limit, f.Offset)
	lim, off := "$"+strconv.Itoa(len(args)-1), "$"+strconv.Itoa(len(args))
	rows, err := s.db.Query(ctx, base+`
	SELECT `+clientActivityCols+`, count(*) OVER () FROM u ORDER BY at DESC, rid LIMIT `+lim+` OFFSET `+off, args...)
	if err != nil {
		slog.Error("client activity read failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "client activity could not be read")
		return
	}
	for rows.Next() {
		var x clientActivityRow
		var total int64
		if err := rows.Scan(&x.At, &x.Category, &x.Kind, &x.Severity, &x.Detail, &x.ClientKind, &x.ClientLabel, &x.Package,
			&x.MAC, &x.IP, &x.Network, &x.SessionID, &x.EntitlementID, &x.StayID, &x.Actor, &x.ID, &total); err != nil {
			rows.Close()
			slog.Error("client activity scan failed", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal", "client activity could not be read")
			return
		}
		out.Meta.Total = total
		out.Data = append(out.Data, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Error("client activity read failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "client activity could not be read")
		return
	}
	out.Meta.HasMore = int64(f.Offset+len(out.Data)) < out.Meta.Total

	// Chip counts ignore the chosen category, so every chip shows what it would list.
	sumBase, sumArgs := clientActivitySQL(f, sc, s.tenantID, s.siteID, false)
	crow, err := s.db.Query(ctx, sumBase+`
	SELECT cat, count(*), count(*) FILTER (WHERE sev IN ('warning','error')) FROM u GROUP BY cat`, sumArgs...)
	if err != nil {
		slog.Error("client activity summary failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "client activity could not be summarised")
		return
	}
	for crow.Next() {
		var cat string
		var n, bad int64
		if err := crow.Scan(&cat, &n, &bad); err != nil {
			crow.Close()
			jsonErr(w, http.StatusInternalServerError, "internal", "client activity could not be summarised")
			return
		}
		out.Categories[cat] = n
		out.Problems += bad
	}
	crow.Close()
	if len(out.Data) == 0 && f.Offset > 0 {
		cntSQL, cntArgs := clientActivitySQL(f, sc, s.tenantID, s.siteID, true)
		_ = s.db.QueryRow(ctx, cntSQL+` SELECT count(*) FROM u`, cntArgs...).Scan(&out.Meta.Total)
		out.Meta.HasMore = false
	}
	out.Total, out.PageSize, out.Page = out.Meta.Total, f.Limit, f.Offset/f.Limit+1
	writeJSON(w, http.StatusOK, out)
}

func boundedSearch(v string) (string, error) {
	if len([]rune(v)) > clientActivityMaxQ {
		return "", fmt.Errorf("search is limited to %d characters", clientActivityMaxQ)
	}
	return v, nil
}
