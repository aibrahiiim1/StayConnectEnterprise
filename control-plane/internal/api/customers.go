package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// CUSTOMERS AND SITES. A customer is the tenants table (the API calls it a customer); customer_id is the old
// tenant_id.

// CustomerRow is the customer list/detail shape (§6).
type CustomerRow struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	Sites          int       `json:"sites"`
	Appliances     int       `json:"appliances"`
	Activated      int       `json:"activated"`
	LicensesActive int       `json:"licenses_active"`
	Attention      int       `json:"attention"`
}

// SiteRow is the site list shape (§6).
type SiteRow struct {
	ID         string  `json:"id"`
	CustomerID string  `json:"customer_id"`
	Code       string  `json:"code"`
	Name       string  `json:"name"`
	Timezone   string  `json:"timezone"`
	Country    *string `json:"country"`
	SiteType   string  `json:"site_type"` // descriptive only (site_types.go); UNSPECIFIED when not set
	Status     string  `json:"status"`
	Appliances int     `json:"appliances"`
}

var slugStrip = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns a display name into a url-safe identifier ("Northwind Sites" -> "northwind-sites").
func slugify(name string) string {
	s := strings.Trim(slugStrip.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-"), "-")
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	if s == "" {
		s = "customer"
	}
	return s
}

// uniqueValue returns base, or base-2, base-3 … the first that exists() rejects.
func uniqueValue(ctx context.Context, base string, exists func(context.Context, string) (bool, error)) (string, error) {
	for i := 1; i < 1000; i++ {
		v := base
		if i > 1 {
			v = base + "-" + strconv.Itoa(i)
		}
		taken, err := exists(ctx, v)
		if err != nil {
			return "", err
		}
		if !taken {
			return v, nil
		}
	}
	return "", errors.New("could not find a free identifier")
}

// querier is satisfied by *pgxpool.Pool and pgx.Tx.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// createCustomer inserts a customer; the slug is derived from the name when not given.
func createCustomer(ctx context.Context, q querier, name, slug string) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", errors.New("customer name is required")
	}
	slug = strings.TrimSpace(strings.ToLower(slug))
	if slug == "" {
		var err error
		slug, err = uniqueValue(ctx, slugify(name), func(ctx context.Context, v string) (bool, error) {
			var n int
			err := q.QueryRow(ctx, `SELECT count(*) FROM tenants WHERE slug=$1`, v).Scan(&n)
			return n > 0, err
		})
		if err != nil {
			return "", "", err
		}
	} else if slugify(slug) != slug {
		return "", "", errors.New("slug may contain only lowercase letters, digits and hyphens")
	}
	var id string
	err := q.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ($1, $2) RETURNING id::text`, slug, name).Scan(&id)
	if err != nil {
		return "", "", errors.New("a customer with that slug already exists")
	}
	return id, slug, nil
}

// createSite inserts a site under customerID; the code is derived from the name when not given. siteType ""
// is UNSPECIFIED; any other value must be one of SiteTypes.
func createSite(ctx context.Context, q querier, customerID, name, code, timezone, country, siteType string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("site name is required")
	}
	siteType, ok := normalizeSiteType(siteType)
	if !ok {
		return "", errors.New(msgUnknownSiteType)
	}
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		return "", errors.New("site timezone is required")
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return "", errors.New("unknown timezone " + timezone)
	}
	code = strings.TrimSpace(strings.ToLower(code))
	if code == "" {
		var err error
		code, err = uniqueValue(ctx, slugify(name), func(ctx context.Context, v string) (bool, error) {
			var n int
			err := q.QueryRow(ctx, `SELECT count(*) FROM sites WHERE tenant_id=$1 AND code=$2`, customerID, v).Scan(&n)
			return n > 0, err
		})
		if err != nil {
			return "", err
		}
	}
	var status string
	if err := q.QueryRow(ctx, `SELECT status FROM tenants WHERE id=$1`, customerID).Scan(&status); err != nil {
		return "", errors.New("customer not found")
	}
	if status == "archived" {
		return "", errors.New("customer is archived; restore it first")
	}
	var id string
	err := q.QueryRow(ctx, `
        INSERT INTO sites (tenant_id, code, name, timezone, country, site_type)
        VALUES ($1, $2, $3, $4, NULLIF($5,''), $6) RETURNING id::text`,
		customerID, code, name, timezone, strings.TrimSpace(country), siteType).Scan(&id)
	if err != nil {
		return "", errors.New("a site with that code already exists for this customer")
	}
	return id, nil
}

// customerRows builds customer rows with their counts. customerID "" = all (fleet scope).
func (b *Base) customerRows(ctx context.Context, customerID, status, text string) ([]CustomerRow, error) {
	rows, err := b.DB.Query(ctx, `
        SELECT t.id::text, t.name, t.slug, t.status, t.created_at,
               (SELECT count(*) FROM sites s WHERE s.tenant_id = t.id AND s.status <> 'archived')
          FROM tenants t
         WHERE ($1 = '' OR t.id::text = $1)
           AND ($2 = 'all' OR ($2 = 'archived' AND t.status = 'archived') OR ($2 = 'active' AND t.status <> 'archived'))
           AND ($3 = '' OR t.name ILIKE '%' || $3 || '%' OR t.slug ILIKE '%' || $3 || '%')
         ORDER BY lower(t.name)`, customerID, status, strings.TrimSpace(text))
	if err != nil {
		return nil, err
	}
	var out []CustomerRow
	idx := map[string]int{}
	for rows.Next() {
		var c CustomerRow
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug, &c.Status, &c.CreatedAt, &c.Sites); err != nil {
			rows.Close()
			return nil, err
		}
		idx[c.ID] = len(out)
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	apps, err := b.queryAppliances(ctx, customerID, "")
	if err != nil {
		return nil, err
	}
	alerts, err := b.openAlerts(ctx, customerID)
	if err != nil {
		return nil, err
	}
	for _, a := range apps {
		i, ok := idx[deref(a.CustomerID)]
		if !ok || a.Activation == ActivationRetired {
			continue
		}
		out[i].Appliances++
		if a.Activation == ActivationActivated {
			out[i].Activated++
		}
		if a.License.State == LicenseActive || a.License.State == LicenseExpiring {
			out[i].LicensesActive++
		}
	}
	for _, it := range attentionItems(apps, alerts) {
		if i, ok := idx[deref(it.CustomerID)]; ok {
			out[i].Attention++
		}
	}
	if out == nil {
		out = []CustomerRow{}
	}
	return out, nil
}

// listCustomers: GET /cloud/v1/customers?status=active|archived|all&q=
func (b *Base) listCustomers(w http.ResponseWriter, r *http.Request) {
	scope, ok := readScope(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "":
		status = "active"
	case "active", "archived", "all":
	default:
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "status must be active, archived or all")
		return
	}
	if !scope.All {
		status = "all" // a customer's own record is always visible to it
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.customerRows(ctx, scope.Filter(""), status, r.URL.Query().Get("q"))
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (b *Base) writeCustomer(w http.ResponseWriter, r *http.Request, code int, id string) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.customerRows(ctx, id, "all", "")
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	if len(rows) == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "customer not found")
		return
	}
	WriteJSON(w, code, rows[0])
}

// getCustomer: GET /cloud/v1/customers/{id}
func (b *Base) getCustomer(w http.ResponseWriter, r *http.Request) {
	scope, ok := readScope(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !scope.Allows(id) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "customer not found")
		return
	}
	b.writeCustomer(w, r, http.StatusOK, id)
}

// postCustomer: POST /cloud/v1/customers {name, slug?}
func (b *Base) postCustomer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	id, slug, err := createCustomer(ctx, b.DB, in.Name, in.Slug)
	if err != nil {
		Fail(w, r, http.StatusConflict, CodeConflict, err.Error())
		return
	}
	audit.Op(r.Context(), b.DB, r, "customer.created", "customer", id, map[string]any{
		"_tenant_id": id, "name": strings.TrimSpace(in.Name), "slug": slug})
	b.writeCustomer(w, r, http.StatusCreated, id)
}

// patchCustomer: PATCH /cloud/v1/customers/{id} {name}
func (b *Base) patchCustomer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Name string `json:"name"`
	}
	if err := DecodeJSON(r, &in); err != nil || strings.TrimSpace(in.Name) == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "name is required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var old string
	if err := b.DB.QueryRow(ctx, `SELECT name FROM tenants WHERE id=$1`, id).Scan(&old); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "customer not found")
		return
	}
	if _, err := b.DB.Exec(ctx, `UPDATE tenants SET name=$2, updated_at=now() WHERE id=$1`, id, strings.TrimSpace(in.Name)); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "customer.renamed", "customer", id, map[string]any{
		"_tenant_id": id, "from": old, "to": strings.TrimSpace(in.Name)})
	b.writeCustomer(w, r, http.StatusOK, id)
}

// setCustomerStatus: POST /cloud/v1/customers/{id}/archive|restore
func (b *Base) setCustomerStatus(to string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		ctx, cancel := DBCtx(r)
		defer cancel()
		tag, err := b.DB.Exec(ctx, `UPDATE tenants SET status=$2, updated_at=now() WHERE id=$1`, id, to)
		if err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
			return
		}
		if tag.RowsAffected() == 0 {
			Fail(w, r, http.StatusNotFound, CodeNotFound, "customer not found")
			return
		}
		action := "customer.archived"
		if to == "active" {
			action = "customer.restored"
		}
		audit.Op(r.Context(), b.DB, r, action, "customer", id, map[string]any{"_tenant_id": id})
		b.writeCustomer(w, r, http.StatusOK, id)
	}
}

type deleteConfirm struct {
	Confirm string `json:"confirm"`
	Reason  string `json:"reason"`
}

// blocker is one class of dependant that refuses a delete: 409 {error, message, blocking:[…]}.
type blocker struct {
	Type  string `json:"type"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

