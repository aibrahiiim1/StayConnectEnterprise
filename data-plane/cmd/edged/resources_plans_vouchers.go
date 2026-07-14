package main

// Guest access plans (ticket_templates), voucher batches and vouchers.
// Ported from control-plane ticket_templates.go / vouchers.go with the fixed
// site scope, the license-provisioning gate and edged response conventions.

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/codegen"
	"github.com/stayconnect/enterprise/data-plane/internal/crockford"
)

// generateUniqueCodes produces n codes per opts, then guarantees they do not
// already exist anywhere in the vouchers table (the global unique index),
// regenerating any collisions. Bounded retries; returns a clear error if the
// configured space is too small.
func (s *server) generateUniqueCodes(ctx context.Context, n int, opts codegen.Options) ([]string, error) {
	codes, err := codegen.GenerateN(n, opts)
	if err != nil {
		return nil, err
	}
	for round := 0; round < 12; round++ {
		var clash []string
		rows, err := s.db.Query(ctx, `SELECT code FROM vouchers WHERE code = ANY($1)`, codes)
		if err != nil {
			return nil, fmt.Errorf("uniqueness check failed")
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err == nil {
				clash = append(clash, c)
			}
		}
		rows.Close()
		if len(clash) == 0 {
			return codes, nil
		}
		// Replace only the clashing codes with fresh ones (kept unique in-set).
		existing := map[string]struct{}{}
		for _, c := range codes {
			existing[c] = struct{}{}
		}
		clashSet := map[string]struct{}{}
		for _, c := range clash {
			clashSet[c] = struct{}{}
		}
		repl, err := codegen.GenerateN(len(clash)*2+8, opts)
		if err != nil {
			return nil, err
		}
		ri := 0
		for i, c := range codes {
			if _, bad := clashSet[c]; !bad {
				continue
			}
			for ; ri < len(repl); ri++ {
				if _, dup := existing[repl[ri]]; !dup {
					codes[i] = repl[ri]
					existing[repl[ri]] = struct{}{}
					ri++
					break
				}
			}
		}
	}
	return nil, fmt.Errorf("could not generate unique codes — increase length or character set")
}

// ----- guest access plans ----------------------------------------------------

