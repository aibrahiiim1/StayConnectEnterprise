package api

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/crockford"
)

// ---- Types ------------------------------------------------------------------

type VoucherBatch struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenant_id"`
	TemplateID string    `json:"template_id"`
	Name       *string   `json:"name,omitempty"`
	Note       *string   `json:"note,omitempty"`
	Count      int       `json:"count"`
	CreatedBy  *string   `json:"created_by,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type Voucher struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenant_id"`
	TemplateID  string     `json:"template_id"`
	BatchID     *string    `json:"batch_id,omitempty"`
	Code        string     `json:"code"`         // canonical (no dashes)
	CodeDisplay string     `json:"code_display"` // XXXX-XXXX-XXXX
	State       string     `json:"state"`
	IssuedAt    time.Time  `json:"issued_at"`
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	BytesUsed   int64      `json:"bytes_used"`
	SecondsUsed int        `json:"seconds_used"`
}

type createBatchReq struct {
	TemplateID string  `json:"template_id"`
	Count      int     `json:"count"`
	Name       *string `json:"name,omitempty"`
	Note       *string `json:"note,omitempty"`
}

// ---- Routes -----------------------------------------------------------------

func (b *Base) VoucherBatchesRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireTenant)
	r.Get("/", b.listVoucherBatches)
	r.Post("/", b.createVoucherBatch)
	r.Get("/{id}", b.getVoucherBatch)
	r.Get("/{id}/codes", b.listBatchCodes)
	r.Get("/{id}/codes.csv", b.exportBatchCSV)
	r.Post("/{id}/revoke", b.revokeBatch)
	return r
}

func (b *Base) VouchersRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireTenant)
	r.Get("/{id}", b.getVoucher)
	r.Post("/{id}/revoke", b.revokeVoucher)
	return r
}

// ---- List / Get batches -----------------------------------------------------