func blockers(all ...blocker) []blocker {
	out := []blocker{}
	for _, b := range all {
		if b.Count > 0 {
			out = append(out, b)
		}
	}
	return out
}

// deleteCustomer: DELETE /cloud/v1/customers/{id} {confirm, reason} — refused while it has sites/appliances.
func (b *Base) deleteCustomer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in deleteConfirm
	_ = DecodeJSON(r, &in)
	ctx, cancel := DBCtx(r)
	defer cancel()
	var name, slug string
	if err := b.DB.QueryRow(ctx, `SELECT name, slug FROM tenants WHERE id=$1`, id).Scan(&name, &slug); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "customer not found")
		return
	}
	if in.Confirm != name && in.Confirm != slug {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "type the exact customer name to confirm", map[string]any{"expected": name})
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "reason is required")
		return
	}
	var sites, apps int
	if err := b.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM sites WHERE tenant_id=$1),
                                         (SELECT count(*) FROM appliances WHERE tenant_id=$1)`, id).Scan(&sites, &apps); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "Central could not check what the customer still has, so it was not deleted.")
		return
	}
	if blocking := blockers(blocker{"site", "Sites", sites}, blocker{"appliance", "Appliances", apps}); len(blocking) > 0 {
		Fail(w, r, http.StatusConflict, CodeConflict,
			"the customer still has sites or appliances; delete or move them first",
			map[string]any{"blocking": blocking})
		return
	}
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed")
		return
	}
	defer tx.Rollback(ctx)
	// Licence rows reference the customer with RESTRICT. With no site left, none can be current (a site
	// cannot be deleted while it holds a current licence), so these are history of appliances already gone.
	for _, stmt := range []string{
		`DELETE FROM licenses WHERE tenant_id=$1 AND status NOT IN ('active','suspended')`,
		`DELETE FROM tenants WHERE id=$1`,
	} {
		if _, err := tx.Exec(ctx, stmt, id); err != nil {
			Fail(w, r, http.StatusConflict, CodeConflict, "delete failed: "+err.Error())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed")
		return
	}
	// audit_log has no foreign key, so the record outlives the customer. Written only once it is gone.
	audit.Op(r.Context(), b.DB, r, "customer.deleted", "customer", id, map[string]any{
		"_tenant_id": id, "name": name, "slug": slug, "reason": in.Reason})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------------------------------------
// Sites
// ---------------------------------------------------------------------------------------------------------

func (b *Base) siteRows(ctx context.Context, customerID, siteID string) ([]SiteRow, error) {
	rows, err := b.DB.Query(ctx, `
        SELECT s.id::text, s.tenant_id::text, s.code, s.name, s.timezone, s.country, s.site_type, s.status,
               (SELECT count(*) FROM appliances a WHERE a.site_id = s.id
                   AND a.lifecycle_state NOT IN ('revoked','decommissioned'))
          FROM sites s
         WHERE ($1 = '' OR s.tenant_id::text = $1) AND ($2 = '' OR s.id::text = $2)
         ORDER BY (s.status = 'archived'), lower(s.name)`, customerID, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SiteRow{}
	for rows.Next() {
		var s SiteRow
		if err := rows.Scan(&s.ID, &s.CustomerID, &s.Code, &s.Name, &s.Timezone, &s.Country, &s.SiteType, &s.Status, &s.Appliances); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// listSites: GET /cloud/v1/customers/{id}/sites
func (b *Base) listSites(w http.ResponseWriter, r *http.Request) {
	scope, ok := readScope(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !scope.Allows(id) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "customer not found")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.siteRows(ctx, id, "")
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (b *Base) writeSite(w http.ResponseWriter, r *http.Request, code int, id string) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.siteRows(ctx, "", id)
	if err != nil || len(rows) == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "site not found")
		return
	}
	WriteJSON(w, code, rows[0])
}

// postSite: POST /cloud/v1/customers/{id}/sites {name, code?, timezone, country?, site_type?}
func (b *Base) postSite(w http.ResponseWriter, r *http.Request) {
	customerID := chi.URLParam(r, "id")
	if !requireCustomerManage(w, r, customerID, auth.PermSitesManage, auth.PermCustomerSitesManage) {
		return
	}
	var in struct {
		Name     string `json:"name"`
		Code     string `json:"code"`
		Timezone string `json:"timezone"`
		Country  string `json:"country"`
		SiteType string `json:"site_type"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	id, err := createSite(ctx, b.DB, customerID, in.Name, in.Code, in.Timezone, in.Country, in.SiteType)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	siteType, _ := normalizeSiteType(in.SiteType)
	audit.Op(r.Context(), b.DB, r, "site.created", "site", id, map[string]any{
		"_tenant_id": customerID, "name": in.Name, "site_type": siteType})
	b.writeSite(w, r, http.StatusCreated, id)
}

