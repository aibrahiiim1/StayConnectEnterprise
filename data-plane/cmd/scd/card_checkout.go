package main

// CARD PAYMENT IN scd.
//
// scd owns the payment key (it seals provider credentials) and runs the checkout engine with two dedicated
// credentials: svc_payment (checkout, intent, paid grant) and svc_payment_outcome (applying what the provider
// said). svc_scd itself holds neither. What the browser reports is never proof: the portal's return page asks
// scd, and scd asks the provider (payment.CheckoutEngine.Reconcile). A background reconciler does the same for
// every open checkout, so a client who paid and never came back is still granted.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/deployment"
	"github.com/stayconnect/enterprise/data-plane/internal/localkeys"
	"github.com/stayconnect/enterprise/data-plane/internal/payment"
	"github.com/stayconnect/enterprise/data-plane/internal/payment/paymob"
	"github.com/stayconnect/enterprise/data-plane/internal/payment/stripe"
	lic "github.com/stayconnect/enterprise/license"
)

const paymentKeyFile = "payment_dek.key"

// cardState is scd's card-payment wiring. card is nil when card payment is not deployed or not configured;
// every surface then reports "not ready" and nothing executes.
type cardState struct {
	engine     *payment.CheckoutEngine
	key        payment.PaymentKey
	adapters   map[string]payment.HostedCheckoutProvider
	unready    string // why card payment cannot run on this appliance at all ("" when it can)
	statusMu   sync.Mutex
	lastStatus map[string]time.Time // per-transaction rate limit for portal-driven status checks
}

// initCard builds the card engine when the deployment ceiling allows card payment. Missing credentials or key
// are not fatal: card payment is simply not ready, which the Payment methods screen explains.
func (s *server) initCard(ctx context.Context, secretsDir string) {
	s.card = &cardState{lastStatus: map[string]time.Time{}}
	s.card.adapters = map[string]payment.HostedCheckoutProvider{"stripe": stripe.New()}
	for _, region := range []string{"egypt", "ksa", "uae", "oman"} {
		a, err := paymob.New(paymob.WithRegion(region))
		if err != nil {
			continue
		}
		key := "paymob"
		if region != "egypt" {
			key = "paymob:" + region
		}
		s.card.adapters[key] = a
	}
	if !s.ceiling.Available(deployment.CapCardPayment) {
		s.card.unready = "CARD_NOT_DEPLOYED"
		return
	}
	raw, err := localkeys.LoadExistingKey(filepath.Join(secretsDir, paymentKeyFile))
	if err != nil {
		s.card.unready = "PAYMENT_KEY_MISSING"
		slog.Warn("card payment: payment key unavailable (run keybootstrap at deploy)", "err", err)
		return
	}
	k, err := payment.NewPaymentKey(raw)
	if err != nil {
		s.card.unready = "PAYMENT_KEY_MISSING"
		return
	}
	s.card.key = k
	runtimeDSN := os.Getenv("SCD_PAYMENT_DB_URL")
	outcomeDSN := os.Getenv(payment.EnvOutcomeDSN)
	if runtimeDSN == "" || outcomeDSN == "" {
		s.card.unready = "PAYMENT_CREDENTIALS_MISSING"
		slog.Warn("card payment: payment database credentials are not configured")
		return
	}
	rt, err := pgxpool.New(ctx, runtimeDSN)
	if err != nil {
		s.card.unready = "PAYMENT_DATABASE_UNAVAILABLE"
		return
	}
	oc, err := pgxpool.New(ctx, outcomeDSN)
	if err != nil {
		rt.Close()
		s.card.unready = "PAYMENT_DATABASE_UNAVAILABLE"
		return
	}
	s.card.engine = &payment.CheckoutEngine{Runtime: rt, Outcome: oc, Adapters: s.card.adapters, Key: k,
		LiveAllowed: s.ceiling.Available(deployment.CapCardLive), Health: payment.NewProviderHealth()}
	slog.Info("card payment engine ready", "live_allowed", s.card.engine.LiveAllowed)
}

