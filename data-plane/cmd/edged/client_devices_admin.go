package main

// Device actions are three distinct things, not one "reset": disconnect ends sessions only; release frees the
// device's place on one package (package time, data, owner and history untouched); require-signin forgets the
// remembered sign-in. None deletes a record, resets a quota or re-enables the one-time free package (that rule
// reads purchase history, which nothing here changes). prepare-test is all three.
//
// Governed by the "sessions" key; the per-day change setting by "auth-methods".

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const deviceSearchHeader = "X-Device-Search"

type devicePackage struct {
	EntitlementID string `json:"entitlement_id"`
	Status        string `json:"status"`
	PackageName   string `json:"package_name"`
	Free          bool   `json:"free"`
	SubjectKind   string `json:"subject_kind"` // ACCOUNT | VOUCHER | ROOM | CLIENT | OPEN
	SubjectLabel  string `json:"subject_label,omitempty"`
	// The PMS connection a room stay came from (a room number only identifies a room together with it).
	PMSConnection   string     `json:"pms_interface_label,omitempty"`
	AccountID       string     `json:"account_id,omitempty"`
	Slot            string     `json:"slot"` // AUTHORIZED (holds a place) | DISCONNECTED (released)
	SlotReason      string     `json:"slot_reason,omitempty"`
	FirstAuthorized *time.Time `json:"first_authorized,omitempty"`
	LastAuthorized  *time.Time `json:"last_authorized,omitempty"`
	WindowEndsAt    *time.Time `json:"window_ends_at,omitempty"`
	ConsumedBytes   int64      `json:"consumed_data_bytes"`
	DataQuotaBytes  *int64     `json:"data_quota_bytes,omitempty"`
	MaxDevices      *int       `json:"max_devices,omitempty"`
	PlacesInUse     int        `json:"places_in_use"`
}

type deviceRow struct {
	// ID is empty for a device on the Wi-Fi that has never signed in (records are created at sign-in).
	ID        string         `json:"id"`
	MAC       string         `json:"mac"`
	LastIP    string         `json:"last_ip,omitempty"`
	FirstSeen *time.Time     `json:"first_seen,omitempty"`
	LastSeen  *time.Time     `json:"last_seen,omitempty"`
	Online    bool           `json:"online"` // signed in: a live Internet session
	OnlineIP  string         `json:"online_ip,omitempty"`
	Current   *devicePackage `json:"current,omitempty"`
	// OnNetwork: holds an active DHCP lease, signed in or not.
	OnNetwork    bool       `json:"on_network"`
	LeaseIP      string     `json:"lease_ip,omitempty"`
	Hostname     string     `json:"hostname,omitempty"`
	LeaseExpires *time.Time `json:"lease_expires,omitempty"`
}

type deviceLease struct {
	IP       string
	Hostname string
	Expires  time.Time
}

// activeLeases maps lower-case MAC to its active lease. On the Wi-Fi is not signed in: a lease starts at join, a
// session at sign-in. nil when netd cannot be asked (the screen still answers).
func (s *server) activeLeases(ctx context.Context) map[string]deviceLease {
	if s.netd == nil {
		return nil
	}
	st, raw, err := s.netd.call(ctx, http.MethodGet, "/v1/leases", nil)
	if err != nil || st != http.StatusOK {
		return nil
	}
	return parseActiveLeases(raw, time.Now())
}

