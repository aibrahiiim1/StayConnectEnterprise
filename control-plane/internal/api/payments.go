package api

// Guest-facing payment flow (phase 12).
//
// Three public endpoints (no operator session required — guests are
// anonymous at this point):
//
//   POST /v1/checkout/create         → create a Stripe Checkout session
//   GET  /v1/checkout/{session_id}   → poll status + retrieve voucher code
//   POST /v1/webhooks/stripe/{tenant_id} → receive Stripe events
//
// Plus one operator-authenticated endpoint for viewing history:
//
//   GET  /v1/payments (mounted under the auth group in router.go)
//
// Idempotency: the stripe_events table has event_id as PK.
// `INSERT … ON CONFLICT DO NOTHING` acts as our lock — if the INSERT
// inserts 0 rows, we've already processed this event and can return 200
// without re-issuing a voucher.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/crockford"
	"github.com/stayconnect/enterprise/control-plane/internal/metrics"
	"github.com/stayconnect/enterprise/control-plane/internal/stripe"
)

type PaymentsBase struct {
	*Base
	Metrics *metrics.Registry
	// StripeNewer is an override hook for tests — lets them swap the
	// Stripe client base URL without touching the production constructor.
	StripeNewer func(secretKey string) *stripe.Client
}

// Routes returns the public-facing routes (checkout create/poll + webhook).
// The admin-only payments list is mounted separately under the auth group.
func (p *PaymentsBase) PublicRoutes() http.Handler {
	r := chi.NewRouter()
	r.Post("/checkout/create", p.createCheckout)
	r.Get("/checkout/{session_id}", p.getCheckout)
	r.Post("/webhooks/stripe/{tenant_id}", p.stripeWebhook)
	return r
}

// AdminRoutes holds the operator-authenticated view; mount under the auth
// group in the router.
func (p *PaymentsBase) AdminRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireTenant)
	r.Get("/", p.list)
	return r
}

// ----- guest: create checkout -----

type checkoutCreateReq struct {
	TenantID   string `json:"tenant_id"`
	SiteID     string `json:"site_id"`
	TemplateID string `json:"template_id"`
	IP         string `json:"ip"`
	MAC        string `json:"mac"`
}
type checkoutCreateResp struct {
	SessionID string `json:"session_id"`
	URL       string `json:"checkout_url"`
}