type guestAccessPlan struct {
	ID                   string    `json:"id"`
	Code                 string    `json:"code"`
	Name                 string    `json:"name"`
	Description          *string   `json:"description,omitempty"`
	DurationSeconds      *int      `json:"duration_seconds,omitempty"`
	DataCapBytes         *int64    `json:"data_cap_bytes,omitempty"`
	DownKbps             *int      `json:"down_kbps,omitempty"`
	UpKbps               *int      `json:"up_kbps,omitempty"`
	MaxConcurrentDevices int       `json:"max_concurrent_devices"`
	ValiditySeconds      *int      `json:"validity_seconds,omitempty"`
	PriceCents           *int      `json:"price_cents,omitempty"`
	Currency             *string   `json:"currency,omitempty"`
	IsActive             bool      `json:"is_active"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

const planCols = `id, code, name, description, duration_seconds, data_cap_bytes,
       down_kbps, up_kbps, max_concurrent_devices, validity_seconds,
       price_cents, currency, is_active, created_at, updated_at`

func scanPlan(row interface{ Scan(...any) error }, p *guestAccessPlan) error {
	return row.Scan(&p.ID, &p.Code, &p.Name, &p.Description, &p.DurationSeconds,
		&p.DataCapBytes, &p.DownKbps, &p.UpKbps, &p.MaxConcurrentDevices,
		&p.ValiditySeconds, &p.PriceCents, &p.Currency, &p.IsActive,
		&p.CreatedAt, &p.UpdatedAt)
}

// planWriteReq covers create and patch. Code is create-only: the patch
// decoder uses planPatchReq (no Code field) so a PATCH carrying "code" is
// rejected by DisallowUnknownFields — that is what makes code immutable.
type planWriteReq struct {
	Code                 string  `json:"code"`
	Name                 string  `json:"name"`
	Description          *string `json:"description,omitempty"`
	DurationSeconds      *int    `json:"duration_seconds,omitempty"`
	DataCapBytes         *int64  `json:"data_cap_bytes,omitempty"`
	DownKbps             *int    `json:"down_kbps,omitempty"`
	UpKbps               *int    `json:"up_kbps,omitempty"`
	MaxConcurrentDevices *int    `json:"max_concurrent_devices,omitempty"`
	ValiditySeconds      *int    `json:"validity_seconds,omitempty"`
	PriceCents           *int    `json:"price_cents,omitempty"`
	Currency             *string `json:"currency,omitempty"`
	IsActive             *bool   `json:"is_active,omitempty"`
}

type planPatchReq struct {
	Name                 *string `json:"name,omitempty"`
	Description          *string `json:"description,omitempty"`
	DurationSeconds      *int    `json:"duration_seconds,omitempty"`
	DataCapBytes         *int64  `json:"data_cap_bytes,omitempty"`
	DownKbps             *int    `json:"down_kbps,omitempty"`
	UpKbps               *int    `json:"up_kbps,omitempty"`
	MaxConcurrentDevices *int    `json:"max_concurrent_devices,omitempty"`
	ValiditySeconds      *int    `json:"validity_seconds,omitempty"`
	PriceCents           *int    `json:"price_cents,omitempty"`
	Currency             *string `json:"currency,omitempty"`
	IsActive             *bool   `json:"is_active,omitempty"`
}

func validatePlanNumbers(duration, down, up, validity, price, mcd *int, cap *int64) string {
	nonNeg := func(p *int, name string) string {
		if p != nil && *p < 0 {
			return name + " must be >= 0"
		}
		return ""
	}
	for _, c := range []struct {
		p    *int
		name string
	}{
		{duration, "duration_seconds"}, {down, "down_kbps"}, {up, "up_kbps"},
		{validity, "validity_seconds"}, {price, "price_cents"},
	} {
		if msg := nonNeg(c.p, c.name); msg != "" {
			return msg
		}
	}
	if cap != nil && *cap < 0 {
		return "data_cap_bytes must be >= 0"
	}
	if mcd != nil && *mcd < 1 {
		return "max_concurrent_devices must be >= 1"
	}
	return ""
}

func (s *server) guestAccessPlansRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listPlans)
	r.Post("/", s.createPlan)
	r.Get("/{id}", s.getPlan)
	r.Patch("/{id}", s.patchPlan)
	r.Delete("/{id}", s.deletePlan)
	return r
}

func (s *server) listPlans(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
        SELECT `+planCols+` FROM ticket_templates
         WHERE tenant_id = $1
         ORDER BY created_at DESC, id DESC
    `, s.tenantID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	var out []guestAccessPlan
	for rows.Next() {
		var p guestAccessPlan
		if err := scanPlan(rows, &p); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, p)
	}
	writeList(w, out)
}

func (s *server) createPlan(w http.ResponseWriter, r *http.Request) {
	var in planWriteReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	if in.Code == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "code required")
		return
	}
	if in.Name == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "name required")
		return
	}
	if msg := validatePlanNumbers(in.DurationSeconds, in.DownKbps, in.UpKbps,
		in.ValiditySeconds, in.PriceCents, in.MaxConcurrentDevices, in.DataCapBytes); msg != "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()

	if !s.requireProvisioning(w, r) {
		return
	}
	if !s.enforceLimit(ctx, w, "max_guest_access_plans", 1,
		`SELECT count(*) FROM ticket_templates WHERE tenant_id = $1`, s.tenantID) {
		return
	}

	mcd := 1
	if in.MaxConcurrentDevices != nil {
		mcd = *in.MaxConcurrentDevices
	}
	active := true
	if in.IsActive != nil {
		active = *in.IsActive
	}

	var p guestAccessPlan
	err := scanPlan(s.db.QueryRow(ctx, `
        INSERT INTO ticket_templates
          (tenant_id, code, name, description, duration_seconds, data_cap_bytes,
           down_kbps, up_kbps, max_concurrent_devices, validity_seconds,
           price_cents, currency, is_active)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
        RETURNING `+planCols,
		s.tenantID, in.Code, in.Name, in.Description, in.DurationSeconds, in.DataCapBytes,
		in.DownKbps, in.UpKbps, mcd, in.ValiditySeconds,
		in.PriceCents, in.Currency, active,
	), &p)
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "plan code already exists")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "insert failed")
		return
	}
	s.audit(r, "guest_access_plan.created", "guest_access_plan", p.ID, map[string]any{
		"code": p.Code, "name": p.Name,
	})
	writeJSON(w, http.StatusCreated, p)
}