// siteCustomer returns the customer a site belongs to, or writes 404.
func (b *Base) siteCustomer(w http.ResponseWriter, r *http.Request, siteID string) (string, bool) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	var customerID string
	if err := b.DB.QueryRow(ctx, `SELECT tenant_id::text FROM sites WHERE id=$1`, siteID).Scan(&customerID); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "site not found")
		return "", false
	}
	return customerID, true
}

// patchSite: PATCH /cloud/v1/sites/{id} {name?, timezone?, country?, site_type?}
//
// The site's name and type are signed into the assignment of every appliance placed there. When either
// changes, each such appliance holding a granting assignment gets a newly signed one (next version) in the
// same transaction, so the change reaches it and the edit and the documents commit or roll back together.
func (b *Base) patchSite(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	customerID, ok := b.siteCustomer(w, r, id)
	if !ok || !requireCustomerManage(w, r, customerID, auth.PermSitesManage, auth.PermCustomerSitesManage) {
		return
	}
	var in struct {
		Name     *string `json:"name"`
		Timezone *string `json:"timezone"`
		Country  *string `json:"country"`
		SiteType *string `json:"site_type"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "name cannot be empty")
		return
	}
	if in.Timezone != nil {
		if _, err := time.LoadLocation(strings.TrimSpace(*in.Timezone)); err != nil || strings.TrimSpace(*in.Timezone) == "" {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "unknown timezone")
			return
		}
	}
	newType := ""
	if in.SiteType != nil {
		t, ok := normalizeSiteType(*in.SiteType)
		if !ok {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, msgUnknownSiteType)
			return
		}
		newType = t
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	defer tx.Rollback(ctx)
	var oldName, oldType string
	if err := tx.QueryRow(ctx, `SELECT name, site_type FROM sites WHERE id=$1 FOR UPDATE`, id).Scan(&oldName, &oldType); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "site not found")
		return
	}
	var newName string
	if err := tx.QueryRow(ctx, `
        UPDATE sites SET name = COALESCE(NULLIF(btrim($2),''), name),
                         timezone = COALESCE(NULLIF(btrim($3),''), timezone),
                         country = CASE WHEN $5 THEN NULLIF(btrim($4),'') ELSE country END,
                         site_type = COALESCE(NULLIF($6,''), site_type),
                         updated_at = now()
         WHERE id = $1 RETURNING name, site_type`, id, deref(in.Name), deref(in.Timezone), deref(in.Country), in.Country != nil,
		newType).Scan(&newName, &newType); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	var reissued []map[string]any
	if newName != oldName || newType != oldType {
		if reissued, err = b.reissueSiteAssignmentsTx(ctx, tx, id); err != nil {
			Fail(w, r, http.StatusServiceUnavailable, "assignment_unsignable",
				"The site was not changed: the appliances placed there could not receive a newly signed assignment: "+err.Error())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "site.updated", "site", id, map[string]any{"_tenant_id": customerID,
		"name": deref(in.Name), "timezone": deref(in.Timezone), "country": deref(in.Country),
		"site_type_from": oldType, "site_type": newType, "assignments_reissued": reissued})
	b.writeSite(w, r, http.StatusOK, id)
}

// reissueSiteAssignmentsTx signs a new assignment, in the SAME state and with the next version, for every
// appliance placed at the site whose current assignment grants ownership. Appliances with a terminal
// (clearing) assignment -- retiring or retired -- are never touched: re-granting them would undo a retirement.
// The appliance rows are locked first so a concurrent move or retire cannot interleave.
func (b *Base) reissueSiteAssignmentsTx(ctx context.Context, tx pgx.Tx, siteID string) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, `
        SELECT a.id::text, sa.state
          FROM appliances a
          JOIN appliance_signed_assignments sa ON sa.appliance_id = a.id
         WHERE a.site_id = $1 AND sa.state IN ('assigned','reassigned')
         ORDER BY a.id
           FOR UPDATE OF a`, siteID)
	if err != nil {
		return nil, err
	}
	type target struct{ id, state string }
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.state); err != nil {
			rows.Close()
			return nil, err
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	if len(targets) == 0 {
		return out, nil
	}
	if b.AssignKey == nil {
		return nil, errors.New("assignment signing key not configured")
	}
	ab := &AssignmentBase{Base: b, SignKey: b.AssignKey}
	for _, t := range targets {
		doc, err := ab.IssueTx(ctx, tx, t.id, t.state)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"appliance_id": t.id, "assignment_version": doc.Version})
	}
	return out, nil
}

// setSiteStatus: POST /cloud/v1/sites/{id}/archive|restore
func (b *Base) setSiteStatus(to string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		customerID, ok := b.siteCustomer(w, r, id)
		if !ok || !requireCustomerManage(w, r, customerID, auth.PermSitesManage, auth.PermCustomerSitesManage) {
			return
		}
		ctx, cancel := DBCtx(r)
		defer cancel()
		if _, err := b.DB.Exec(ctx, `UPDATE sites SET status=$2, updated_at=now() WHERE id=$1`, id, to); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
			return
		}
		action := "site.archived"
		if to == "active" {
			action = "site.restored"
		}
		audit.Op(r.Context(), b.DB, r, action, "site", id, map[string]any{"_tenant_id": customerID})
		b.writeSite(w, r, http.StatusOK, id)
	}
}

// deleteSite: DELETE /cloud/v1/sites/{id} {confirm, reason} — only an empty site.
func (b *Base) deleteSite(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	customerID, ok := b.siteCustomer(w, r, id)
	if !ok || !requireCustomerManage(w, r, customerID, auth.PermSitesManage, auth.PermCustomerSitesManage) {
		return
	}
	var in deleteConfirm
	_ = DecodeJSON(r, &in)
	ctx, cancel := DBCtx(r)
	defer cancel()
	var code, name string
	if err := b.DB.QueryRow(ctx, `SELECT code, name FROM sites WHERE id=$1`, id).Scan(&code, &name); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "site not found")
		return
	}
	if in.Confirm != code && in.Confirm != name {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "type the exact site name to confirm", map[string]any{"expected": name})
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "reason is required")
		return
	}
	var apps, current int
	if err := b.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM appliances WHERE site_id=$1),
                                         (SELECT count(*) FROM licenses WHERE site_id=$1 AND status IN ('active','suspended'))`,
		id).Scan(&apps, &current); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "Central could not check what the site still has, so it was not deleted.")
		return
	}
	if blocking := blockers(blocker{"appliance", "Appliances", apps}, blocker{"license", "Current licences", current}); len(blocking) > 0 {
		Fail(w, r, http.StatusConflict, CodeConflict,
			"the site still has appliances or a current licence; move or delete them first",
			map[string]any{"blocking": blocking})
		return
	}
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed")
		return
	}
	defer tx.Rollback(ctx)
	// Licence history of appliances that no longer exist at this site; the audit log keeps the record.
	for _, stmt := range []string{
		`DELETE FROM licenses WHERE site_id=$1 AND status IN ('revoked','superseded')`,
		`DELETE FROM sites WHERE id=$1`,
	} {
		if _, err := tx.Exec(ctx, stmt, id); err != nil {
			Fail(w, r, http.StatusConflict, CodeConflict, "delete failed: "+err.Error())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "site.deleted", "site", id, map[string]any{
		"_tenant_id": customerID, "code": code, "name": name, "reason": in.Reason})
	w.WriteHeader(http.StatusNoContent)
}