func (p *PaymentsBase) createCheckout(w http.ResponseWriter, r *http.Request) {
	var req checkoutCreateReq
	if err := DecodeJSON(r, &req); err != nil {
		p.metric("create", "bad_body")
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if req.TenantID == "" || req.TemplateID == "" {
		p.metric("create", "bad_body")
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "tenant_id and template_id required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	// Load Stripe account + template atomically so we fail fast on
	// misconfigured tenants.
	var (
		secretKey, successTpl, cancelTpl string
		tplName, tplCurrency             string
		tplPrice                         int64
	)
	if err := p.DB.QueryRow(ctx, `
        SELECT s.secret_key, s.success_url, s.cancel_url,
               t.name, COALESCE(t.price_cents,0), COALESCE(t.currency,'')
          FROM stripe_accounts s
          JOIN ticket_templates t ON t.id = $2 AND t.tenant_id = s.tenant_id
         WHERE s.tenant_id = $1 AND s.enabled = true AND t.is_active = true
    `, req.TenantID, req.TemplateID).Scan(&secretKey, &successTpl, &cancelTpl,
		&tplName, &tplPrice, &tplCurrency); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			p.metric("create", "not_configured")
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "stripe not configured or template not found")
			return
		}
		p.metric("create", "internal")
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "lookup failed")
		return
	}
	if tplPrice <= 0 || tplCurrency == "" {
		p.metric("create", "bad_template")
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "template has no price")
		return
	}

	// Pre-insert the payment row with status=pending. The webhook lands
	// later and flips status to paid — the stripe_session_id UNIQUE
	// constraint is our join key.
	var paymentID string
	var siteArg, ipArg, macArg any
	if req.SiteID != "" {
		siteArg = req.SiteID
	}
	if req.IP != "" {
		ipArg = req.IP
	}
	if req.MAC != "" {
		macArg = req.MAC
	}
	// Use a placeholder session id until Stripe gives us one; then update.
	if err := p.DB.QueryRow(ctx, `
        INSERT INTO payments(tenant_id, site_id, template_id, stripe_session_id,
                             status, amount_cents, currency, client_ip, client_mac)
        VALUES ($1, $2, $3, $4, 'pending', $5, $6, $7::inet, $8::macaddr)
        RETURNING id
    `, req.TenantID, siteArg, req.TemplateID, "pending-"+genNonce(),
		tplPrice, tplCurrency, ipArg, macArg).Scan(&paymentID); err != nil {
		p.metric("create", "insert_failed")
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "payment row insert failed")
		return
	}

	client := p.stripeClient(secretKey)
	cr, err := client.CreateCheckoutSession(r.Context(), stripe.CheckoutRequest{
		SuccessURL: substitutePlaceholder(successTpl),
		CancelURL:  substitutePlaceholder(cancelTpl),
		LineItems: []stripe.CheckoutLineItem{{
			Name: tplName, AmountCents: tplPrice,
			Currency: tplCurrency, Quantity: 1,
		}},
		Metadata: map[string]string{
			"stayconnect_payment_id": paymentID,
			"stayconnect_tenant_id":  req.TenantID,
		},
	})
	if err != nil {
		// Roll back our placeholder row so we don't leak pending records.
		_, _ = p.DB.Exec(ctx, `DELETE FROM payments WHERE id=$1`, paymentID)
		slog.Warn("stripe checkout create failed", "err", err, "tenant", req.TenantID)
		p.metric("create", "stripe_error")
		Fail(w, r, http.StatusBadGateway, CodeBadGateway, "checkout create failed")
		return
	}

	// Replace the placeholder session id with Stripe's. Unique constraint
	// holds because the placeholder was unique.
	if _, err := p.DB.Exec(ctx,
		`UPDATE payments SET stripe_session_id=$2, updated_at=now() WHERE id=$1`,
		paymentID, cr.ID); err != nil {
		slog.Warn("payments: swap session id failed", "err", err)
		// Non-fatal — Stripe still has our metadata so the webhook can
		// find the payment by metadata.stayconnect_payment_id.
	}

	p.metric("create", "ok")
	WriteJSON(w, http.StatusOK, checkoutCreateResp{SessionID: cr.ID, URL: cr.URL})
}

// ----- guest: poll session -----

type checkoutStatusResp struct {
	SessionID   string `json:"session_id"`
	Status      string `json:"status"`
	VoucherCode string `json:"voucher_code,omitempty"`
}

func (p *PaymentsBase) getCheckout(w http.ResponseWriter, r *http.Request) {
	sessID := chi.URLParam(r, "session_id")
	if sessID == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "session_id required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var (
		status string
		code   *string
	)
	err := p.DB.QueryRow(ctx, `
        SELECT p.status, v.code
          FROM payments p
          LEFT JOIN vouchers v ON v.id = p.voucher_id
         WHERE p.stripe_session_id = $1
    `, sessID).Scan(&status, &code)
	if errors.Is(err, pgx.ErrNoRows) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "session not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "lookup failed")
		return
	}
	resp := checkoutStatusResp{SessionID: sessID, Status: status}
	if code != nil {
		resp.VoucherCode = *code
	}
	WriteJSON(w, http.StatusOK, resp)
}

// ----- webhook handler -----