func (s *server) getPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var p guestAccessPlan
	err := scanPlan(s.db.QueryRow(ctx,
		`SELECT `+planCols+` FROM ticket_templates WHERE id = $1 AND tenant_id = $2`,
		id, s.tenantID), &p)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "plan not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) patchPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in planPatchReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body (note: code is immutable)")
		return
	}
	if msg := validatePlanNumbers(in.DurationSeconds, in.DownKbps, in.UpKbps,
		in.ValiditySeconds, in.PriceCents, in.MaxConcurrentDevices, in.DataCapBytes); msg != "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()

	var p guestAccessPlan
	err := scanPlan(s.db.QueryRow(ctx, `
        UPDATE ticket_templates SET
            name                   = COALESCE($3, name),
            description            = COALESCE($4, description),
            duration_seconds       = COALESCE($5, duration_seconds),
            data_cap_bytes         = COALESCE($6, data_cap_bytes),
            down_kbps              = COALESCE($7, down_kbps),
            up_kbps                = COALESCE($8, up_kbps),
            max_concurrent_devices = COALESCE($9, max_concurrent_devices),
            validity_seconds       = COALESCE($10, validity_seconds),
            price_cents            = COALESCE($11, price_cents),
            currency               = COALESCE($12, currency),
            is_active              = COALESCE($13, is_active),
            updated_at             = now()
         WHERE id = $1 AND tenant_id = $2
         RETURNING `+planCols,
		id, s.tenantID,
		in.Name, in.Description, in.DurationSeconds, in.DataCapBytes,
		in.DownKbps, in.UpKbps, in.MaxConcurrentDevices, in.ValiditySeconds,
		in.PriceCents, in.Currency, in.IsActive,
	), &p)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "plan not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	s.audit(r, "guest_access_plan.updated", "guest_access_plan", id, nil)
	writeJSON(w, http.StatusOK, p)
}

