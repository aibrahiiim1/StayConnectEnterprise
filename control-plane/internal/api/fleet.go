package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Every appliance state in the fleet reads comes from DeriveState over facts read by queryAppliances.

type LicenseSummary struct {
	ID                        *string    `json:"id"`
	State                     string     `json:"state"`
	ValidUntil                *time.Time `json:"valid_until"`
	GraceEndsAt               *time.Time `json:"grace_ends_at"`
	MaxConcurrentOnlineGuests *int       `json:"max_concurrent_online_guests"`
	GracePeriodDays           *int       `json:"grace_period_days"`
	LicenseVersion            *int64     `json:"license_version"`
	Modules                   []string   `json:"modules"` // null with no licence; [] = core only
}

type ApplianceRow struct {
	ID           string         `json:"id"`
	Serial       string         `json:"serial"`
	Hostname     string         `json:"hostname"`
	Model        string         `json:"model"`
	Version      string         `json:"version"`
	CustomerID   *string        `json:"customer_id"`
	CustomerName *string        `json:"customer_name"`
	SiteID       *string        `json:"site_id"`
	SiteName     *string        `json:"site_name"`
	SiteType     *string        `json:"site_type"` // descriptive only; null when not placed
	Activation   string         `json:"activation"`
	Connection   string         `json:"connection"`
	LastSeenAt   *time.Time     `json:"last_seen_at"`
	LastPublicIP *string        `json:"last_public_ip"`
	License      LicenseSummary `json:"license"`
	RegisteredAt time.Time      `json:"registered_at"`
	ActivatedAt  *time.Time     `json:"activated_at"`
	OpenAlerts   int            `json:"open_alerts"`
	// HoldsCustomerID is the customer whose data the appliance reported holding at its last registration; it
	// activates only for that customer. HoldsCustomerName is null when that customer is gone from Central.
	HoldsCustomerID   *string `json:"holds_customer_id"`
	HoldsCustomerName *string `json:"holds_customer_name"`
	// OfflineRequestPending: an unconsumed offline activation request is on file; without one the package endpoint refuses.
	OfflineRequestPending bool `json:"offline_request_pending"`
	// ActivatingOn says what an "activating" appliance lacks: "certificate_reissue" (revoked for reissue, new CSR
	// not yet sent), "certificate" (never held one) or "license". Empty in every other state.
	ActivatingOn string `json:"activating_on,omitempty"`

	lifecycle          string
	terminalDelivery   string
	terminalDeadline   *time.Time
	replacementPending bool
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// queryAppliances loads appliance rows with their derived state. customerID "" = every customer;
// applianceID "" = every appliance.
func (b *Base) queryAppliances(ctx context.Context, customerID, applianceID string) ([]ApplianceRow, error) {
	rows, _, err := b.queryAppliancesOpt(ctx, applianceQuery{customerID: customerID, applianceID: applianceID})
	return rows, err
}

// applianceQuery narrows queryAppliancesOpt in SQL (AD-55). limit 0 = no paging.
type applianceQuery struct {
	customerID, applianceID, siteID, text string
	limit, offset                         int
}

// queryAppliancesOpt is queryAppliances with SQL filters on site and text and, when q.limit > 0, a page of the
// result. total is the number of rows matching the filters before paging.
func (b *Base) queryAppliancesOpt(ctx context.Context, q applianceQuery) ([]ApplianceRow, int, error) {
	rows, err := b.DB.Query(ctx, `
        SELECT a.id::text, a.serial, COALESCE(a.hostname,''), COALESCE(a.model,''), COALESCE(a.version,''),
               COALESCE(a.tenant_id::text,''), COALESCE(t.name,''), COALESCE(a.site_id::text,''), COALESCE(s.name,''),
               COALESCE(s.site_type,''), a.lifecycle_state, a.last_seen_at, COALESCE(a.last_public_ip,''),
               COALESCE(a.registered_at, a.first_seen_at, a.created_at), a.activated_at,
               EXISTS (SELECT 1 FROM appliance_certificates c WHERE c.appliance_id = a.id AND c.status = 'active'),
               COALESCE(td.delivery_state,''), td.timeout_at, a.replacement_pending,
               (SELECT count(*) FROM appliance_security_alerts x WHERE x.appliance_id = a.id AND NOT x.resolved),
               l.id::text, l.status, l.valid_until, l.grace_period_days, l.offline_grace_days,
               l.max_concurrent_online_guests, l.license_version, l.modules,
               COALESCE(a.held_customer_id::text,''), COALESCE(hc.name,''),
               EXISTS (SELECT 1 FROM offline_activation_requests o WHERE o.appliance_id = a.id AND o.consumed_at IS NULL),
               EXISTS (SELECT 1 FROM appliance_certificates c WHERE c.appliance_id = a.id AND c.status = 'revoked'
                         AND c.revocation_reason LIKE 'reissue:%'),
               count(*) OVER ()
          FROM appliances a
          LEFT JOIN tenants t ON t.id = a.tenant_id
          LEFT JOIN tenants hc ON hc.id = a.held_customer_id
          LEFT JOIN sites   s ON s.id = a.site_id
          LEFT JOIN appliance_terminal_delivery td ON td.appliance_id = a.id
          LEFT JOIN LATERAL (
                SELECT x.id, x.status, x.valid_until, x.grace_period_days, x.offline_grace_days,
                       x.max_concurrent_online_guests, x.license_version, COALESCE(x.modules, '{}') AS modules
                  FROM licenses x
                 -- Every licence names exactly one appliance (licensing.IssueTx), so appliance_ids[1] matches the
                 -- partial indexes of 0041; "= ANY" scanned every licence per appliance (perf audit D9-M2).
                 WHERE x.appliance_ids[1] = a.id AND cardinality(x.appliance_ids) > 0
                   AND x.status IN ('active','suspended','revoked')
                 ORDER BY (x.status IN ('active','suspended')) DESC, x.issued_at DESC
                 LIMIT 1) l ON true
         WHERE ($1 = '' OR a.tenant_id::text = $1)
           AND ($2 = '' OR a.id::text = $2)
           AND ($3 = '' OR a.site_id::text = $3)
           AND ($4 = '' OR position($4 in lower(concat_ws(' ', a.serial, a.hostname, a.model, t.name, s.name, a.last_public_ip))) > 0)
         ORDER BY (a.lifecycle_state = 'pending_approval') DESC, a.created_at DESC, a.id
         LIMIT CASE WHEN $5 > 0 THEN $5 END OFFSET $6`, q.customerID, q.applianceID, q.siteID,
		strings.ToLower(strings.TrimSpace(q.text)), q.limit, q.offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	now := time.Now()
	total := 0
	var out []ApplianceRow
	for rows.Next() {
		var a ApplianceRow
		var custID, custName, siteID, siteName, siteType, ip, td, heldID, heldName string
		var licModules []string
		var hasCert, reissued bool
		var licID, licStatus *string
		var licUntil *time.Time
		var licGrace, licOffline, licCap *int
		var licVer *int64
		if err := rows.Scan(&a.ID, &a.Serial, &a.Hostname, &a.Model, &a.Version,
			&custID, &custName, &siteID, &siteName, &siteType,
			&a.lifecycle, &a.LastSeenAt, &ip, &a.RegisteredAt, &a.ActivatedAt,
			&hasCert, &td, &a.terminalDeadline, &a.replacementPending, &a.OpenAlerts,
			&licID, &licStatus, &licUntil, &licGrace, &licOffline, &licCap, &licVer, &licModules, &heldID, &heldName,
			&a.OfflineRequestPending, &reissued, &total); err != nil {
			return nil, 0, err
		}
		a.HoldsCustomerID, a.HoldsCustomerName = strPtr(heldID), strPtr(heldName)
		a.CustomerID, a.CustomerName, a.SiteID, a.SiteName = strPtr(custID), strPtr(custName), strPtr(siteID), strPtr(siteName)
		a.SiteType = strPtr(siteType)
		a.LastPublicIP = strPtr(ip)
		a.terminalDelivery = td
		facts := ApplianceFacts{Lifecycle: a.lifecycle, TerminalDelivery: td, HasActiveCert: hasCert, LastSeenAt: a.LastSeenAt}
		if licID != nil && licStatus != nil && licUntil != nil {
			lf := &LicenseFacts{Status: *licStatus, ValidUntil: *licUntil}
			if licGrace != nil {
				lf.GracePeriodDays = *licGrace
			}
			if licOffline != nil {
				lf.OfflineGraceDays = *licOffline
			}
			facts.License = lf
			ge := lf.GraceEndsAt()
			a.License = LicenseSummary{ID: licID, ValidUntil: licUntil, GraceEndsAt: &ge,
				MaxConcurrentOnlineGuests: licCap, GracePeriodDays: licGrace, LicenseVersion: licVer, Modules: licModules}
			if a.License.Modules == nil {
				a.License.Modules = []string{}
			}
		}
		d := DeriveState(facts, now)
		a.Activation, a.Connection, a.License.State = d.Activation, d.Connection, d.License
		if a.Activation == ActivationActivating {
			switch {
			case !hasCert && reissued:
				a.ActivatingOn = "certificate_reissue"
			case !hasCert:
				a.ActivatingOn = "certificate"
			default:
				a.ActivatingOn = "license"
			}
		}
		out = append(out, a)
	}
	if q.limit <= 0 || len(out) > 0 {
		return out, total, rows.Err()
	}
	// An offset past the end returns no rows, so the total is read separately.
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	rows.Close()
	err = b.DB.QueryRow(ctx, `SELECT count(*) FROM appliances a
          LEFT JOIN tenants t ON t.id = a.tenant_id LEFT JOIN sites s ON s.id = a.site_id
         WHERE ($1 = '' OR a.tenant_id::text = $1) AND ($2 = '' OR a.id::text = $2) AND ($3 = '' OR a.site_id::text = $3)
           AND ($4 = '' OR position($4 in lower(concat_ws(' ', a.serial, a.hostname, a.model, t.name, s.name, a.last_public_ip))) > 0)`,
		q.customerID, q.applianceID, q.siteID, strings.ToLower(strings.TrimSpace(q.text))).Scan(&total)
	return out, total, err
}

// ApplianceListPageSize is the page size of the paged fleet list (AD-55).
const ApplianceListPageSize = 100

// listAppliances: GET /cloud/v1/appliances?customer_id=&site_id=&activation=&connection=&license=&q=[&page=&limit=]
//
// Unpaged (no page or limit): the whole filtered list, as before. Paged (AD-55): pages of `limit` (default and
// maximum 100), `page` starting at 1, with {total, page, limit, next_page}. Customer, site and text filters run in
// SQL and the page is cut there. The activation, connection and licence filters are derived states (DeriveState),
// so with any of them the SQL-filtered set is derived in Go, filtered, counted and then cut into the page.
func (b *Base) listAppliances(w http.ResponseWriter, r *http.Request) {
	scope, ok := readScope(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	ctx, cancel := DBCtx(r)
	defer cancel()
	customer := scope.Filter(q.Get("customer_id"))
	if q.Get("page") == "" && q.Get("limit") == "" {
		rows, _, err := b.queryAppliancesOpt(ctx, applianceQuery{customerID: customer, siteID: q.Get("site_id"), text: q.Get("q")})
		if err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": filterAppliances(rows, "",
			q.Get("activation"), q.Get("connection"), q.Get("license"), "")})
		return
	}
	limit := ParseLimit(r, ApplianceListPageSize, ApplianceListPageSize)
	page := 1
	if v, err := strconv.Atoi(q.Get("page")); err == nil && v > 1 {
		page = v
	}
	offset := (page - 1) * limit
	derived := q.Get("activation") != "" || q.Get("connection") != "" || q.Get("license") != ""
	aq := applianceQuery{customerID: customer, siteID: q.Get("site_id"), text: q.Get("q")}
	var items []ApplianceRow
	var total int
	if derived {
		rows, _, err := b.queryAppliancesOpt(ctx, aq)
		if err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
			return
		}
		rows = filterAppliances(rows, "", q.Get("activation"), q.Get("connection"), q.Get("license"), "")
		total = len(rows)
		if offset > total {
			offset = total
		}
		end := offset + limit
		if end > total {
			end = total
		}
		items = rows[offset:end]
	} else {
		aq.limit, aq.offset = limit, offset
		var err error
		items, total, err = b.queryAppliancesOpt(ctx, aq)
		if err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
			return
		}
	}
	if items == nil {
		items = []ApplianceRow{}
	}
	var next any
	if offset+len(items) < total {
		next = page + 1
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "page": page, "limit": limit, "next_page": next})
}