// cardReadiness is the readiness probe for the card_payment module: a default ACTIVE account whose provider
// has an adapter, whose credentials open and are complete, whose mode is permitted, and whose provider is
// answering. Readiness only stops NEW purchases; configuration and history stay available.
func (s *server) cardReadiness(ctx context.Context, tenantID, siteID string) (bool, []string) {
	if s.card == nil || s.card.unready != "" {
		r := "CARD_NOT_CONFIGURED"
		if s.card != nil {
			r = s.card.unready
		}
		return false, []string{r}
	}
	acct, err := s.card.engine.ResolveAccount(ctx, tenantID, siteID)
	if err != nil {
		if errors.Is(err, payment.ErrCredentials) {
			return false, []string{"PROVIDER_CREDENTIALS_MISSING"}
		}
		return false, []string{"NO_ACTIVE_PAYMENT_ACCOUNT"}
	}
	if acct.Mode == payment.ModeLive && !s.card.engine.LiveAllowed {
		return false, []string{"LIVE_MODE_NOT_AUTHORISED"}
	}
	ad, ok := s.card.adapters[payment.AdapterKey(acct.Provider, acct.Credentials)]
	if !ok {
		return false, []string{"PROVIDER_NOT_SUPPORTED"}
	}
	for _, k := range ad.CredentialKeys() {
		if k.Required && strings.TrimSpace(acct.Credentials[k.Key]) == "" {
			return false, []string{"PROVIDER_CREDENTIALS_INCOMPLETE"}
		}
	}
	if !s.card.engine.Health.Healthy(acct.ID) {
		return false, []string{"PROVIDER_UNREACHABLE"}
	}
	return true, nil
}