func (s *server) deletePlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()

	var inUse bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM vouchers WHERE template_id = $1)`, id).Scan(&inUse); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if inUse {
		jsonErr(w, http.StatusConflict, "conflict", "plan in use by vouchers; deactivate instead")
		return
	}
	ct, err := s.db.Exec(ctx,
		`DELETE FROM ticket_templates WHERE id = $1 AND tenant_id = $2`, id, s.tenantID)
	if err != nil {
		if isFKViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "plan referenced by other records; deactivate instead")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "delete failed")
		return
	}
	if ct.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "plan not found")
		return
	}
	s.audit(r, "guest_access_plan.deleted", "guest_access_plan", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ----- voucher batches ---------------------------------------------------------

type edgeVoucherBatch struct {
	ID         string    `json:"id"`
	TemplateID string    `json:"template_id"`
	Name       *string   `json:"name,omitempty"`
	Count      int       `json:"count"`
	CreatedAt  time.Time `json:"created_at"`
	// Generation metadata (null for legacy batches).
	CodeLength       *int    `json:"code_length,omitempty"`
	CharMode         *string `json:"char_mode,omitempty"`
	CodePrefix       *string `json:"code_prefix,omitempty"`
	ExcludeAmbiguous *bool   `json:"exclude_ambiguous,omitempty"`
	// State totals across the batch (filled by list/get).
	Totals *voucherTotals `json:"totals,omitempty"`
}

type voucherTotals struct {
	Unused    int `json:"unused"`
	Active    int `json:"active"`
	Exhausted int `json:"exhausted"`
	Expired   int `json:"expired"`
	Revoked   int `json:"revoked"`
}

type edgeVoucher struct {
	ID          string     `json:"id"`
	TemplateID  string     `json:"template_id"`
	BatchID     *string    `json:"batch_id,omitempty"`
	Code        string     `json:"code"`
	CodeDisplay string     `json:"code_display"`
	State       string     `json:"state"`
	IssuedAt    time.Time  `json:"issued_at"`
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	BytesUsed   int64      `json:"bytes_used"`
	SecondsUsed int        `json:"seconds_used"`
	// Plan snapshot + live device usage (detail view only).
	PlanName        *string `json:"plan_name,omitempty"`
	PlanCode        *string `json:"plan_code,omitempty"`
	DurationSeconds *int    `json:"duration_seconds,omitempty"`
	DataCapBytes    *int64  `json:"data_cap_bytes,omitempty"`
	DownKbps        *int    `json:"down_kbps,omitempty"`
	UpKbps          *int    `json:"up_kbps,omitempty"`
	MaxDevices      *int    `json:"max_devices,omitempty"`
	ActiveDevices   *int    `json:"active_devices,omitempty"`
}

func (s *server) voucherBatchesRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listVoucherBatches)
	r.Post("/", s.createVoucherBatch)
	r.Get("/{id}", s.getVoucherBatch)
	r.Get("/{id}/codes", s.listBatchCodes)
	r.Get("/{id}/codes.csv", s.exportBatchCSV)
	r.Post("/{id}/revoke", s.revokeBatch)
	r.Post("/{id}/change-plan", s.changeBatchPlan)
	return r
}

func (s *server) vouchersRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/{id}", s.getVoucher)
	r.Post("/{id}/revoke", s.revokeVoucher)
	r.Post("/{id}/change-plan", s.changeVoucherPlan)
	return r
}

// changeBatchPlan reassigns the Guest Access Plan across a batch. scope:
//   - "unused" (default): only vouchers still in state 'unused'.
//   - "eligible": unused + activated-but-idle vouchers that have NO active
//     session. Revoked/expired/exhausted vouchers and any voucher with a live
//     session are always skipped (never silently mutates a running policy).
//
// The target plan must be an active plan owned by THIS tenant. Codes and usage
// history are unchanged; the count changed is audited.
func (s *server) changeBatchPlan(w http.ResponseWriter, r *http.Request) {
	batchID := chi.URLParam(r, "id")
	var in struct {
		TemplateID string `json:"template_id"`
		Scope      string `json:"scope,omitempty"`
		Reason     string `json:"reason,omitempty"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if in.TemplateID == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "template_id (target plan) required")
		return
	}
	scope := in.Scope
	if scope == "" {
		scope = "unused"
	}
	if scope != "unused" && scope != "eligible" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "scope must be 'unused' or 'eligible'")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()

	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM voucher_batches WHERE id=$1 AND tenant_id=$2)`,
		batchID, s.tenantID).Scan(&exists); err != nil || !exists {
		jsonErr(w, http.StatusNotFound, "not_found", "batch not found")
		return
	}
	var ok bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ticket_templates WHERE id=$1 AND tenant_id=$2 AND is_active)`,
		in.TemplateID, s.tenantID).Scan(&ok); err != nil || !ok {
		jsonErr(w, http.StatusBadRequest, "bad_request", "target plan not found, inactive, or not owned by this tenant")
		return
	}
	// Only unused, or (for 'eligible') unused+active vouchers WITHOUT a live
	// session. Vouchers with an active session are excluded so we never mutate a
	// running policy.
	stateFilter := "v.state = 'unused'"
	if scope == "eligible" {
		stateFilter = "v.state IN ('unused','active')"
	}
	ct, err := s.db.Exec(ctx, `
        UPDATE vouchers v SET template_id=$3
         WHERE v.tenant_id=$1 AND v.batch_id=$2
           AND `+stateFilter+`
           AND NOT EXISTS (SELECT 1 FROM sessions s WHERE s.voucher_id = v.id AND s.state='active')
    `, s.tenantID, batchID, in.TemplateID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	s.audit(r, "voucher_batch.plan_changed", "voucher_batch", batchID, map[string]any{
		"new_template_id": in.TemplateID, "scope": scope, "vouchers_changed": ct.RowsAffected(), "reason": in.Reason,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"batch_id": batchID, "template_id": in.TemplateID, "scope": scope, "vouchers_changed": ct.RowsAffected(),
	})
}