func filterAppliances(rows []ApplianceRow, siteID, activation, connection, license, text string) []ApplianceRow {
	text = strings.ToLower(strings.TrimSpace(text))
	out := []ApplianceRow{}
	for _, a := range rows {
		if siteID != "" && deref(a.SiteID) != siteID ||
			activation != "" && a.Activation != activation ||
			connection != "" && a.Connection != connection ||
			license != "" && a.License.State != license {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(strings.Join([]string{a.Serial, a.Hostname, a.Model,
			deref(a.CustomerName), deref(a.SiteName), deref(a.LastPublicIP)}, " ")), text) {
			continue
		}
		out = append(out, a)
	}
	return out
}

func (b *Base) loadOne(w http.ResponseWriter, r *http.Request, scope Scope, id string) (*ApplianceRow, bool) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.queryAppliances(ctx, "", id)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return nil, false
	}
	if len(rows) == 0 || !scope.Allows(deref(rows[0].CustomerID)) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return nil, false
	}
	return &rows[0], true
}

// getAppliance: GET /cloud/v1/appliances/{id}
func (b *Base) getAppliance(w http.ResponseWriter, r *http.Request) {
	scope, ok := readScope(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	row, ok := b.loadOne(w, r, scope, id)
	if !ok {
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	out := map[string]any{}
	raw, _ := json.Marshal(row)
	_ = json.Unmarshal(raw, &out)

	// A failed read is a 503, never a page with empty sections (audit Phase 1 L7).
	unavailable := func(err error) {
		failDB(w, r, "Central could not read this appliance's details. Try again in a moment.", err)
	}
	var wan, lan, hwf, pub, certFpr string
	var certNotAfter *time.Time
	var replOf, replBy *string
	var replDeadline *time.Time
	if err := b.DB.QueryRow(ctx, `
        SELECT COALESCE(wan_mac,''), COALESCE(lan_mac,''), COALESCE(hardware_fingerprint,''), COALESCE(public_key,''),
               COALESCE(current_cert_fingerprint,''), cert_not_after,
               replacement_of::text, replaced_by::text, replacement_deadline
          FROM appliances WHERE id=$1`, id).
		Scan(&wan, &lan, &hwf, &pub, &certFpr, &certNotAfter, &replOf, &replBy, &replDeadline); err != nil {
		unavailable(err)
		return
	}
	if certFpr == "" {
		certNotAfter = nil
	}
	out["identity"] = map[string]any{
		"wan_mac": strPtr(wan), "lan_mac": strPtr(lan), "hardware_fingerprint": strPtr(hwf),
		"identity_key_fingerprint": strPtr(identityFprFromPubB64(pub)),
		"cert_fingerprint":         strPtr(certFpr), "cert_not_after": certNotAfter,
	}

	var asgVersion, ackedVersion int64
	var asgState string
	var asgIssued time.Time
	switch err := b.DB.QueryRow(ctx, `
        SELECT version, state, issued_at, last_acked_version FROM appliance_signed_assignments WHERE appliance_id=$1`, id).
		Scan(&asgVersion, &asgState, &asgIssued, &ackedVersion); {
	case err == nil:
		out["assignment"] = map[string]any{"version": asgVersion, "state": asgState,
			"signer_key_id": signerKeyID(b, ctx, id), "issued_at": asgIssued, "acked_version": ackedVersion}
	case errors.Is(err, pgx.ErrNoRows):
		out["assignment"] = nil
	default:
		unavailable(err)
		return
	}

	lics, err := b.queryLicenses(ctx, licenseFilter{applianceID: id, includeSuperseded: true})
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "licence history failed")
		return
	}
	out["licenses"] = lics
	events, err := b.applianceEvents(ctx, id, scope)
	if err != nil {
		unavailable(err)
		return
	}
	out["events"] = events

	if row.replacementPending || replOf != nil || replBy != nil {
		out["replacement"] = map[string]any{"pending": row.replacementPending, "deadline": replDeadline,
			"replaces": replOf, "replaced_by": replBy}
	} else {
		out["replacement"] = nil
	}
	if row.terminalDelivery != "" {
		out["retirement"] = map[string]any{"state": row.terminalDelivery, "deadline": row.terminalDeadline}
	} else {
		out["retirement"] = nil
	}
	WriteJSON(w, http.StatusOK, out)
}