func parseActiveLeases(raw []byte, now time.Time) map[string]deviceLease {
	var v struct {
		Leases []struct {
			IP       string `json:"ip-address"`
			MAC      string `json:"hw-address"`
			Hostname string `json:"hostname"`
			State    int    `json:"state"`
			CLTT     int64  `json:"cltt"`
			Valid    int64  `json:"valid-lft"`
		} `json:"leases"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	out := map[string]deviceLease{}
	for _, l := range v.Leases {
		exp := time.Unix(l.CLTT+l.Valid, 0)
		if l.State != 0 || l.MAC == "" || !exp.After(now) { // Kea state 0 = assigned
			continue
		}
		out[strings.ToLower(l.MAC)] = deviceLease{IP: l.IP, Hostname: l.Hostname, Expires: exp}
	}
	return out
}

func (d *deviceRow) withLease(leases map[string]deviceLease) {
	if l, ok := leases[strings.ToLower(d.MAC)]; ok {
		d.OnNetwork, d.LeaseIP, d.Hostname = true, l.IP, l.Hostname
		exp := l.Expires
		d.LeaseExpires = &exp
	}
}

// devicePackageSelect: $1 tenant, $2 site.
const devicePackageSelect = `
	    SELECT e.id::text, e.status, COALESCE(NULLIF(ipr.display->>'name',''), ''), COALESCE(ipr.price_minor,0) = 0,
	           CASE WHEN e.guest_account_id IS NOT NULL THEN 'ACCOUNT' WHEN e.voucher_id IS NOT NULL THEN 'VOUCHER'
	                WHEN e.stay_id IS NOT NULL THEN 'ROOM' WHEN e.guest_principal_id IS NOT NULL THEN 'CLIENT'
	                ELSE 'OPEN' END,
	           COALESCE(ga.username, st.normalized_room_number, ''), COALESCE(e.guest_account_id::text,''),
	           ed.status, COALESCE(ed.disconnected_reason,''), ed.first_authorized, ed.last_authorized,
	           e.window_ends_at, e.consumed_data_bytes, e.data_quota_bytes, spr.max_concurrent_devices,
	           (SELECT count(*)::int FROM iam_v2.entitlement_devices x WHERE x.entitlement_id = e.id AND x.status = 'AUTHORIZED'),
	           COALESCE((SELECT pi.display_label FROM iam_v2.pms_interfaces pi WHERE pi.id = st.pms_interface_id), '')
	      FROM iam_v2.entitlement_devices ed
	      JOIN iam_v2.entitlements e ON e.id = ed.entitlement_id
	      LEFT JOIN iam_v2.internet_package_revisions ipr ON ipr.id = e.package_revision_id
	      LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
	      LEFT JOIN iam_v2.guest_access_accounts ga ON ga.id = e.guest_account_id
	      LEFT JOIN iam_v2.stays st ON st.id = e.stay_id
	     WHERE ed.tenant_id = $1 AND ed.site_id = $2`

func scanDevicePackage(row interface{ Scan(...any) error }, p *devicePackage) error {
	return row.Scan(&p.EntitlementID, &p.Status, &p.PackageName, &p.Free, &p.SubjectKind, &p.SubjectLabel, &p.AccountID,
		&p.Slot, &p.SlotReason, &p.FirstAuthorized, &p.LastAuthorized, &p.WindowEndsAt, &p.ConsumedBytes,
		&p.DataQuotaBytes, &p.MaxDevices, &p.PlacesInUse, &p.PMSConnection)
}

func (s *server) clientDevicesAdminRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listClientDevices)
	r.Get("/{id}", s.getClientDevice)
	r.Post("/{id}/disconnect", s.disconnectClientDevice)
	r.Post("/{id}/release", s.releaseClientDeviceSlot)
	r.Post("/{id}/require-signin", s.requireClientDeviceSignIn)
	r.Post("/{id}/prepare-test", s.prepareClientDeviceForTest)
	return r
}

func (s *server) listClientDevices(w http.ResponseWriter, r *http.Request) {
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "devices")
		return
	}
	var search any
	needle := strings.ToLower(headerSearchText(r, deviceSearchHeader))
	if v := needle; v != "" {
		if len([]rune(v)) > 100 {
			jsonErr(w, http.StatusBadRequest, "bad_request", "search is limited to 100 characters")
			return
		}
		search = "%" + likeEscape(strings.ToLower(v)) + "%"
	}
	page := 1
	if v := r.URL.Query().Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > pageMax {
			jsonErr(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("page must be a whole number from 1 to %d", pageMax))
			return
		}
		page = n
	}
	const size = 50
	ctx, cancel := dbCtx(r)
	defer cancel()
	// Order: signed in, then on the Wi-Fi, then the rest by last sign-in.
	leases := s.activeLeases(ctx)
	onWiFi := make([]string, 0, len(leases))
	for m := range leases {
		onWiFi = append(onWiFi, m)
	}
	rows, err := s.db.Query(ctx, `
	    SELECT d.id::text, d.mac::text, COALESCE(host(d.last_ip),''), d.first_seen, d.last_seen,
	           live.ip IS NOT NULL, COALESCE(live.ip,'')
	      FROM iam_v2.devices d
	      LEFT JOIN LATERAL (SELECT host(x.ip) AS ip FROM iam_v2.sessions x
	                          WHERE x.device_id = d.id AND x.state = 'active' AND x.ended IS NULL
	                          ORDER BY x.started DESC LIMIT 1) live ON true
	     WHERE d.tenant_id = $1 AND d.site_id = $2
	       AND ($3::text IS NULL OR lower(d.mac::text) LIKE $3 OR COALESCE(host(d.last_ip),'') LIKE $3
	            OR EXISTS (SELECT 1 FROM iam_v2.entitlement_devices ed
	                         JOIN iam_v2.entitlements e ON e.id = ed.entitlement_id
	                         JOIN iam_v2.guest_access_accounts ga ON ga.id = e.guest_account_id
	                        WHERE ed.device_id = d.id AND lower(ga.username) LIKE $3))
	     ORDER BY (live.ip IS NOT NULL) DESC, (lower(d.mac::text) = ANY($6::text[])) DESC, d.last_seen DESC NULLS LAST, d.id
	     LIMIT $4 OFFSET $5`, s.tenantID, s.siteID, search, size+1, (page-1)*size, onWiFi)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "devices could not be listed")
		return
	}
	out := []deviceRow{}
	for rows.Next() {
		var d deviceRow
		if err := rows.Scan(&d.ID, &d.MAC, &d.LastIP, &d.FirstSeen, &d.LastSeen, &d.Online, &d.OnlineIP); err != nil {
			rows.Close()
			jsonErr(w, http.StatusInternalServerError, "internal", "devices could not be listed")
			return
		}
		out = append(out, d)
	}
	rows.Close()
	// A read cut off mid-way (the 10 s dbCtx) ends rows.Next without an error; it must not read as "no devices"
	// (performance audit D7-M2).
	if err := rows.Err(); err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "read_incomplete", "the device list could not be read in time; try again")
		return
	}
	more := len(out) > size
	if more {
		out = out[:size]
	}
	// On the first page, devices on the Wi-Fi that have never signed in (no record yet) are listed first.
	for i := range out {
		out[i].withLease(leases)
	}
	if page == 1 && len(leases) > 0 {
		macs := make([]string, 0, len(leases))
		for m := range leases {
			macs = append(macs, m)
		}
		known := map[string]bool{}
		if krows, err := s.db.Query(ctx, `SELECT lower(mac::text) FROM iam_v2.devices
		     WHERE tenant_id=$1 AND site_id=$2 AND mac = ANY($3::macaddr[])`, s.tenantID, s.siteID, macs); err == nil {
			for krows.Next() {
				var m string
				if krows.Scan(&m) == nil {
					known[m] = true
				}
			}
			krows.Close()
		}
		var fresh []deviceRow
		for m, l := range leases {
			if known[m] {
				continue
			}
			if needle != "" && !strings.Contains(m, needle) && !strings.Contains(l.IP, needle) &&
				!strings.Contains(strings.ToLower(l.Hostname), needle) {
				continue
			}
			d := deviceRow{MAC: m}
			d.withLease(leases)
			fresh = append(fresh, d)
		}
		sort.Slice(fresh, func(i, j int) bool { return fresh[i].LeaseIP < fresh[j].LeaseIP })
		out = append(fresh, out...)
	}
	for i := range out {
		if out[i].ID == "" {
			continue
		}
		var p devicePackage
		if err := scanDevicePackage(s.db.QueryRow(ctx, devicePackageSelect+`
		       AND ed.device_id = $3::uuid
		     ORDER BY (ed.status = 'AUTHORIZED' AND e.status IN ('ACTIVE','PENDING','SUSPENDED')) DESC,
		              ed.last_authorized DESC NULLS LAST LIMIT 1`, s.tenantID, s.siteID, out[i].ID), &p); err == nil {
			out[i].Current = &p
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "meta": listMeta{HasMore: more}, "page": page})
}

type deviceSessionRow struct {
	ID        string     `json:"id"`
	Started   *time.Time `json:"started,omitempty"`
	Ended     *time.Time `json:"ended,omitempty"`
	State     string     `json:"state"`
	EndReason string     `json:"end_reason,omitempty"`
	Method    string     `json:"method,omitempty"`
	IP        string     `json:"ip,omitempty"`
	Package   string     `json:"package_name,omitempty"`
}

type deviceActionRow struct {
	ActedAt  time.Time `json:"acted_at"`
	Actor    string    `json:"actor"`
	Operator string    `json:"operator,omitempty"`
	Action   string    `json:"action"`
	Outcome  string    `json:"outcome"`
	Reason   string    `json:"reason,omitempty"`
}

func (s *server) loadDevice(ctx context.Context, id string) (deviceRow, bool) {
	var d deviceRow
	err := s.db.QueryRow(ctx, `
	    SELECT d.id::text, d.mac::text, COALESCE(host(d.last_ip),''), d.first_seen, d.last_seen,
	           EXISTS (SELECT 1 FROM iam_v2.sessions x WHERE x.device_id = d.id AND x.state = 'active' AND x.ended IS NULL)
	      FROM iam_v2.devices d WHERE d.id = $1::uuid AND d.tenant_id = $2 AND d.site_id = $3`,
		id, s.tenantID, s.siteID).Scan(&d.ID, &d.MAC, &d.LastIP, &d.FirstSeen, &d.LastSeen, &d.Online)
	return d, err == nil
}

func (s *server) getClientDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	d, ok := s.loadDevice(ctx, id)
	if !ok {
		jsonErr(w, http.StatusNotFound, "not_found", "device not found")
		return
	}
	pkgs := []devicePackage{}
	rows, err := s.db.Query(ctx, devicePackageSelect+` AND ed.device_id = $3::uuid
	     ORDER BY (e.status IN ('ACTIVE','PENDING','SUSPENDED')) DESC, ed.last_authorized DESC NULLS LAST LIMIT 20`,
		s.tenantID, s.siteID, id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the device's packages could not be read")
		return
	}
	for rows.Next() {
		var p devicePackage
		if scanDevicePackage(rows, &p) == nil {
			pkgs = append(pkgs, p)
		}
	}
	rows.Close()
	if rows.Err() != nil {
		jsonErr(w, http.StatusServiceUnavailable, "read_incomplete", "the device's packages could not be read in time; try again")
		return
	}

	sessions := []deviceSessionRow{}
	srows, err := s.db.Query(ctx, `
	    SELECT x.id::text, x.started, x.ended, x.state, COALESCE(x.end_reason,''), COALESCE(x.credential_method,''),
	           COALESCE(host(x.ip),''), COALESCE(NULLIF(ipr.display->>'name',''),'')
	      FROM iam_v2.sessions x
	      LEFT JOIN iam_v2.entitlements e ON e.id = x.entitlement_id
	      LEFT JOIN iam_v2.internet_package_revisions ipr ON ipr.id = e.package_revision_id
	     WHERE x.device_id = $1::uuid AND x.tenant_id = $2 AND x.site_id = $3
	     ORDER BY x.started DESC NULLS LAST LIMIT 20`, id, s.tenantID, s.siteID)
	if err == nil {
		for srows.Next() {
			var x deviceSessionRow
			if srows.Scan(&x.ID, &x.Started, &x.Ended, &x.State, &x.EndReason, &x.Method, &x.IP, &x.Package) == nil {
				sessions = append(sessions, x)
			}
		}
		srows.Close()
	}

	restriction := map[string]any{"restricted": false}
	var until *time.Time
	var failures *int
	if err := s.db.QueryRow(ctx, `SELECT expires_at, failure_count FROM iam_v2.guest_signin_restrictions
	     WHERE tenant_id=$1 AND site_id=$2 AND device_mac=$3::macaddr AND released_at IS NULL AND expires_at > now()`,
		s.tenantID, s.siteID, d.MAC).Scan(&until, &failures); err == nil {
		restriction = map[string]any{"restricted": true, "until": until, "failures": failures}
	}
	var remembered int
	_ = s.db.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.principal_device_credentials
	     WHERE tenant_id=$1 AND site_id=$2 AND device_id=$3::uuid AND revoked_at IS NULL AND expires_at > now()`,
		s.tenantID, s.siteID, id).Scan(&remembered)
	freeUsed := 0
	for _, p := range pkgs {
		if p.Free {
			freeUsed++
		}
	}
	actions := []deviceActionRow{}
	arows, err := s.db.Query(ctx, `SELECT acted_at, actor, COALESCE(operator_label,''), action, outcome, COALESCE(reason,'')
	     FROM iam_v2.device_slot_actions WHERE tenant_id=$1 AND site_id=$2 AND (device_id=$3::uuid OR replacement_device_id=$3::uuid)
	     ORDER BY acted_at DESC LIMIT 10`, s.tenantID, s.siteID, id)
	if err == nil {
		for arows.Next() {
			var a deviceActionRow
			if arows.Scan(&a.ActedAt, &a.Actor, &a.Operator, &a.Action, &a.Outcome, &a.Reason) == nil {
				actions = append(actions, a)
			}
		}
		arows.Close()
	}
	d.withLease(s.activeLeases(ctx))
	writeJSON(w, http.StatusOK, map[string]any{
		"device": d, "packages": pkgs, "sessions": sessions, "restriction": restriction,
		"remembered_signins": remembered, "free_packages_used": freeUsed, "actions": actions,
	})
}