func (s *server) listVoucherBatches(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
        SELECT b.id, b.template_id, b.name, b.count, b.created_at,
               b.code_length, b.char_mode, b.code_prefix, b.exclude_ambiguous,
               COUNT(*) FILTER (WHERE v.state='unused'),
               COUNT(*) FILTER (WHERE v.state='active'),
               COUNT(*) FILTER (WHERE v.state='exhausted'),
               COUNT(*) FILTER (WHERE v.state='expired'),
               COUNT(*) FILTER (WHERE v.state='revoked')
          FROM voucher_batches b
          LEFT JOIN vouchers v ON v.batch_id = b.id AND v.tenant_id = b.tenant_id
         WHERE b.tenant_id = $1
         GROUP BY b.id
         ORDER BY b.created_at DESC, b.id DESC
    `, s.tenantID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	var out []edgeVoucherBatch
	for rows.Next() {
		var vb edgeVoucherBatch
		var t voucherTotals
		if err := rows.Scan(&vb.ID, &vb.TemplateID, &vb.Name, &vb.Count, &vb.CreatedAt,
			&vb.CodeLength, &vb.CharMode, &vb.CodePrefix, &vb.ExcludeAmbiguous,
			&t.Unused, &t.Active, &t.Exhausted, &t.Expired, &t.Revoked); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		vb.Totals = &t
		out = append(out, vb)
	}
	writeList(w, out)
}

func (s *server) getVoucherBatch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var vb edgeVoucherBatch
	err := s.db.QueryRow(ctx, `
        SELECT id, template_id, name, count, created_at
          FROM voucher_batches WHERE id = $1 AND tenant_id = $2
    `, id, s.tenantID).Scan(&vb.ID, &vb.TemplateID, &vb.Name, &vb.Count, &vb.CreatedAt)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "batch not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, vb)
}

func (s *server) createVoucherBatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TemplateID       string  `json:"template_id"`
		Count            int     `json:"count"`
		Name             *string `json:"name,omitempty"`
		Note             *string `json:"note,omitempty"`
		CodeLength       int     `json:"code_length,omitempty"`
		CharMode         string  `json:"char_mode,omitempty"`
		CodePrefix       string  `json:"code_prefix,omitempty"`
		ExcludeAmbiguous *bool   `json:"exclude_ambiguous,omitempty"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if in.TemplateID == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "template_id required")
		return
	}
	if in.Count < 1 || in.Count > 10000 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "count must be 1..10000")
		return
	}
	// Ambiguous-character exclusion defaults ON.
	excludeAmbiguous := true
	if in.ExcludeAmbiguous != nil {
		excludeAmbiguous = *in.ExcludeAmbiguous
	}
	if in.CharMode == "" {
		in.CharMode = codegen.ModeAlnum
	}
	if in.CodeLength == 0 {
		in.CodeLength = 8
	}
	opts := codegen.Options{
		Length:           in.CodeLength,
		Mode:             in.CharMode,
		Prefix:           in.CodePrefix,
		ExcludeAmbiguous: excludeAmbiguous,
	}

	ctx, cancel := dbCtx(r)
	defer cancel()

	if !s.requireProvisioning(w, r) {
		return
	}

	var tplExists bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM ticket_templates WHERE id = $1 AND tenant_id = $2)`,
		in.TemplateID, s.tenantID).Scan(&tplExists); err != nil || !tplExists {
		jsonErr(w, http.StatusBadRequest, "bad_request", "template not found")
		return
	}

	// Generate, then GUARANTEE global uniqueness against the DB before insert:
	// vouchers has a global unique index, so regenerate any code that already
	// exists (bounded retries) instead of failing the batch on a rare collision.
	codes, err := s.generateUniqueCodes(ctx, in.Count, opts)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	var createdBy any
	if sess := sessFrom(r.Context()); sess != nil {
		createdBy = sess.OperatorID
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "begin tx failed")
		return
	}
	defer tx.Rollback(ctx)

	var vb edgeVoucherBatch
	err = tx.QueryRow(ctx, `
        INSERT INTO voucher_batches (tenant_id, template_id, name, note, count, created_by,
                                     code_length, char_mode, code_prefix, exclude_ambiguous)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9,''), $10)
        RETURNING id, template_id, name, count, created_at
    `, s.tenantID, in.TemplateID, in.Name, in.Note, in.Count, createdBy,
		opts.Length, opts.Mode, opts.Prefix, opts.ExcludeAmbiguous).Scan(
		&vb.ID, &vb.TemplateID, &vb.Name, &vb.Count, &vb.CreatedAt)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "batch insert failed")
		return
	}

	// Batched multi-row inserts: 500 rows x 4 params per statement keeps us
	// far below the wire-protocol parameter cap.
	const chunk = 500
	for start := 0; start < len(codes); start += chunk {
		end := min(start+chunk, len(codes))
		var sb strings.Builder
		sb.WriteString(`INSERT INTO vouchers (tenant_id, template_id, batch_id, code, state) VALUES `)
		args := make([]any, 0, (end-start)*4)
		for i, c := range codes[start:end] {
			if i > 0 {
				sb.WriteByte(',')
			}
			n := i * 4
			fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,'unused')", n+1, n+2, n+3, n+4)
			args = append(args, s.tenantID, in.TemplateID, vb.ID, c)
		}
		if _, err := tx.Exec(ctx, sb.String(), args...); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "voucher insert failed")
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	s.audit(r, "voucher_batch.created", "voucher_batch", vb.ID, map[string]any{
		"template_id": vb.TemplateID, "count": vb.Count,
	})
	writeJSON(w, http.StatusCreated, vb)
}

func (s *server) listBatchCodes(w http.ResponseWriter, r *http.Request) {
	batchID := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()

	var exists bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM voucher_batches WHERE id = $1 AND tenant_id = $2)`,
		batchID, s.tenantID).Scan(&exists); err != nil || !exists {
		jsonErr(w, http.StatusNotFound, "not_found", "batch not found")
		return
	}

	limit := parseLimit(r, 500, 500)
	rows, err := s.db.Query(ctx, `
        SELECT id, template_id, batch_id, code, state,
               issued_at, activated_at, expires_at, bytes_used, seconds_used
          FROM vouchers
         WHERE tenant_id = $1 AND batch_id = $2
         ORDER BY issued_at ASC, id ASC
         LIMIT $3
    `, s.tenantID, batchID, limit)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	var out []edgeVoucher
	for rows.Next() {
		var v edgeVoucher
		if err := rows.Scan(&v.ID, &v.TemplateID, &v.BatchID, &v.Code, &v.State,
			&v.IssuedAt, &v.ActivatedAt, &v.ExpiresAt, &v.BytesUsed, &v.SecondsUsed); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		v.CodeDisplay = crockford.Format(v.Code)
		out = append(out, v)
	}
	writeList(w, out)
}