// cardHasRecords keeps the card screens manageable when the module is withdrawn but history exists.
func (s *server) cardHasRecords(ctx context.Context, tenantID, siteID string) bool {
	if s.db == nil {
		return false
	}
	var n int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.payment_checkouts WHERE tenant_id=$1 AND site_id=$2`, tenantID, siteID).Scan(&n)
	return n > 0
}

// portalBase returns the return base portald supplied, restricted to an http(s) origin with no path.
func portalBase(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if !(strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")) || strings.ContainsAny(raw, " \"'<>") {
		return ""
	}
	rest := raw[strings.Index(raw, "//")+2:]
	if strings.Contains(rest, "/") || rest == "" {
		return ""
	}
	return raw
}

// startCardCheckout is called by commerceConfirm when a Card payment purchase was created. It starts the
// provider checkout and answers with where to send the client. Nothing is granted here.
func (s *server) startCardCheckout(w http.ResponseWriter, r *http.Request, purchaseID, settlementID, returnBase, name string) {
	base := portalBase(returnBase)
	if s.card == nil || s.card.engine == nil || base == "" {
		slog.Warn("card checkout: not available", "configured", s.card != nil && s.card.engine != nil, "base_ok", base != "")
		writeJSON(w, http.StatusConflict, map[string]any{"error": "unavailable", "purchase_id": purchaseID, "state": "failed"})
		return
	}
	ret := base + "/pay/return?p=" + purchaseID
	cancel := ret + "&cancelled=1"
	desc := "Internet access"
	if name != "" {
		desc = name
	}
	st, err := s.card.engine.StartCheckout(r.Context(), s.tenID, s.siteID, settlementID, desc, ret, cancel)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"purchase_id": purchaseID, "method": "ONLINE_PAYMENT",
			"awaiting_settlement": true, "state": "pending", "redirect_url": st.RedirectURL})
	case errors.Is(err, payment.ErrCheckoutAmbiguous):
		writeJSON(w, http.StatusAccepted, map[string]any{"purchase_id": purchaseID, "method": "ONLINE_PAYMENT",
			"awaiting_settlement": true, "state": "pending"})
	default:
		slog.Info("card checkout refused", "err", err)
		writeJSON(w, http.StatusConflict, map[string]any{"error": "unavailable", "purchase_id": purchaseID, "state": "failed"})
	}
}

type purchaseStatusReq struct {
	PurchaseID string `json:"purchase_id"`
	DeviceID   string `json:"device_id"`
}

// purchaseStatus answers where one purchase stands, for the portal's pending and return pages. It is bound to
// the device that made the purchase: another device learns nothing. For a card payment it asks the provider
// (rate limited); what the browser says plays no part.
func (s *server) purchaseStatus(w http.ResponseWriter, r *http.Request) {
	var req purchaseStatusReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PurchaseID == "" || req.DeviceID == "" {
		httpErr(w, http.StatusBadRequest, "unavailable")
		return
	}
	ctx := r.Context()
	var method, pstate, sstatus string
	var eid *string
	err := s.db.QueryRow(ctx, `
		SELECT se.method, pu.state, se.status, e.id::text
		  FROM iam_v2.purchases pu
		  JOIN iam_v2.auth_contexts ac ON ac.id = pu.auth_context_id
		  JOIN iam_v2.settlements se ON se.purchase_id = pu.id
		  LEFT JOIN iam_v2.entitlements e ON e.purchase_id = pu.id
		 WHERE pu.tenant_id=$1 AND pu.site_id=$2 AND pu.id=$3 AND ac.device_id=$4`,
		s.tenID, s.siteID, req.PurchaseID, req.DeviceID).Scan(&method, &pstate, &sstatus, &eid)
	if err != nil {
		httpErr(w, http.StatusNotFound, "unavailable")
		return
	}
	if method == "ONLINE_PAYMENT" && pstate == "AWAITING_SETTLEMENT" && s.card != nil && s.card.engine != nil {
		if txn, terr := s.card.engine.TransactionForPurchase(ctx, s.tenID, s.siteID, req.PurchaseID); terr == nil && s.statusAllowed(txn) {
			res, _ := s.card.engine.Reconcile(ctx, s.tenID, s.siteID, txn)
			if res.PurchaseState != "" {
				pstate, sstatus = res.PurchaseState, res.SettlementStatus
				if res.EntitlementID != "" {
					e := res.EntitlementID
					eid = &e
				}
			}
		}
	}
	out := map[string]any{"purchase_id": req.PurchaseID, "method": method, "state": purchaseView(pstate, sstatus)}
	if eid != nil {
		out["entitlement_id"] = *eid
	}
	writeJSON(w, http.StatusOK, out)
}

// purchaseView is the client-facing state vocabulary.
func purchaseView(purchaseState, settlementStatus string) string {
	switch purchaseState {
	case "GRANTED":
		return "granted"
	case "FAILED", "CANCELLED":
		return "failed"
	case "MANUAL_REVIEW":
		return "review"
	}
	if settlementStatus == "MANUAL_REVIEW" {
		return "review"
	}
	return "pending"
}

func (s *server) statusAllowed(txn string) bool {
	s.card.statusMu.Lock()
	defer s.card.statusMu.Unlock()
	if t, ok := s.card.lastStatus[txn]; ok && time.Since(t) < 4*time.Second {
		return false
	}
	s.card.lastStatus[txn] = time.Now()
	if len(s.card.lastStatus) > 5000 {
		s.card.lastStatus = map[string]time.Time{}
	}
	return true
}

// cardReconcileLoop resolves every open checkout, grants captured ones that were never granted (crash
// recovery), and sweeps recent captures for provider-reported refunds and chargebacks.
func (s *server) cardReconcileLoop(ctx context.Context) {
	if s.card == nil || s.card.engine == nil {
		return
	}
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	lastSweep := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if s.tenID == "" || s.siteID == "" {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		ids, err := s.card.engine.PendingTransactions(cctx, s.tenID, s.siteID, 25)
		if err == nil {
			for _, id := range ids {
				if _, rerr := s.card.engine.Reconcile(cctx, s.tenID, s.siteID, id); rerr != nil {
					slog.Info("card reconcile: not yet resolved", "err", rerr)
				}
			}
		}
		for _, id := range s.card.engine.CapturedStillUngranted(cctx, s.tenID, s.siteID) {
			_, _ = s.card.engine.Reconcile(cctx, s.tenID, s.siteID, id)
		}
		if time.Since(lastSweep) > 6*time.Hour {
			s.card.engine.SweepReversals(cctx, s.tenID, s.siteID, 60*24*time.Hour, 200)
			lastSweep = time.Now()
		}
		cancel()
	}
}

// paymentGardenDomains are the pre-sign-in domains card payment needs: the declared hosted-payment domains of
// the providers of this site's ACTIVE accounts, plus the site's bounded extra list. Only while card payment is
// licensed, enabled and deployed (readiness is deliberately ignored here so a flapping provider does not tear
// a client's payment page down mid-payment).
func (s *server) paymentGardenDomains(ctx context.Context) []string {
	if s.card == nil || s.db == nil {
		return nil
	}
	snap := s.moduleSnapshot(ctx)
	st := snap.Modules[lic.ModuleCardPayment]
	if !(st.Deployed && st.Licensed && st.Enabled) {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		d = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), "*.")
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	rows, err := s.db.Query(ctx, `SELECT DISTINCT provider FROM iam_v2.payment_provider_accounts
		WHERE tenant_id=$1 AND site_id=$2 AND status='ACTIVE'`, s.tenID, s.siteID)
	if err == nil {
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil {
				for k, ad := range s.card.adapters {
					if k == p || strings.HasPrefix(k, p+":") {
						for _, d := range ad.RequiredHostedDomains() {
							add(d)
						}
					}
				}
			}
		}
		rows.Close()
	}
	var extra []string
	_ = s.db.QueryRow(ctx, `SELECT domains FROM iam_v2.site_payment_domains WHERE tenant_id=$1 AND site_id=$2`,
		s.tenID, s.siteID).Scan(&extra)
	for _, d := range extra {
		add(d)
	}
	return out
}

// ---- administration (edged proxies these; admin socket only) ---------------------------------------------

func (s *server) paymentAdminRoutes(r chi.Router) {
	r.Get("/v1/payment/providers", s.paymentProviders)
	r.Get("/v1/payment/accounts", s.paymentAccounts)
	r.Post("/v1/payment/accounts", s.paymentAccountSave)
	r.Post("/v1/payment/accounts/{id}/test", s.paymentAccountTest)
	r.Put("/v1/payment/domains", s.paymentDomainsSave)
}

func (s *server) paymentProviders(w http.ResponseWriter, r *http.Request) {
	type prov struct {
		ID      string                  `json:"id"`
		Label   string                  `json:"label"`
		Regions []string                `json:"regions,omitempty"`
		Keys    []payment.CredentialKey `json:"credential_keys"`
		Domains []string                `json:"hosted_domains"`
		LiveOK  bool                    `json:"live_allowed"`
	}
	live := s.ceiling.Available(deployment.CapCardLive)
	out := []prov{}
	if s.card != nil {
		if a := s.card.adapters["stripe"]; a != nil {
			out = append(out, prov{ID: "stripe", Label: "Stripe", Keys: a.CredentialKeys(), Domains: a.RequiredHostedDomains(), LiveOK: live})
		}
		if a := s.card.adapters["paymob"]; a != nil {
			out = append(out, prov{ID: "paymob", Label: "Paymob", Regions: []string{"egypt", "ksa", "uae", "oman"},
				Keys: a.CredentialKeys(), Domains: a.RequiredHostedDomains(), LiveOK: live})
		}
	}
	unready := ""
	if s.card != nil {
		unready = s.card.unready
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out, "engine_unready": unready})
}

func (s *server) paymentAccounts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT a.id::text, a.provider, COALESCE(a.merchant_account_ref,''), COALESCE(a.display_name,''),
		       COALESCE(a.currency,''), a.mode, a.status, a.is_default, a.updated_at,
		       EXISTS (SELECT 1 FROM iam_v2.payment_provider_secret_generations g
		                WHERE g.account_id = a.id AND g.superseded_at IS NULL)
		  FROM iam_v2.payment_provider_accounts a
		 WHERE a.tenant_id=$1 AND a.site_id=$2 ORDER BY a.created_at`, s.tenID, s.siteID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "accounts unreadable")
		return
	}
	defer rows.Close()
	type acct struct {
		ID          string    `json:"id"`
		Provider    string    `json:"provider"`
		MerchantRef string    `json:"merchant_account_ref"`
		DisplayName string    `json:"display_name"`
		Currency    string    `json:"currency"`
		Mode        string    `json:"mode"`
		Status      string    `json:"status"`
		IsDefault   bool      `json:"is_default"`
		UpdatedAt   time.Time `json:"updated_at"`
		HasSecrets  bool      `json:"has_credentials"`
		SetKeys     []string  `json:"credential_keys_set"`
	}
	out := []acct{}
	for rows.Next() {
		var a acct
		if err := rows.Scan(&a.ID, &a.Provider, &a.MerchantRef, &a.DisplayName, &a.Currency, &a.Mode, &a.Status,
			&a.IsDefault, &a.UpdatedAt, &a.HasSecrets); err != nil {
			httpErr(w, http.StatusInternalServerError, "accounts unreadable")
			return
		}
		a.Currency = strings.TrimSpace(a.Currency)
		out = append(out, a)
	}
	rows.Close()
	// Which credential keys are set (never their values).
	if s.card != nil && s.card.key.Present() {
		for i := range out {
			if c, ok, err := payment.OpenStoredCredentials(r.Context(), s.db, s.card.key, s.tenID, s.siteID, out[i].ID); err == nil && ok {
				for k, v := range c {
					if strings.TrimSpace(v) != "" && k != "region" {
						out[i].SetKeys = append(out[i].SetKeys, k)
					}
				}
			}
		}
	}
	ready, reasons := s.cardReadiness(r.Context(), s.tenID, s.siteID)
	var extra []string
	_ = s.db.QueryRow(r.Context(), `SELECT domains FROM iam_v2.site_payment_domains WHERE tenant_id=$1 AND site_id=$2`,
		s.tenID, s.siteID).Scan(&extra)
	if extra == nil {
		extra = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out, "ready": ready, "readiness": reasons, "extra_domains": extra})
}