// endDeviceSessions ends live sessions (optionally one package's) via scd and reads back what ended. A non-zero
// status is a refusal issued before anything was ended.
func (s *server) endDeviceSessions(r *http.Request, ctx context.Context, deviceID, entitlementID string) (int, int, string, string) {
	q := `SELECT x.id::text, COALESCE(host(x.ip),'') FROM iam_v2.sessions x
	     WHERE x.tenant_id=$1 AND x.site_id=$2 AND x.device_id=$3::uuid AND x.ended IS NULL
	       AND x.state IN ('active','PENDING_ENFORCEMENT')`
	args := []any{s.tenantID, s.siteID, deviceID}
	if entitlementID != "" {
		q += ` AND x.entitlement_id = $4::uuid`
		args = append(args, entitlementID)
	}
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return 0, http.StatusInternalServerError, "internal", "the device's sessions could not be read"
	}
	var ids, ips []string
	for rows.Next() {
		var id, ip string
		if rows.Scan(&id, &ip) == nil {
			ids = append(ids, id)
			ips = append(ips, ip)
		}
	}
	rows.Close()
	if rows.Err() != nil {
		// A partial read would disconnect only some of the device's sessions and report success.
		return 0, http.StatusServiceUnavailable, "read_incomplete", "the device's sessions could not be read in time; nothing was changed"
	}
	if len(ids) == 0 {
		return 0, 0, "", ""
	}
	if s.scd == nil {
		return 0, http.StatusServiceUnavailable, "enforcement_unavailable",
			"this appliance cannot withdraw network access right now, so nothing was changed"
	}
	for _, ip := range ips {
		if ip == "" {
			return 0, http.StatusConflict, "conflict",
				"one of this device's sessions has no recorded address, so it cannot be disconnected from here; end it from Active sessions"
		}
	}
	seen := map[string]bool{}
	for _, ip := range ips {
		if !seen[ip] {
			seen[ip] = true
			_, _, _ = s.scd.call(r.Context(), http.MethodPost, "/v1/sessions/revoke", map[string]string{"ip": ip, "reason": "admin"})
		}
	}
	var ended int
	_ = s.db.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.sessions WHERE id = ANY($1::uuid[]) AND state='ended'`, ids).Scan(&ended)
	if ended < len(ids) {
		return ended, http.StatusBadGateway, "not_disconnected",
			fmt.Sprintf("%d of the device's %d live sessions could not be ended; nothing else was changed", len(ids)-ended, len(ids))
	}
	return ended, 0, "", ""
}

type deviceActionIn struct {
	EntitlementID string `json:"entitlement_id,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// deviceActor is the session's operator, never a caller-supplied field.