func (b *Base) listVoucherBatches(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	limit := ParseLimit(r, 50, 200)
	curT, curI, err := DecodeCursor(r.URL.Query().Get("cursor"))
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

	rows, err := b.DB.Query(ctx, `
        SELECT id, tenant_id, template_id, name, note, count, created_by, created_at
          FROM voucher_batches
         WHERE tenant_id = $1
           AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3::uuid))
         ORDER BY created_at DESC, id DESC
         LIMIT $4
    `, tenantID, tArg, iArg, limit+1)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()

	var out []VoucherBatch
	for rows.Next() {
		var vb VoucherBatch
		var createdBy *string
		if err := rows.Scan(&vb.ID, &vb.TenantID, &vb.TemplateID, &vb.Name, &vb.Note,
			&vb.Count, &createdBy, &vb.CreatedAt); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		vb.CreatedBy = createdBy
		out = append(out, vb)
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

func (b *Base) getVoucherBatch(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	var vb VoucherBatch
	var createdBy *string
	err := b.DB.QueryRow(ctx, `
        SELECT id, tenant_id, template_id, name, note, count, created_by, created_at
          FROM voucher_batches WHERE id = $1 AND tenant_id = $2
    `, id, tenantID).Scan(&vb.ID, &vb.TenantID, &vb.TemplateID, &vb.Name, &vb.Note,
		&vb.Count, &createdBy, &vb.CreatedAt)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "batch not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	vb.CreatedBy = createdBy
	WriteJSON(w, http.StatusOK, vb)
}

// ---- Create batch -----------------------------------------------------------

func (b *Base) createVoucherBatch(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	sess := auth.FromContext(r.Context())

	var req createBatchReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if req.TemplateID == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "template_id required")
		return
	}
	if req.Count < 1 || req.Count > 10000 {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "count must be 1..10000")
		return
	}

	ctx, cancel := DBCtx(r)
	defer cancel()

	// Template must belong to this tenant.
	var n int
	if err := b.DB.QueryRow(ctx,
		`SELECT count(*) FROM ticket_templates WHERE id = $1 AND tenant_id = $2`,
		req.TemplateID, tenantID).Scan(&n); err != nil || n == 0 {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "template not found in this tenant")
		return
	}

	// Limit: max_vouchers_per_month (rolling calendar month). Commercial
	// limits come from the cloud schema even when this deprecated adapter
	// writes to a site database (b.LimitsDB), counts from where rows live.
	if !enforceCreateLimitDeltaSplit(ctx, b.limitsPool(), b.DB, w, r, tenantID, "max_vouchers_per_month", int64(req.Count),
		`SELECT count(*) FROM vouchers
           WHERE tenant_id = $1 AND issued_at >= date_trunc('month', now())`,
		tenantID) {
		return
	}

	codes, err := crockford.GenerateN(req.Count)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "code generation failed")
		return
	}

	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "begin tx failed")
		return
	}
	defer tx.Rollback(ctx)

	// created_by references the operators table of the database this
	// (deprecated-compat) route writes to. Post-cutover that is the SITE
	// database, where cloud/platform operators intentionally do not exist —
	// record NULL there instead of failing the FK; the audit log keeps the
	// actor either way.
	var createdByArg any = sess.OperatorID
	var opExists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM operators WHERE id = $1)`, sess.OperatorID).Scan(&opExists); err == nil && !opExists {
		createdByArg = nil
	}

	var batch VoucherBatch
	var createdBy *string
	err = tx.QueryRow(ctx, `
        INSERT INTO voucher_batches (tenant_id, template_id, name, note, count, created_by)
        VALUES ($1, $2, $3, $4, $5, $6)
        RETURNING id, tenant_id, template_id, name, note, count, created_by, created_at
    `, tenantID, req.TemplateID, req.Name, req.Note, req.Count, createdByArg).Scan(
		&batch.ID, &batch.TenantID, &batch.TemplateID, &batch.Name, &batch.Note,
		&batch.Count, &createdBy, &batch.CreatedAt,
	)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "batch insert failed")
		return
	}
	batch.CreatedBy = createdBy

	// Bulk insert vouchers. COPY FROM would be faster, but for ≤10k rows the
	// VALUES-batch is plenty fast and keeps the code simple.
	rows := make([][]any, 0, len(codes))
	for _, c := range codes {
		rows = append(rows, []any{tenantID, req.TemplateID, batch.ID, c})
	}
	if _, err := tx.CopyFrom(
		ctx,
		pgx.Identifier{"vouchers"},
		[]string{"tenant_id", "template_id", "batch_id", "code"},
		pgx.CopyFromRows(rows),
	); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "voucher insert failed: "+err.Error())
		return
	}

	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "voucher_batch.created", "voucher_batch", batch.ID, map[string]any{
		"_tenant_id": tenantID, "template_id": batch.TemplateID, "count": batch.Count,
	})
	WriteJSON(w, http.StatusCreated, batch)
}

// ---- List codes in batch ----------------------------------------------------

func (b *Base) listBatchCodes(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	batchID := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()

	limit := ParseLimit(r, 50, 500)
	curT, curI, err := DecodeCursor(r.URL.Query().Get("cursor"))
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

	rows, err := b.DB.Query(ctx, `
        SELECT id, tenant_id, template_id, batch_id, code, state,
               issued_at, activated_at, expires_at, bytes_used, seconds_used
          FROM vouchers
         WHERE tenant_id = $1 AND batch_id = $2
           AND ($3::timestamptz IS NULL OR (issued_at, id) < ($3, $4::uuid))
         ORDER BY issued_at ASC, id ASC
         LIMIT $5
    `, tenantID, batchID, tArg, iArg, limit+1)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()

	var out []Voucher
	for rows.Next() {
		var v Voucher
		if err := rows.Scan(&v.ID, &v.TenantID, &v.TemplateID, &v.BatchID,
			&v.Code, &v.State, &v.IssuedAt, &v.ActivatedAt, &v.ExpiresAt,
			&v.BytesUsed, &v.SecondsUsed); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		v.CodeDisplay = crockford.Format(v.Code)
		out = append(out, v)
	}
	meta := ListMeta{}
	if len(out) > limit {
		last := out[limit-1]
		meta.HasMore = true
		meta.Cursor = EncodeCursor(last.IssuedAt, last.ID)
		out = out[:limit]
	}
	WriteList(w, out, meta)
}

// ---- CSV export -------------------------------------------------------------

func (b *Base) exportBatchCSV(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	batchID := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()

	// Batch must be in this tenant.
	var batchName *string
	err := b.DB.QueryRow(ctx,
		`SELECT name FROM voucher_batches WHERE id = $1 AND tenant_id = $2`,
		batchID, tenantID).Scan(&batchName)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "batch not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}

	rows, err := b.DB.Query(ctx, `
        SELECT code, state, issued_at
          FROM vouchers
         WHERE tenant_id = $1 AND batch_id = $2
         ORDER BY issued_at ASC, id ASC
    `, tenantID, batchID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()

	fname := "voucher-batch-" + batchID[:8] + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fname))

	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"code", "display_code", "state", "issued_at"})
	for rows.Next() {
		var code, state string
		var issued time.Time
		if err := rows.Scan(&code, &state, &issued); err != nil {
			return
		}
		_ = cw.Write([]string{code, crockford.Format(code), state, issued.UTC().Format(time.RFC3339)})
	}
	cw.Flush()
}

// ---- Revoke batch / voucher -------------------------------------------------

func (b *Base) revokeBatch(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	batchID := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()

	ct, err := b.DB.Exec(ctx, `
        UPDATE vouchers
           SET state = 'revoked'
         WHERE tenant_id = $1 AND batch_id = $2
           AND state IN ('unused','active')
    `, tenantID, batchID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "revoke failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "voucher_batch.revoked", "voucher_batch", batchID, map[string]any{
		"_tenant_id": tenantID, "vouchers_revoked": ct.RowsAffected(),
	})
	WriteJSON(w, http.StatusOK, map[string]any{
		"batch_id":         batchID,
		"vouchers_revoked": ct.RowsAffected(),
	})
}

func (b *Base) getVoucher(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	var v Voucher
	err := b.DB.QueryRow(ctx, `
        SELECT id, tenant_id, template_id, batch_id, code, state,
               issued_at, activated_at, expires_at, bytes_used, seconds_used
          FROM vouchers WHERE id = $1 AND tenant_id = $2
    `, id, tenantID).Scan(&v.ID, &v.TenantID, &v.TemplateID, &v.BatchID,
		&v.Code, &v.State, &v.IssuedAt, &v.ActivatedAt, &v.ExpiresAt,
		&v.BytesUsed, &v.SecondsUsed)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "voucher not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	v.CodeDisplay = crockford.Format(v.Code)
	WriteJSON(w, http.StatusOK, v)
}

func (b *Base) revokeVoucher(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	ct, err := b.DB.Exec(ctx, `
        UPDATE vouchers SET state = 'revoked'
         WHERE id = $1 AND tenant_id = $2 AND state IN ('unused','active')
    `, id, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "revoke failed")
		return
	}
	if ct.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "voucher not found or already terminal")
		return
	}
	audit.Op(r.Context(), b.DB, r, "voucher.revoked", "voucher", id, map[string]any{"_tenant_id": tenantID})
	w.WriteHeader(http.StatusNoContent)
}

// ---- Helpers ----------------------------------------------------------------

// EnforceCreateLimitDelta is like EnforceCreateLimit but adds `delta` to the
// existing count (for bulk operations like voucher-batch creation).
func EnforceCreateLimitDelta(
	ctx context.Context,
	db *pgxpool.Pool,
	w http.ResponseWriter,
	r *http.Request,
	tenantID, limitKey string,
	delta int64,
	countQuery string,
	countArgs ...any,
) bool {
	return enforceCreateLimitDeltaSplit(ctx, db, db, w, r, tenantID, limitKey, delta, countQuery, countArgs...)
}

// enforceCreateLimitDeltaSplit separates where the limit is defined
// (limitsDB — cloud commercial schema) from where the rows are counted
// (countDB — possibly a site database behind a compatibility adapter).
func enforceCreateLimitDeltaSplit(
	ctx context.Context,
	limitsDB, countDB *pgxpool.Pool,
	w http.ResponseWriter,
	r *http.Request,
	tenantID, limitKey string,
	delta int64,
	countQuery string,
	countArgs ...any,
) bool {
	lim, err := GetIntLimit(ctx, limitsDB, tenantID, limitKey)
	if err != nil {
		if errors.Is(err, ErrNoSubscription) {
			Fail(w, r, http.StatusPaymentRequired, CodePaymentRequired, "no active subscription")
			return false
		}
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "limits lookup failed")
		return false
	}
	if lim == -1 || lim == 0 {
		return true
	}
	var count int64
	if err := countDB.QueryRow(ctx, countQuery, countArgs...).Scan(&count); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "limits count failed")
		return false
	}
	if count+delta > lim {
		Fail(w, r, http.StatusForbidden, CodeLimitExceeded, "license limit reached", map[string]any{
			"limit_key": limitKey,
			"limit":     lim,
			"current":   count,
		})
		return false
	}
	return true
}