type paymentAccountReq struct {
	ID          string            `json:"id"`
	Provider    string            `json:"provider"`
	MerchantRef string            `json:"merchant_account_ref"`
	DisplayName string            `json:"display_name"`
	Currency    string            `json:"currency"`
	Mode        string            `json:"mode"`
	Status      string            `json:"status"`
	IsDefault   bool              `json:"is_default"`
	Credentials map[string]string `json:"credentials"`
	Operator    string            `json:"operator"`
	Reason      string            `json:"reason"`
}

func (s *server) paymentAccountSave(w http.ResponseWriter, r *http.Request) {
	var req paymentAccountReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if s.card == nil || !s.card.key.Present() {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "payment_key_missing",
			"message": "This appliance has no payment key; credentials cannot be stored."})
		return
	}
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
	req.Mode = strings.ToUpper(strings.TrimSpace(req.Mode))
	if req.Mode == "" {
		req.Mode = "TEST"
	}
	if req.Status == "" {
		req.Status = "ACTIVE"
	}
	if req.Provider != "stripe" && req.Provider != "paymob" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "provider_unsupported"})
		return
	}
	if req.Mode != "TEST" && req.Mode != "LIVE" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "mode_invalid"})
		return
	}
	// A LIVE account may be recorded but not activated here: live card traffic needs a separate authorisation.
	if req.Mode == "LIVE" && req.Status == "ACTIVE" && !s.ceiling.Available(deployment.CapCardLive) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "live_not_authorised",
			"message": "LIVE mode is not authorised on this appliance. Use TEST (sandbox) credentials."})
		return
	}
	if strings.TrimSpace(req.MerchantRef) == "" || len(req.Currency) != 3 || strings.TrimSpace(req.Operator) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid", "message": "merchant account, currency and operator are required"})
		return
	}
	creds := payment.Credentials{}
	for k, v := range req.Credentials {
		creds[k] = strings.TrimSpace(v)
	}
	if req.ID != "" {
		if stored, ok, err := payment.OpenStoredCredentials(r.Context(), s.db, s.card.key, s.tenID, s.siteID, req.ID); err == nil && ok {
			creds = payment.MergeCredentials(stored, creds)
		}
	}
	// Refuse credentials whose declared mode disagrees before storing anything (the adapter also refuses per call).
	ad := s.card.adapters[payment.AdapterKey(req.Provider, creds)]
	if ad == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "region_unsupported"})
		return
	}
	hasAny := false
	for _, v := range req.Credentials {
		if strings.TrimSpace(v) != "" {
			hasAny = true
		}
	}
	var toSeal payment.Credentials
	if hasAny {
		toSeal = creds
	}
	id, err := payment.SaveAccount(r.Context(), s.db, s.card.key, s.tenID, s.siteID, req.ID, req.Provider, strings.TrimSpace(req.MerchantRef),
		strings.TrimSpace(req.DisplayName), req.Currency, payment.Mode(req.Mode), req.Status, req.IsDefault, toSeal,
		strings.TrimSpace(req.Operator), strings.TrimSpace(req.Reason))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "not_saved", "message": sanitizeDBError(err)})
		return
	}
	s.kickGarden()
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *server) paymentAccountTest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if s.card == nil || !s.card.key.Present() {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "payment_key_missing"})
		return
	}
	var provider, merchant, mode string
	if err := s.db.QueryRow(r.Context(), `SELECT provider, COALESCE(merchant_account_ref,''), mode FROM iam_v2.payment_provider_accounts
		WHERE tenant_id=$1 AND site_id=$2 AND id=$3`, s.tenID, s.siteID, id).Scan(&provider, &merchant, &mode); err != nil {
		httpErr(w, http.StatusNotFound, "no such account")
		return
	}
	creds, ok, err := payment.OpenStoredCredentials(r.Context(), s.db, s.card.key, s.tenID, s.siteID, id)
	if err != nil || !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "credentials_missing"})
		return
	}
	ad := s.card.adapters[payment.AdapterKey(provider, creds)]
	if ad == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "provider_unsupported"})
		return
	}
	cctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	// Non-financial: an authenticated read (balance / token) that moves no money.
	if err := ad.TestConnection(cctx, payment.Mode(mode), merchant, creds); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "connection_failed", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type paymentDomainsReq struct {
	Domains  []string `json:"domains"`
	Operator string   `json:"operator"`
	Reason   string   `json:"reason"`
}