func deviceActor(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	sess := sessFrom(r.Context())
	if sess == nil || sess.OperatorID == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "no operator session")
		return "", "", false
	}
	label := strings.TrimSpace(sess.DisplayName)
	if label == "" {
		label = strings.TrimSpace(sess.Email)
	}
	if label == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "the operator session carries no identity")
		return "", "", false
	}
	return sess.OperatorID, label, true
}

func (s *server) deviceActionStart(w http.ResponseWriter, r *http.Request, needReason bool) (deviceRow, deviceActionIn, context.Context, context.CancelFunc, bool) {
	var in deviceActionIn
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &in); err != nil {
			jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
			return deviceRow{}, in, nil, nil, false
		}
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if needReason && !validReason(in.Reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a reason of 4-500 characters is required")
		return deviceRow{}, in, nil, nil, false
	}
	ctx, cancel := dbCtx(r)
	d, ok := s.loadDevice(ctx, chi.URLParam(r, "id"))
	if !ok {
		cancel()
		jsonErr(w, http.StatusNotFound, "not_found", "device not found")
		return deviceRow{}, in, nil, nil, false
	}
	return d, in, ctx, cancel, true
}

func (s *server) disconnectClientDevice(w http.ResponseWriter, r *http.Request) {
	d, _, ctx, cancel, ok := s.deviceActionStart(w, r, false)
	if !ok {
		return
	}
	defer cancel()
	n, st, code, msg := s.endDeviceSessions(r, ctx, d.ID, "")
	if st != 0 {
		jsonErr(w, st, code, msg)
		return
	}
	if n > 0 {
		s.audit(r, "client_device.disconnected", "device", d.ID, map[string]any{"disconnected_sessions": n})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "disconnected", "disconnected_sessions": n})
}