// applianceEvents is the appliance's recent audit trail, newest first: {ts, action, actor_email, reason}.
// actor_email is the operator's email, or "system" / "appliance" for actions nobody signed in for.
//
// The two conditions are separate branches of a UNION ALL so each can use its own index (migration 0058); with
// an OR the query walked the whole audit log backwards for a quiet appliance (perf audit D9-L1). A row matching
// both appears once.
//
// AD-4: a customer-scoped caller sees "OneGate support" for any operator who is not one of that customer's own
// users, and no reason for such an entry. Platform scopes see everything.
func (b *Base) applianceEvents(ctx context.Context, id string, scope Scope) ([]map[string]any, error) {
	out := []map[string]any{}
	restrict := restrictTenant(scope)
	rows, err := b.DB.Query(ctx, `
        WITH ev AS (
            (SELECT ts, action, actor_type, actor_id, payload FROM audit_log
              WHERE target_type = 'appliance' AND target_id = $1::text
              ORDER BY ts DESC LIMIT 50)
            UNION ALL
            (SELECT ts, action, actor_type, actor_id, payload FROM audit_log
              WHERE payload->>'appliance_id' = $1::text
                AND NOT (target_type IS NOT DISTINCT FROM 'appliance' AND target_id IS NOT DISTINCT FROM $1::text)
              ORDER BY ts DESC LIMIT 50))
        SELECT ev.ts, ev.action, COALESCE(o.email, ev.actor_type), COALESCE(ev.payload->>'reason',''),
               ($2::text <> '' AND ev.actor_type = 'operator' AND o.tenant_id::text IS DISTINCT FROM $2::text)
          FROM ev
          LEFT JOIN operators o ON ev.actor_type = 'operator' AND o.id::text = ev.actor_id
         ORDER BY ev.ts DESC LIMIT 50`, id, restrict)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var at time.Time
		var action, actor, reason string
		var staff bool
		if err := rows.Scan(&at, &action, &actor, &reason, &staff); err != nil {
			return nil, err
		}
		if staff {
			actor, reason = SupportActorLabel, ""
		}
		out = append(out, map[string]any{"ts": at, "action": action, "actor_email": actor, "reason": strPtr(reason)})
	}
	return out, rows.Err()
}

