package api

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

type WalledGardenRule struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	SiteID      *string   `json:"site_id,omitempty"` // NULL = applies tenant-wide
	Kind        string    `json:"kind"`              // domain | cidr | ip
	Value       string    `json:"value"`
	Ports       []int     `json:"ports,omitempty"` // nil = all ports
	Description *string   `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type wgWriteReq struct {
	SiteID      *string `json:"site_id,omitempty"`
	Kind        string  `json:"kind,omitempty"`
	Value       string  `json:"value,omitempty"`
	Ports       []int   `json:"ports,omitempty"`
	Description *string `json:"description,omitempty"`
}

func (r *wgWriteReq) validate(isCreate bool) string {
	if isCreate {
		if r.Kind == "" {
			return "kind required"
		}
		if r.Value == "" {
			return "value required"
		}
	}
	if r.Kind != "" {
		switch r.Kind {
		case "domain", "cidr", "ip":
		default:
			return "kind must be domain|cidr|ip"
		}
	}
	if r.Value != "" && r.Kind != "" {
		if msg := validateWGValue(r.Kind, r.Value); msg != "" {
			return msg
		}
	}
	for _, p := range r.Ports {
		if p < 1 || p > 65535 {
			return "ports must be in 1..65535"
		}
	}
	return ""
}

func validateWGValue(kind, val string) string {
	switch kind {
	case "ip":
		if net.ParseIP(val) == nil {
			return "value is not a valid IP"
		}
	case "cidr":
		if _, _, err := net.ParseCIDR(val); err != nil {
			return "value is not a valid CIDR"
		}
	case "domain":
		v := strings.TrimSpace(val)
		if strings.ContainsAny(v, " \t") || !strings.Contains(v, ".") || len(v) < 3 {
			return "value is not a valid domain"
		}
	}
	return ""
}

func (b *Base) WalledGardenRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireTenant)
	r.Get("/", b.listWG)
	r.Get("/effective", b.effectiveWG) // phase 5.7.A
	r.Post("/", b.createWG)
	r.Get("/{id}", b.getWG)
	r.Patch("/{id}", b.patchWG)
	r.Delete("/{id}", b.deleteWG)
	return r
}

// effectiveWG returns the rules an appliance at ?site_id=X actually
// enforces: union of tenant-wide (site_id IS NULL) and site-scoped rows.
// Walled-garden is additive — rules accumulate, none "override" — so this
// is a straight UNION rather than the prefer-site-scoped resolution PMS
// uses.
//
// Without ?site_id, returns tenant-wide rules only.
func (b *Base) effectiveWG(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	siteID := strings.TrimSpace(r.URL.Query().Get("site_id"))
	ctx, cancel := DBCtx(r)
	defer cancel()

	var siteArg any
	if siteID != "" {
		// Validate the site belongs to this tenant before exposing data.
		var n int
		if err := b.DB.QueryRow(ctx,
			`SELECT count(*) FROM sites WHERE id = $1 AND tenant_id = $2`,
			siteID, tenantID).Scan(&n); err != nil || n == 0 {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "site not in tenant")
			return
		}
		siteArg = siteID
	}

	rows, err := b.DB.Query(ctx, `
        SELECT `+wgCols+`
          FROM walled_garden_rules
         WHERE tenant_id = $1
           AND (site_id IS NULL OR site_id = $2)
         ORDER BY (site_id IS NULL) DESC, created_at DESC, id DESC
    `, tenantID, siteArg)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	var out []WalledGardenRule
	for rows.Next() {
		var wg WalledGardenRule
		if err := scanWG(rows, &wg); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, wg)
	}
	WriteList(w, out, ListMeta{})
}

const wgCols = `id, tenant_id, site_id::text, kind, value, ports, description, created_at`

func scanWG(row interface{ Scan(...any) error }, wg *WalledGardenRule) error {
	var siteID *string
	var ports []int32 // pgx decodes int[] into []int32
	err := row.Scan(&wg.ID, &wg.TenantID, &siteID, &wg.Kind, &wg.Value, &ports, &wg.Description, &wg.CreatedAt)
	if err != nil {
		return err
	}
	wg.SiteID = siteID
	if ports != nil {
		wg.Ports = make([]int, len(ports))
		for i, p := range ports {
			wg.Ports[i] = int(p)
		}
	}
	return nil
}

func (b *Base) listWG(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	ctx, cancel := DBCtx(r)
	defer cancel()

	q := r.URL.Query()
	limit := ParseLimit(r, 50, 200)
	curT, curI, err := DecodeCursor(q.Get("cursor"))
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	var tArg, iArg any
	if !curT.IsZero() {
		tArg = curT
	}
	if curI != "" {
		iArg = curI
	}
	var siteArg any
	if v := q.Get("site_id"); v != "" {
		siteArg = v
	}

	rows, err := b.DB.Query(ctx, `
        SELECT `+wgCols+`
          FROM walled_garden_rules
         WHERE tenant_id = $1
           AND ($2::uuid IS NULL OR site_id = $2)
           AND ($3::timestamptz IS NULL OR (created_at, id) < ($3, $4::uuid))
         ORDER BY created_at DESC, id DESC
         LIMIT $5
    `, tenantID, siteArg, tArg, iArg, limit+1)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()

	var out []WalledGardenRule
	for rows.Next() {
		var wg WalledGardenRule
		if err := scanWG(rows, &wg); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, wg)
	}
	meta := ListMeta{}
	if len(out) > limit {
		last := out[limit-1]
		meta.HasMore = true
		meta.Cursor = EncodeCursor(last.CreatedAt, last.ID)
		out = out[:limit]
	}
	WriteList(w, out, meta)
}

func (b *Base) createWG(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	var req wgWriteReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if msg := req.validate(true); msg != "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, msg)
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	// If a site is specified, verify it belongs to this tenant.
	if req.SiteID != nil && *req.SiteID != "" {
		var n int
		if err := b.DB.QueryRow(ctx,
			`SELECT count(*) FROM sites WHERE id = $1 AND tenant_id = $2`,
			*req.SiteID, tenantID).Scan(&n); err != nil || n == 0 {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "site not found in this tenant")
			return
		}
	}

	var siteArg any
	if req.SiteID != nil && *req.SiteID != "" {
		siteArg = *req.SiteID
	}
	var wg WalledGardenRule
	err := scanWG(b.DB.QueryRow(ctx, `
        INSERT INTO walled_garden_rules (tenant_id, site_id, kind, value, ports, description)
        VALUES ($1, $2, $3, $4, $5::int[], $6)
        RETURNING `+wgCols,
		tenantID, siteArg, req.Kind, req.Value, int32Slice(req.Ports), req.Description,
	), &wg)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "insert failed: "+err.Error())
		return
	}
	audit.Op(r.Context(), b.DB, r, "walled_garden.created", "walled_garden_rule", wg.ID, map[string]any{
		"_tenant_id": tenantID, "kind": wg.Kind, "value": wg.Value,
	})
	WriteJSON(w, http.StatusCreated, wg)
}

func (b *Base) getWG(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	var wg WalledGardenRule
	err := scanWG(b.DB.QueryRow(ctx,
		`SELECT `+wgCols+` FROM walled_garden_rules WHERE id = $1 AND tenant_id = $2`,
		id, tenantID), &wg)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "rule not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	WriteJSON(w, http.StatusOK, wg)
}

func (b *Base) patchWG(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	var req wgWriteReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if msg := req.validate(false); msg != "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, msg)
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	// To update value safely we may also need the current kind — read-modify
	// cross-check if either is provided without the other.
	if (req.Kind == "") != (req.Value == "") {
		// Allow: kind+value together, OR neither. Otherwise we can't validate.
		if req.Kind == "" || req.Value == "" {
			// Fetch the missing side and re-validate.
			var curKind, curValue string
			if err := b.DB.QueryRow(ctx,
				`SELECT kind, value FROM walled_garden_rules WHERE id=$1 AND tenant_id=$2`,
				id, tenantID).Scan(&curKind, &curValue); err != nil {
				if IsNoRows(err) {
					Fail(w, r, http.StatusNotFound, CodeNotFound, "rule not found")
					return
				}
				Fail(w, r, http.StatusInternalServerError, CodeInternal, "lookup failed")
				return
			}
			kind := req.Kind
			if kind == "" {
				kind = curKind
			}
			val := req.Value
			if val == "" {
				val = curValue
			}
			if msg := validateWGValue(kind, val); msg != "" {
				Fail(w, r, http.StatusBadRequest, CodeBadRequest, msg)
				return
			}
		}
	}

	var siteArg any
	if req.SiteID != nil {
		// empty string means "clear" (tenant-wide); non-empty sets a specific site.
		if *req.SiteID == "" {
			siteArg = nil // handled with sentinel below
		} else {
			siteArg = *req.SiteID
		}
	}
	// Use a second parameter to mark presence of site_id in the patch.
	siteProvided := req.SiteID != nil

	var wg WalledGardenRule
	err := scanWG(b.DB.QueryRow(ctx, `
        UPDATE walled_garden_rules SET
            kind        = COALESCE(NULLIF($3,''), kind),
            value       = COALESCE(NULLIF($4,''), value),
            ports       = COALESCE($5::int[], ports),
            description = COALESCE($6, description),
            site_id     = CASE WHEN $7::boolean THEN $8::uuid ELSE site_id END
         WHERE id = $1 AND tenant_id = $2
         RETURNING `+wgCols,
		id, tenantID, req.Kind, req.Value, int32Slice(req.Ports), req.Description,
		siteProvided, siteArg,
	), &wg)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "rule not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed: "+err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, wg)
}

func (b *Base) deleteWG(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	ct, err := b.DB.Exec(ctx,
		`DELETE FROM walled_garden_rules WHERE id = $1 AND tenant_id = $2`,
		id, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed")
		return
	}
	if ct.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "rule not found")
		return
	}
	audit.Op(r.Context(), b.DB, r, "walled_garden.deleted", "walled_garden_rule", id, map[string]any{"_tenant_id": tenantID})
	w.WriteHeader(http.StatusNoContent)
}

func int32Slice(in []int) any {
	if in == nil {
		return nil
	}
	out := make([]int32, len(in))
	for i, v := range in {
		out[i] = int32(v)
	}
	return out
}