func (s *server) releaseOne(r *http.Request, ctx context.Context, deviceID, ent, opID, opLabel, reason string) (string, int, string, string) {
	if _, st, code, msg := s.endDeviceSessions(r, ctx, deviceID, ent); st != 0 {
		return "", st, code, msg
	}
	var outcome string
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.staff_release_device_slot($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6,$7)`,
		s.tenantID, s.siteID, ent, deviceID, opID, opLabel, reason).Scan(&outcome); err != nil {
		if isUndefinedObject(err) {
			return "", http.StatusServiceUnavailable, "not_available", "this appliance's database does not have device management yet"
		}
		return "", http.StatusInternalServerError, "internal", "the place could not be released"
	}
	return outcome, 0, "", ""
}

var releaseRefusalWords = map[string]string{
	"REFUSED_NO_PACKAGE":       "that package is not at this site",
	"REFUSED_NOT_FOUND":        "this device has never used that package",
	"REFUSED_ALREADY_RELEASED": "this device's place on that package is already free",
	"REFUSED_ONLINE":           "the device came back online while it was being released; try again",
}

func (s *server) releaseClientDeviceSlot(w http.ResponseWriter, r *http.Request) {
	d, in, ctx, cancel, ok := s.deviceActionStart(w, r, true)
	if !ok {
		return
	}
	defer cancel()
	if in.EntitlementID == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "entitlement_id is required")
		return
	}
	opID, opLabel, ok := deviceActor(w, r)
	if !ok {
		return
	}
	outcome, st, code, msg := s.releaseOne(r, ctx, d.ID, in.EntitlementID, opID, opLabel, in.Reason)
	if st != 0 {
		jsonErr(w, st, code, msg)
		return
	}
	s.audit(r, "client_device.place_released", "device", d.ID, map[string]any{
		"entitlement_id": in.EntitlementID, "outcome": outcome, "reason": in.Reason})
	if outcome != "OK" {
		jsonErr(w, http.StatusConflict, strings.ToLower(outcome), releaseRefusalWords[outcome])
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "released"})
}

func (s *server) requireSignIn(ctx context.Context, deviceID, opID, opLabel, reason string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT iam_v2.staff_require_device_signin($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6)`,
		s.tenantID, s.siteID, deviceID, opID, opLabel, reason).Scan(&n)
	return n, err
}