type LicenseRow struct {
	ID                        string     `json:"id"`
	ApplianceID               *string    `json:"appliance_id"`
	Serial                    *string    `json:"serial"`
	CustomerID                string     `json:"customer_id"`
	CustomerName              string     `json:"customer_name"`
	SiteID                    string     `json:"site_id"`
	SiteName                  string     `json:"site_name"`
	State                     string     `json:"state"`
	Status                    string     `json:"status"`
	ValidFrom                 *time.Time `json:"valid_from"`
	ValidUntil                time.Time  `json:"valid_until"`
	GracePeriodDays           int        `json:"grace_period_days"`
	GraceEndsAt               time.Time  `json:"grace_ends_at"`
	MaxConcurrentOnlineGuests int        `json:"max_concurrent_online_guests"`
	LicenseVersion            int64      `json:"license_version"`
	IssuedAt                  time.Time  `json:"issued_at"`
	// Modules are the module ids the licence authorises; empty = core only.
	Modules []string `json:"modules"`
}

type licenseFilter struct {
	customerID, applianceID, licenseID string
	includeSuperseded                  bool
}

func (b *Base) queryLicenses(ctx context.Context, f licenseFilter) ([]LicenseRow, error) {
	rows, err := b.DB.Query(ctx, `
        SELECT l.id::text, COALESCE(l.appliance_ids[1]::text,''), COALESCE(a.serial,''),
               l.tenant_id::text, COALESCE(t.name,''), l.site_id::text, COALESCE(s.name,''),
               l.status, l.valid_from, l.valid_until, l.grace_period_days, l.offline_grace_days,
               l.max_concurrent_online_guests, l.license_version, l.issued_at, COALESCE(l.modules, '{}')
          FROM licenses l
          LEFT JOIN appliances a ON a.id = l.appliance_ids[1]
          LEFT JOIN tenants    t ON t.id = l.tenant_id
          LEFT JOIN sites      s ON s.id = l.site_id
         WHERE ($1 = '' OR l.tenant_id::text = $1)
           AND ($2 = '' OR l.appliance_ids[1]::text = $2)
           AND ($3 = '' OR l.id::text = $3)
           AND ($4 OR l.status <> 'superseded')
         ORDER BY l.issued_at DESC
         LIMIT 2000`, f.customerID, f.applianceID, f.licenseID, f.includeSuperseded)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	out := []LicenseRow{}
	for rows.Next() {
		var l LicenseRow
		var appID, serial string
		var offline int
		if err := rows.Scan(&l.ID, &appID, &serial, &l.CustomerID, &l.CustomerName, &l.SiteID, &l.SiteName,
			&l.Status, &l.ValidFrom, &l.ValidUntil, &l.GracePeriodDays, &offline,
			&l.MaxConcurrentOnlineGuests, &l.LicenseVersion, &l.IssuedAt, &l.Modules); err != nil {
			return nil, err
		}
		l.ApplianceID, l.Serial = strPtr(appID), strPtr(serial)
		if l.Modules == nil {
			l.Modules = []string{}
		}
		lf := LicenseFacts{Status: l.Status, ValidUntil: l.ValidUntil, GracePeriodDays: l.GracePeriodDays, OfflineGraceDays: offline}
		l.GraceEndsAt = lf.GraceEndsAt()
		if l.Status == "superseded" {
			l.State = "superseded"
		} else {
			l.State = DeriveLicense(&lf, now)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// listLicenses: GET /cloud/v1/licenses?customer_id=&state=&q=
func (b *Base) listLicenses(w http.ResponseWriter, r *http.Request) {
	scope, ok := readScope(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.queryLicenses(ctx, licenseFilter{customerID: scope.Filter(q.Get("customer_id"))})
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	state := q.Get("state")
	text := strings.ToLower(strings.TrimSpace(q.Get("q")))
	out := []LicenseRow{}
	for _, l := range rows {
		if state != "" && l.State != state {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(deref(l.Serial)+" "+l.CustomerName+" "+l.SiteName+" "+l.ID), text) {
			continue
		}
		out = append(out, l)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

type Attention struct {
	Kind         string    `json:"kind"`
	ApplianceID  *string   `json:"appliance_id"`
	Serial       *string   `json:"serial"`
	CustomerID   *string   `json:"customer_id"`
	CustomerName *string   `json:"customer_name"`
	SiteName     *string   `json:"site_name"`
	Detail       string    `json:"detail"`
	Since        time.Time `json:"since"`
}

type openAlert struct {
	id, applianceID, serial, kind, customerID string
	at                                        time.Time
}

func (b *Base) openAlerts(ctx context.Context, customerID string) ([]openAlert, error) {
	rows, err := b.DB.Query(ctx, `
        SELECT x.id::text, COALESCE(x.appliance_id::text,''), COALESCE(x.serial, a.serial, ''), x.kind,
               COALESCE(a.tenant_id::text,''), x.created_at
          FROM appliance_security_alerts x
          LEFT JOIN appliances a ON a.id = x.appliance_id
         WHERE NOT x.resolved AND ($1 = '' OR a.tenant_id::text = $1)
         ORDER BY x.created_at DESC LIMIT 500`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []openAlert
	for rows.Next() {
		var a openAlert
		if rows.Scan(&a.id, &a.applianceID, &a.serial, &a.kind, &a.customerID, &a.at) == nil {
			out = append(out, a)
		}
	}
	return out, rows.Err()
}

// attentionItems lists everything that needs an operator, derived from the same rows the lists show.
func attentionItems(apps []ApplianceRow, alerts []openAlert) []Attention {
	out := []Attention{}
	byID := map[string]*ApplianceRow{}
	for i := range apps {
		a := &apps[i]
		byID[a.ID] = a
		item := func(kind, detail string, since time.Time) {
			id, serial := a.ID, a.Serial
			out = append(out, Attention{Kind: kind, ApplianceID: &id, Serial: &serial, CustomerID: a.CustomerID,
				CustomerName: a.CustomerName, SiteName: a.SiteName, Detail: detail, Since: since})
		}
		switch a.Activation {
		case ActivationWaiting:
			item("waiting_activation", "registered and waiting for activation", a.RegisteredAt)
			continue
		case ActivationRetired:
			continue
		case ActivationRetiring:
			if a.terminalDelivery == "terminal_delivery_failed" {
				since := a.RegisteredAt
				if a.terminalDeadline != nil {
					since = *a.terminalDeadline
				}
				item("retirement_unconfirmed", "the appliance did not confirm its retirement; its credentials are still valid", since)
			}
		}
		lsince := a.RegisteredAt
		if a.License.ValidUntil != nil {
			lsince = *a.License.ValidUntil
		}
		switch a.License.State {
		case LicenseExpiring:
			item("license_expiring", "licence expires "+lsince.UTC().Format("2006-01-02"), lsince)
		case LicenseGrace:
			item("license_grace", "licence expired; in grace until "+a.License.GraceEndsAt.UTC().Format("2006-01-02"), lsince)
		case LicenseExpired:
			item("license_expired", "licence and grace period have ended", *a.License.GraceEndsAt)
		case LicenseSuspended:
			item("license_suspended", "licence is suspended", a.RegisteredAt)
		}
		if a.Connection == ConnectionOffline && a.LastSeenAt != nil {
			item("appliance_offline", "not seen for more than 24 hours", *a.LastSeenAt)
		}
	}
	for _, al := range alerts {
		it := Attention{Kind: "security_alert", Detail: al.kind, Since: al.at, Serial: strPtr(al.serial)}
		if a := byID[al.applianceID]; a != nil {
			it.ApplianceID, it.CustomerID, it.CustomerName, it.SiteName = strPtr(a.ID), a.CustomerID, a.CustomerName, a.SiteName
		} else {
			it.ApplianceID = strPtr(al.applianceID)
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Since.After(out[j].Since) })
	return out
}

// overview: GET /cloud/v1/overview
func (b *Base) overview(w http.ResponseWriter, r *http.Request) {
	scope, ok := readScope(w, r)
	if !ok {
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	filter := scope.Filter("")
	apps, err := b.queryAppliances(ctx, filter, "")
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	alerts, err := b.openAlerts(ctx, filter)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	var customers, sites int
	_ = b.DB.QueryRow(ctx, `SELECT count(*) FROM tenants WHERE status <> 'archived' AND ($1 = '' OR id::text = $1)`, filter).Scan(&customers)
	_ = b.DB.QueryRow(ctx, `SELECT count(*) FROM sites WHERE status <> 'archived' AND ($1 = '' OR tenant_id::text = $1)`, filter).Scan(&sites)

	appl := map[string]int{"total": len(apps)}
	for _, k := range []string{ActivationWaiting, ActivationActivating, ActivationActivated, ActivationRetiring,
		ActivationRetired, ConnectionConnected, ConnectionRecentlySeen, ConnectionOffline, ConnectionNever} {
		appl[k] = 0
	}
	lics := map[string]int{}
	for _, k := range []string{LicenseActive, LicenseExpiring, LicenseGrace, LicenseExpired, LicenseSuspended,
		LicenseRevoked, LicenseNone} {
		lics[k] = 0
	}
	for _, a := range apps {
		appl[a.Activation]++
		appl[a.Connection]++
		lics[a.License.State]++
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"customers": customers, "sites": sites, "appliances": appl, "licenses": lics,
		"attention": attentionItems(apps, alerts),
	})
}