func (p *PaymentsBase) stripeWebhook(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenant_id")
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		p.metric("webhook", "read_failed")
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}

	ctx, cancel := DBCtx(r)
	defer cancel()
	var webhookSecret string
	if err := p.DB.QueryRow(ctx,
		`SELECT webhook_secret FROM stripe_accounts WHERE tenant_id=$1 AND enabled=true`,
		tenantID).Scan(&webhookSecret); err != nil {
		p.metric("webhook", "no_account")
		http.Error(w, "unknown tenant", http.StatusForbidden)
		return
	}

	evt, err := stripe.VerifyWebhook(body, r.Header.Get("Stripe-Signature"), webhookSecret, time.Now())
	if err != nil {
		slog.Warn("stripe webhook verify", "err", err, "tenant", tenantID)
		p.metric("webhook", "signature_fail")
		http.Error(w, "bad signature", http.StatusBadRequest)
		return
	}

	// Idempotency gate: insert event; 0 rows affected → already processed.
	tag, err := p.DB.Exec(ctx, `
        INSERT INTO stripe_events(event_id, tenant_id, event_type)
        VALUES ($1, $2, $3)
        ON CONFLICT (event_id) DO NOTHING
    `, evt.ID, tenantID, evt.Type)
	if err != nil {
		p.metric("webhook", "dedupe_failed")
		http.Error(w, "dedupe failed", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		// Stripe retries — we already handled this event.
		p.metric("webhook", "idempotent_skip")
		w.WriteHeader(http.StatusOK)
		return
	}

	if evt.Type != "checkout.session.completed" {
		// Accept but no-op on event types we don't handle yet.
		p.metric("webhook", "ignored_type")
		w.WriteHeader(http.StatusOK)
		return
	}

	var sess stripe.CheckoutSessionObject
	if err := json.Unmarshal(evt.Data.Object, &sess); err != nil {
		p.metric("webhook", "parse_failed")
		http.Error(w, "bad event body", http.StatusBadRequest)
		return
	}
	if sess.PaymentStatus != "paid" {
		// Event fired for an unpaid completion (e.g. async method declined).
		p.metric("webhook", "unpaid")
		_, _ = p.DB.Exec(ctx, `
            UPDATE payments SET status='failed', updated_at=now()
             WHERE stripe_session_id=$1
        `, sess.ID)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Issue the voucher + link to the payment in one transaction so a
	// partial crash doesn't leave a paid row with no voucher.
	if err := p.issueVoucherForSession(ctx, tenantID, &sess); err != nil {
		slog.Error("stripe webhook voucher issue failed", "err", err, "event", evt.ID)
		p.metric("webhook", "voucher_fail")
		// Don't ACK — Stripe will retry, and next time the stripe_events
		// row is gone (since we're going to delete it), re-try cleanly.
		_, _ = p.DB.Exec(ctx, `DELETE FROM stripe_events WHERE event_id=$1`, evt.ID)
		http.Error(w, "voucher issue failed", http.StatusInternalServerError)
		return
	}

	if p.Metrics != nil {
		p.Metrics.PaymentAmountCents.WithLabelValues(tenantID, strings.ToLower(sess.Currency)).Add(float64(sess.AmountTotal))
	}
	p.metric("webhook", "ok")
	audit.Op(r.Context(), p.DB, r, "payment.completed", "payment", sess.ID, map[string]any{
		"_tenant_id": tenantID, "amount_cents": sess.AmountTotal, "currency": sess.Currency,
	})
	w.WriteHeader(http.StatusOK)
}

func (p *PaymentsBase) issueVoucherForSession(ctx context.Context, tenantID string, sess *stripe.CheckoutSessionObject) error {
	paymentID := sess.Metadata["stayconnect_payment_id"]
	tx, err := p.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var templateID string
	// If metadata told us the payment row, use it; otherwise look up by
	// session id. (Normally the update in createCheckout already wrote
	// the session id, but the metadata lookup is a safety net.)
	if paymentID != "" {
		if err := tx.QueryRow(ctx, `SELECT template_id FROM payments WHERE id=$1 AND tenant_id=$2`,
			paymentID, tenantID).Scan(&templateID); err != nil {
			return err
		}
	} else {
		if err := tx.QueryRow(ctx, `SELECT id, template_id FROM payments WHERE stripe_session_id=$1 AND tenant_id=$2`,
			sess.ID, tenantID).Scan(&paymentID, &templateID); err != nil {
			return err
		}
	}

	// Short-circuit if we've already issued a voucher for this payment.
	// Covers the race where two webhook retries slip past the stripe_events
	// gate under concurrent dispatch (rare but not impossible).
	var alreadyVoucher *string
	_ = tx.QueryRow(ctx, `SELECT voucher_id::text FROM payments WHERE id=$1`, paymentID).Scan(&alreadyVoucher)
	if alreadyVoucher != nil && *alreadyVoucher != "" {
		return tx.Commit(ctx)
	}

	code, err := crockford.Generate()
	if err != nil {
		return err
	}
	var voucherID string
	if err := tx.QueryRow(ctx, `
        INSERT INTO vouchers(tenant_id, template_id, code, state, issued_at, metadata)
        VALUES ($1, $2, $3, 'unused', now(),
                jsonb_build_object('payment_id', $4::text, 'stripe_session_id', $5::text))
        RETURNING id
    `, tenantID, templateID, code, paymentID, sess.ID).Scan(&voucherID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
        UPDATE payments SET status='paid', voucher_id=$2,
                            stripe_payment_intent=NULLIF($3,''),
                            completed_at=now(), updated_at=now()
         WHERE id=$1
    `, paymentID, voucherID, sess.PaymentIntent); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ----- admin: list -----

type PaymentRow struct {
	ID              string     `json:"id"`
	TenantID        string     `json:"tenant_id"`
	SiteID          string     `json:"site_id,omitempty"`
	TemplateID      string     `json:"template_id"`
	StripeSessionID string     `json:"stripe_session_id"`
	Status          string     `json:"status"`
	AmountCents     int64      `json:"amount_cents"`
	Currency        string     `json:"currency"`
	VoucherID       string     `json:"voucher_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

func (p *PaymentsBase) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	limit := ParseLimit(r, 50, 200)
	rows, err := p.DB.Query(ctx, `
        SELECT id, tenant_id, COALESCE(site_id::text,''), template_id::text,
               stripe_session_id, status, amount_cents, currency,
               COALESCE(voucher_id::text,''), created_at, completed_at
          FROM payments
         WHERE tenant_id = $1
         ORDER BY created_at DESC
         LIMIT $2
    `, tenantID, limit)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	var out []PaymentRow
	for rows.Next() {
		var p PaymentRow
		if err := rows.Scan(&p.ID, &p.TenantID, &p.SiteID, &p.TemplateID,
			&p.StripeSessionID, &p.Status, &p.AmountCents, &p.Currency,
			&p.VoucherID, &p.CreatedAt, &p.CompletedAt); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, p)
	}
	WriteList(w, out, ListMeta{})
}

// ----- helpers -----

func (p *PaymentsBase) stripeClient(secretKey string) *stripe.Client {
	if p.StripeNewer != nil {
		return p.StripeNewer(secretKey)
	}
	return stripe.New(secretKey)
}

func (p *PaymentsBase) metric(kind, result string) {
	if p.Metrics == nil {
		return
	}
	switch kind {
	case "create":
		p.Metrics.PaymentCheckoutTotal.WithLabelValues(result).Inc()
	case "webhook":
		p.Metrics.PaymentWebhookTotal.WithLabelValues(result).Inc()
	}
}

func substitutePlaceholder(tpl string) string {
	// Stripe's own placeholder — passed through for them to substitute.
	// We could template-replace with {SESSION_ID} here if operators
	// preferred their own placeholder shape, but keep it as Stripe
	// documents for now.
	return tpl
}

// genNonce produces a short placeholder session id (used only until the
// real Stripe session id is known). 12 hex chars = 48 bits, collision
// probability is negligible for concurrent-call counts we'll ever see.
func genNonce() string {
	code, err := crockford.Generate()
	if err != nil {
		// Extremely unlikely (crypto/rand failure); fall back to
		// timestamp-based to keep the INSERT unique.
		return "ts-" + time.Now().Format("20060102150405.000000")
	}
	return code
}