func (s *server) requireClientDeviceSignIn(w http.ResponseWriter, r *http.Request) {
	d, in, ctx, cancel, ok := s.deviceActionStart(w, r, true)
	if !ok {
		return
	}
	defer cancel()
	opID, opLabel, ok := deviceActor(w, r)
	if !ok {
		return
	}
	ended, st, code, msg := s.endDeviceSessions(r, ctx, d.ID, "")
	if st != 0 {
		jsonErr(w, st, code, msg)
		return
	}
	n, err := s.requireSignIn(ctx, d.ID, opID, opLabel, in.Reason)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the remembered sign-in could not be forgotten")
		return
	}
	s.audit(r, "client_device.signin_required", "device", d.ID, map[string]any{
		"disconnected_sessions": ended, "remembered_signins_revoked": n, "reason": in.Reason})
	writeJSON(w, http.StatusOK, map[string]any{"status": "signin_required", "disconnected_sessions": ended,
		"remembered_signins_revoked": n})
}

// prepareClientDeviceForTest runs all three actions for a device reused in a sign-in test; package history, and
// so the one-time free package rule, is unchanged.
func (s *server) prepareClientDeviceForTest(w http.ResponseWriter, r *http.Request) {
	d, in, ctx, cancel, ok := s.deviceActionStart(w, r, true)
	if !ok {
		return
	}
	defer cancel()
	opID, opLabel, ok := deviceActor(w, r)
	if !ok {
		return
	}
	ended, st, code, msg := s.endDeviceSessions(r, ctx, d.ID, "")
	if st != 0 {
		jsonErr(w, st, code, msg)
		return
	}
	rows, err := s.db.Query(ctx, `SELECT ed.entitlement_id::text FROM iam_v2.entitlement_devices ed
	     JOIN iam_v2.entitlements e ON e.id = ed.entitlement_id
	     WHERE ed.tenant_id=$1 AND ed.site_id=$2 AND ed.device_id=$3::uuid AND ed.status='AUTHORIZED'
	       AND e.status IN ('ACTIVE','PENDING','SUSPENDED')`, s.tenantID, s.siteID, d.ID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the device's places could not be read")
		return
	}
	var ents []string
	for rows.Next() {
		var e string
		if rows.Scan(&e) == nil {
			ents = append(ents, e)
		}
	}
	rows.Close()
	released := 0
	for _, e := range ents {
		outcome, st, code, msg := s.releaseOne(r, ctx, d.ID, e, opID, opLabel, in.Reason)
		if st != 0 {
			jsonErr(w, st, code, msg)
			return
		}
		if outcome == "OK" {
			released++
		}
	}
	n, err := s.requireSignIn(ctx, d.ID, opID, opLabel, in.Reason)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the remembered sign-in could not be forgotten")
		return
	}
	var freeUsed int
	_ = s.db.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.entitlement_devices ed
	     JOIN iam_v2.entitlements e ON e.id = ed.entitlement_id
	     JOIN iam_v2.internet_package_revisions ipr ON ipr.id = e.package_revision_id
	     WHERE ed.device_id = $1::uuid AND ipr.price_minor = 0`, d.ID).Scan(&freeUsed)
	s.audit(r, "client_device.prepared_for_test", "device", d.ID, map[string]any{
		"disconnected_sessions": ended, "places_released": released, "remembered_signins_revoked": n,
		"reason": in.Reason})
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "prepared", "disconnected_sessions": ended, "places_released": released,
		"remembered_signins_revoked": n, "free_packages_used": freeUsed,
	})
}

type deviceSettings struct {
	ClientDeviceChangesPerDay int `json:"client_device_changes_per_day"`
}

func (s *server) deviceSettingsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getDeviceSettings)
	r.Put("/", s.setDeviceSettings)
	return r
}

func (s *server) getDeviceSettings(w http.ResponseWriter, r *http.Request) {
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "device settings")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var v deviceSettings
	var version int64
	var updatedAt *time.Time
	var updatedBy *string
	if err := s.db.QueryRow(ctx, `SELECT client_device_changes_per_day, config_version, updated_at, updated_by
	     FROM iam_v2.device_settings_get($1::uuid,$2::uuid)`, s.tenantID, s.siteID).
		Scan(&v.ClientDeviceChangesPerDay, &version, &updatedAt, &updatedBy); err != nil {
		if isUndefinedObject(err) {
			jsonErr(w, http.StatusServiceUnavailable, "not_available", "this appliance's database does not have device management yet")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "the device settings could not be read")
		return
	}
	type change struct {
		ChangedAt time.Time `json:"changed_at"`
		ChangedBy string    `json:"changed_by"`
		Reason    string    `json:"reason"`
		Old       *int      `json:"old"`
		New       int       `json:"new"`
	}
	changes := []change{}
	rows, err := s.db.Query(ctx, `SELECT changed_at, changed_by, change_reason, old_client_device_changes_per_day,
	     new_client_device_changes_per_day FROM iam_v2.device_settings_changes
	     WHERE tenant_id=$1 AND site_id=$2 ORDER BY changed_at DESC LIMIT 20`, s.tenantID, s.siteID)
	if err == nil {
		for rows.Next() {
			var c change
			if rows.Scan(&c.ChangedAt, &c.ChangedBy, &c.Reason, &c.Old, &c.New) == nil {
				changes = append(changes, c)
			}
		}
		rows.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": v, "config_version": version, "updated_at": updatedAt, "updated_by": updatedBy,
		"bounds": map[string]any{"client_device_changes_per_day": map[string]any{
			"min": 1, "max": 10, "default": 3, "unit": "changes per package per day"}},
		"changes": changes,
	})
}

func (s *server) setDeviceSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ClientDeviceChangesPerDay *int   `json:"client_device_changes_per_day"`
		Reason                    string `json:"reason"`
		Password                  string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if in.ClientDeviceChangesPerDay == nil || *in.ClientDeviceChangesPerDay < 1 || *in.ClientDeviceChangesPerDay > 10 {
		jsonErr(w, http.StatusBadRequest, "invalid_setting", "client_device_changes_per_day must be 1-10")
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if !validReason(reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason (4-500 characters) is required")
		return
	}
	if s.tenantID == "" || s.siteID == "" {
		jsonErr(w, http.StatusConflict, "awaiting_assignment", "this appliance has no site yet, so nothing can be saved")
		return
	}
	actor, ok := s.stepUp(w, r, in.Password)
	if !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var old int
	_ = s.db.QueryRow(ctx, `SELECT client_device_changes_per_day FROM iam_v2.device_settings_get($1::uuid,$2::uuid)`,
		s.tenantID, s.siteID).Scan(&old)
	var version int64
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.device_settings_set($1::uuid,$2::uuid,$3,$4,$5)`,
		s.tenantID, s.siteID, *in.ClientDeviceChangesPerDay, actor, reason).Scan(&version); err != nil {
		if isUndefinedObject(err) {
			jsonErr(w, http.StatusServiceUnavailable, "not_available", "this appliance's database does not have device management yet")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "update_failed", "the device settings could not be saved")
		return
	}
	s.audit(r, "device_settings.changed", "device_settings", s.siteID, map[string]any{
		"old": old, "new": *in.ClientDeviceChangesPerDay, "config_version": version, "reason": reason})
	writeJSON(w, http.StatusOK, map[string]any{"settings": deviceSettings{*in.ClientDeviceChangesPerDay}, "config_version": version})
}