func (s *server) exportBatchCSV(w http.ResponseWriter, r *http.Request) {
	batchID := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()

	var exists bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM voucher_batches WHERE id = $1 AND tenant_id = $2)`,
		batchID, s.tenantID).Scan(&exists); err != nil || !exists {
		jsonErr(w, http.StatusNotFound, "not_found", "batch not found")
		return
	}

	rows, err := s.db.Query(ctx, `
        SELECT code, state, issued_at
          FROM vouchers
         WHERE tenant_id = $1 AND batch_id = $2
         ORDER BY issued_at ASC, id ASC
    `, s.tenantID, batchID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()

	fname := "voucher-batch-" + batchID
	if len(batchID) >= 8 {
		fname = "voucher-batch-" + batchID[:8]
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.csv"`, fname))

	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"code_display", "state", "issued_at"})
	for rows.Next() {
		var code, state string
		var issued time.Time
		if err := rows.Scan(&code, &state, &issued); err != nil {
			return
		}
		_ = cw.Write([]string{crockford.Format(code), state, issued.UTC().Format(time.RFC3339)})
	}
	cw.Flush()
}

func (s *server) revokeBatch(w http.ResponseWriter, r *http.Request) {
	batchID := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()

	var exists bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM voucher_batches WHERE id = $1 AND tenant_id = $2)`,
		batchID, s.tenantID).Scan(&exists); err != nil || !exists {
		jsonErr(w, http.StatusNotFound, "not_found", "batch not found")
		return
	}

	ct, err := s.db.Exec(ctx, `
        UPDATE vouchers
           SET state = 'revoked'
         WHERE tenant_id = $1 AND batch_id = $2
           AND state IN ('unused','active')
    `, s.tenantID, batchID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "revoke failed")
		return
	}
	s.audit(r, "voucher_batch.revoked", "voucher_batch", batchID, map[string]any{
		"vouchers_revoked": ct.RowsAffected(),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"batch_id":         batchID,
		"vouchers_revoked": ct.RowsAffected(),
	})
}

// ----- individual vouchers ------------------------------------------------------

func (s *server) getVoucher(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var v edgeVoucher
	var active int64
	err := s.db.QueryRow(ctx, `
        SELECT v.id, v.template_id, v.batch_id, v.code, v.state,
               v.issued_at, v.activated_at, v.expires_at, v.bytes_used, v.seconds_used,
               t.name, t.code, t.duration_seconds, t.data_cap_bytes, t.down_kbps, t.up_kbps,
               t.max_concurrent_devices,
               (SELECT count(DISTINCT s.mac) FROM sessions s
                 WHERE s.voucher_id = v.id AND s.state='active')
          FROM vouchers v JOIN ticket_templates t ON t.id = v.template_id
         WHERE v.id = $1 AND v.tenant_id = $2
    `, id, s.tenantID).Scan(&v.ID, &v.TemplateID, &v.BatchID, &v.Code, &v.State,
		&v.IssuedAt, &v.ActivatedAt, &v.ExpiresAt, &v.BytesUsed, &v.SecondsUsed,
		&v.PlanName, &v.PlanCode, &v.DurationSeconds, &v.DataCapBytes, &v.DownKbps, &v.UpKbps,
		&v.MaxDevices, &active)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "voucher not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	ai := int(active)
	v.ActiveDevices = &ai
	v.CodeDisplay = crockford.Format(v.Code)
	writeJSON(w, http.StatusOK, v)
}

// changeVoucherPlan reassigns the Guest Access Plan of a SINGLE voucher.
// Lifecycle rules:
//   - revoked / expired / exhausted vouchers cannot be edited (409).
//   - a voucher with an ACTIVE guest session cannot be repointed, because acctd
//     derives that running session's quota live from the voucher's plan — the
//     operator must end the session first (or the change would silently mutate
//     a running policy). Unused / activated-but-idle vouchers change immediately
//     and the new plan governs all FUTURE authorizations.
//   - the target plan must be an ACTIVE plan owned by THIS tenant (never cross-
//     tenant). The code, usage history and audit trail are unchanged.
func (s *server) changeVoucherPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		TemplateID string `json:"template_id"`
		Reason     string `json:"reason,omitempty"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if in.TemplateID == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "template_id (target plan) required")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()

	var state, oldPlan string
	if err := s.db.QueryRow(ctx, `SELECT state, template_id FROM vouchers WHERE id=$1 AND tenant_id=$2`,
		id, s.tenantID).Scan(&state, &oldPlan); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "voucher not found")
		return
	}
	if state == "revoked" || state == "expired" || state == "exhausted" {
		jsonErr(w, http.StatusConflict, "conflict", "voucher is "+state+"; its plan cannot be changed")
		return
	}
	// Target plan must be active and owned by this tenant (blocks cross-tenant).
	var ok bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ticket_templates WHERE id=$1 AND tenant_id=$2 AND is_active)`,
		in.TemplateID, s.tenantID).Scan(&ok); err != nil || !ok {
		jsonErr(w, http.StatusBadRequest, "bad_request", "target plan not found, inactive, or not owned by this tenant")
		return
	}
	// Block if the voucher currently has an active session (would mutate a
	// running policy). The operator ends the session first.
	var activeSessions int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE voucher_id=$1 AND state='active'`, id).Scan(&activeSessions); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if activeSessions > 0 {
		jsonErr(w, http.StatusConflict, "active_sessions", "voucher has an active guest session; disconnect it before changing the plan")
		return
	}
	if _, err := s.db.Exec(ctx, `UPDATE vouchers SET template_id=$3 WHERE id=$1 AND tenant_id=$2`,
		id, s.tenantID, in.TemplateID); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	s.audit(r, "voucher.plan_changed", "voucher", id, map[string]any{
		"previous_template_id": oldPlan, "new_template_id": in.TemplateID, "reason": in.Reason,
	})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "template_id": in.TemplateID})
}

func (s *server) revokeVoucher(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	ct, err := s.db.Exec(ctx, `
        UPDATE vouchers SET state = 'revoked'
         WHERE id = $1 AND tenant_id = $2 AND state IN ('unused','active')
    `, id, s.tenantID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "revoke failed")
		return
	}
	if ct.RowsAffected() == 0 {
		// Distinguish "no such voucher" from "already terminal".
		var state string
		err := s.db.QueryRow(ctx,
			`SELECT state FROM vouchers WHERE id = $1 AND tenant_id = $2`, id, s.tenantID).Scan(&state)
		if isNoRows(err) {
			jsonErr(w, http.StatusNotFound, "not_found", "voucher not found")
			return
		}
		jsonErr(w, http.StatusConflict, "conflict", "voucher is "+state+"; only unused/active can be revoked")
		return
	}
	s.audit(r, "voucher.revoked", "voucher", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "state": "revoked"})
}