func (s *server) paymentDomainsSave(w http.ResponseWriter, r *http.Request) {
	var req paymentDomainsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	clean := []string{}
	for _, d := range req.Domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" {
			clean = append(clean, d)
		}
	}
	var v int64
	if err := s.db.QueryRow(r.Context(), `SELECT iam_v2.site_payment_domains_set($1,$2,$3,$4,NULLIF($5,''))`,
		s.tenID, s.siteID, clean, strings.TrimSpace(req.Operator), strings.TrimSpace(req.Reason)).Scan(&v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "not_saved", "message": sanitizeDBError(err)})
		return
	}
	s.kickGarden()
	writeJSON(w, http.StatusOK, map[string]any{"config_version": v, "domains": clean})
}

// sanitizeDBError returns the database's own message line (validation text), never connection details.
func sanitizeDBError(err error) string {
	m := err.Error()
	if i := strings.Index(m, "ERROR: "); i >= 0 {
		m = m[i+7:]
	}
	if i := strings.Index(m, " (SQLSTATE"); i >= 0 {
		m = m[:i]
	}
	return m
}

// kickGarden re-reconciles the walled garden soon, so an account or domain change reaches the portal quickly.
func (s *server) kickGarden() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if s.nft == nil {
			return
		}
		if _, err := s.reconcileWalledGarden(ctx); err != nil {
			slog.Warn("walled-garden: reconcile after payment change failed", "err", err)
		}
	}()
}
