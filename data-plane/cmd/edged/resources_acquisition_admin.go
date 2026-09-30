package main

// CARD PAYMENT AND ROOM CHARGE ADMINISTRATION (docs/architecture/ONEGATE_MODULES_AND_ACQUISITION.md).
//
// payment-providers: provider accounts, credentials, the extra hosted-payment domains and the card payment
// settings. scd owns the payment key, so everything that touches a credential is proxied to scd's admin
// socket and edged never sees a secret after the request that carried it. Credentials are write-only: the
// list reports which keys are set, never their values. Every write needs password step-up, names the
// operator, and is audited without the credential values.
//
// pms-financial-onboarding: approving a FIAS interface for room charge (posting target RN + G#, base
// currency and an attestation of what was observed). Site_admin only; the approval is a new immutable
// interface revision plus an append-only onboarding record, written by iam_v2.pms_interface_financial_onboard.

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *server) paymentProvidersRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/providers", func(w http.ResponseWriter, r *http.Request) {
		s.scd.proxy(w, r, http.MethodGet, "/v1/payment/providers", nil)
	})
	r.Get("/accounts", func(w http.ResponseWriter, r *http.Request) {
		s.scd.proxy(w, r, http.MethodGet, "/v1/payment/accounts", nil)
	})
	r.Post("/accounts", s.savePaymentAccount)
	r.Post("/accounts/{id}/test", s.testPaymentAccount)
	r.Put("/domains", s.savePaymentDomains)
	r.Get("/settings", s.getCardSettings)
	r.Put("/settings", s.putCardSettings)
	r.Get("/changes", s.paymentChanges)
	return r
}

// paymentStepUp checks the password and returns the operator label the change is recorded under.
func (s *server) paymentStepUp(w http.ResponseWriter, r *http.Request, password string) (string, bool) {
	if !s.reauth(r, password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return "", false
	}
	actor := protectionActor(sessFrom(r.Context()))
	if actor == "" {
		jsonErr(w, http.StatusForbidden, "forbidden", "the change could not be attributed to an operator")
		return "", false
	}
	return actor, true
}

func (s *server) savePaymentAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID          string            `json:"id"`
		Provider    string            `json:"provider"`
		MerchantRef string            `json:"merchant_account_ref"`
		DisplayName string            `json:"display_name"`
		Currency    string            `json:"currency"`
		Mode        string            `json:"mode"`
		Status      string            `json:"status"`
		IsDefault   bool              `json:"is_default"`
		Credentials map[string]string `json:"credentials"`
		Reason      string            `json:"reason"`
		Password    string            `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if len(in.Credentials) > 16 || len(strings.TrimSpace(in.Reason)) > 500 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "too many credential fields or reason too long")
		return
	}
	actor, ok := s.paymentStepUp(w, r, in.Password)
	if !ok {
		return
	}
	body := map[string]any{
		"id": in.ID, "provider": in.Provider, "merchant_account_ref": in.MerchantRef, "display_name": in.DisplayName,
		"currency": in.Currency, "mode": in.Mode, "status": in.Status, "is_default": in.IsDefault,
		"credentials": in.Credentials, "operator": actor, "reason": in.Reason,
	}
	st, raw, err := s.scd.call(r.Context(), http.MethodPost, "/v1/payment/accounts", body)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", "the payment service could not be reached")
		return
	}
	if st < 300 {
		// The audit names which credential keys were supplied, never a value.
		keys := []string{}
		for k, v := range in.Credentials {
			if strings.TrimSpace(v) != "" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		s.audit(r, "payment_account.save", "payment_provider_account", in.ID, map[string]any{
			"provider": in.Provider, "merchant_account_ref": in.MerchantRef, "currency": in.Currency,
			"mode": in.Mode, "status": in.Status, "is_default": in.IsDefault, "credential_keys_supplied": keys,
			"reason": strings.TrimSpace(in.Reason),
		})
		s.invalidateModules()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(raw)
}

func (s *server) testPaymentAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "invalid account id")
		return
	}
	s.audit(r, "payment_account.test", "payment_provider_account", id, nil)
	s.scd.proxy(w, r, http.MethodPost, "/v1/payment/accounts/"+id+"/test", nil)
}

func (s *server) savePaymentDomains(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Domains  []string `json:"domains"`
		Reason   string   `json:"reason"`
		Password string   `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil || len(in.Domains) > 50 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	actor, ok := s.paymentStepUp(w, r, in.Password)
	if !ok {
		return
	}
	st, raw, err := s.scd.call(r.Context(), http.MethodPut, "/v1/payment/domains",
		map[string]any{"domains": in.Domains, "operator": actor, "reason": in.Reason})
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", "the payment service could not be reached")
		return
	}
	if st < 300 {
		s.audit(r, "payment_domains.update", "site_payment_domains", s.siteID, map[string]any{
			"domains": in.Domains, "reason": strings.TrimSpace(in.Reason)})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(raw)
}

type cardSettings struct {
	CheckoutExpiryMinutes int        `json:"checkout_expiry_minutes"`
	ReconcileGraceMinutes int        `json:"reconcile_grace_minutes"`
	ConfigVersion         int64      `json:"config_version"`
	UpdatedAt             *time.Time `json:"updated_at,omitempty"`
	IsDefault             bool       `json:"is_default"`
}

func (s *server) readCardSettings(r *http.Request) (cardSettings, error) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var c cardSettings
	err := s.db.QueryRow(ctx, `SELECT checkout_expiry_minutes, reconcile_grace_minutes, config_version, updated_at, is_default
		  FROM iam_v2.card_payment_settings_get($1::uuid,$2::uuid)`, s.tenantID, s.siteID).
		Scan(&c.CheckoutExpiryMinutes, &c.ReconcileGraceMinutes, &c.ConfigVersion, &c.UpdatedAt, &c.IsDefault)
	return c, err
}

func (s *server) getCardSettings(w http.ResponseWriter, r *http.Request) {
	c, err := s.readCardSettings(r)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "settings_unreadable", "card payment settings could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": c,
		"bounds": map[string]any{
			"checkout_expiry_minutes": map[string]int{"min": 30, "max": 240, "default": 30},
			"reconcile_grace_minutes": map[string]int{"min": 15, "max": 1440, "default": 60},
		},
	})
}

func (s *server) putCardSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CheckoutExpiryMinutes int    `json:"checkout_expiry_minutes"`
		ReconcileGraceMinutes int    `json:"reconcile_grace_minutes"`
		Reason                string `json:"reason"`
		Password              string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil || len(strings.TrimSpace(in.Reason)) > 500 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if in.CheckoutExpiryMinutes < 30 || in.CheckoutExpiryMinutes > 240 {
		jsonErr(w, http.StatusBadRequest, "out_of_range", "Checkout expiry must be between 30 and 240 minutes.")
		return
	}
	if in.ReconcileGraceMinutes < 15 || in.ReconcileGraceMinutes > 1440 {
		jsonErr(w, http.StatusBadRequest, "out_of_range", "Reconciliation grace must be between 15 and 1440 minutes.")
		return
	}
	actor, ok := s.paymentStepUp(w, r, in.Password)
	if !ok {
		return
	}
	before, _ := s.readCardSettings(r)
	ctx, cancel := dbCtx(r)
	defer cancel()
	var v int64
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.card_payment_settings_set($1::uuid,$2::uuid,$3,$4,$5,NULLIF($6,''))`,
		s.tenantID, s.siteID, in.CheckoutExpiryMinutes, in.ReconcileGraceMinutes, actor, strings.TrimSpace(in.Reason)).Scan(&v); err != nil {
		jsonErr(w, http.StatusBadRequest, "not_saved", dbMessage(err))
		return
	}
	s.audit(r, "card_payment_settings.update", "card_payment_settings", s.siteID, map[string]any{
		"previous":       map[string]int{"checkout_expiry_minutes": before.CheckoutExpiryMinutes, "reconcile_grace_minutes": before.ReconcileGraceMinutes},
		"new":            map[string]int{"checkout_expiry_minutes": in.CheckoutExpiryMinutes, "reconcile_grace_minutes": in.ReconcileGraceMinutes},
		"config_version": v, "reason": strings.TrimSpace(in.Reason),
	})
	s.getCardSettings(w, r)
}

// paymentChanges is the change history of accounts, extra domains and card settings, newest first.
func (s *server) paymentChanges(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT 'account' AS kind, changed_at, changed_by, COALESCE(change_reason,''), change_kind AS detail
		  FROM iam_v2.payment_provider_account_changes WHERE tenant_id=$1::uuid AND site_id=$2::uuid
		UNION ALL
		SELECT 'domains', changed_at, changed_by, COALESCE(change_reason,''), array_to_string(new_domains, ', ')
		  FROM iam_v2.site_payment_domain_changes WHERE tenant_id=$1::uuid AND site_id=$2::uuid
		UNION ALL
		SELECT 'settings', changed_at, changed_by, COALESCE(change_reason,''),
		       'checkout expiry ' || new_checkout_expiry_minutes || ' min, grace ' || new_reconcile_grace_minutes || ' min'
		  FROM iam_v2.site_card_payment_setting_changes WHERE tenant_id=$1::uuid AND site_id=$2::uuid
		 ORDER BY 2 DESC LIMIT 200`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "changes_unreadable", "the change history could not be read")
		return
	}
	defer rows.Close()
	type change struct {
		Kind      string    `json:"kind"`
		ChangedAt time.Time `json:"changed_at"`
		ChangedBy string    `json:"changed_by"`
		Reason    string    `json:"reason"`
		Detail    string    `json:"detail"`
	}
	out := []change{}
	for rows.Next() {
		var c change
		if err := rows.Scan(&c.Kind, &c.ChangedAt, &c.ChangedBy, &c.Reason, &c.Detail); err != nil {
			jsonErr(w, http.StatusInternalServerError, "changes_unreadable", "the change history could not be read")
			return
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": out})
}

// dbMessage returns the database's validation message without connection details.
func dbMessage(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Message
	}
	return "the change was refused"
}

// ---- PMS financial onboarding (room charge) ----------------------------------------------------------------

func (s *server) pmsFinancialOnboardingRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listFinancialOnboarding)
	// Approving an interface for room charge is a site_admin decision whatever the role matrix grants.
	r.With(s.requireRole("pms-financial-onboarding", permWrite), requireSiteAdmin).Post("/{id}", s.approveFinancialOnboarding)
	// Vendor-confirmed PA meanings, per interface (Phase-0 Amendment A1): until a code is confirmed here it is
	// UNKNOWN. Append-only; a site administrator records a CONFIRM or a WITHDRAW with the vendor evidence.
	r.With(s.requireRole("pms-financial-onboarding", permWrite), requireSiteAdmin).Post("/{id}/answer-confirmations", s.recordAnswerConfirmation)
	// A stay's ADMIN_BLOCK: the only posting block an operator may set or clear. PMS_NO_POST and
	// PMS_DATA_SUSPECT are cleared by PMS data only, and POSTING_UNRESOLVED by its charge's review.
	r.With(s.requireRole("pms-financial-onboarding", permWrite), requireSiteAdmin).Post("/stays/{stay}/admin-block", s.stayAdminBlock)
	// The financial mirror maximum age (D48): an operational setting per interface, site_admin with step-up.
	r.With(s.requireRole("pms-financial-onboarding", permWrite), requireSiteAdmin).Post("/{id}/financial-mirror-max-age", s.setFinancialMirrorMaxAge)
	return r
}

// requireSiteAdmin refuses anyone who is not a site administrator.
func requireSiteAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := sessFrom(r.Context())
		if sess == nil || !hasRole(sess.Roles, "site_admin") {
			jsonErr(w, http.StatusForbidden, "forbidden", "only a site administrator can approve room charge on an interface")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hasRole(roles []string, want string) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

type financialOnboardingRow struct {
	InterfaceID       string     `json:"pms_interface_id"`
	DisplayLabel      string     `json:"display_label"`
	ConnectorKind     string     `json:"connector_kind"`
	LifecycleState    string     `json:"lifecycle_state"`
	CurrentRevisionID string     `json:"current_revision_id"`
	PostingTarget     string     `json:"posting_target_model"`
	Currency          *string    `json:"financial_base_currency"`
	CurrencyExponent  *int       `json:"financial_base_currency_exponent"`
	Ready             bool       `json:"ready"`
	Reason            *string    `json:"reason"`
	ApprovedAt        *time.Time `json:"approved_at,omitempty"`
	ApprovedBy        *string    `json:"approved_by,omitempty"`
	Attestation       *string    `json:"attestation,omitempty"`
	// AnswerMeanings lists, per PA status that can be confirmed, whether the vendor has confirmed it means
	// "definitely not posted" on this interface, and its fixed stay effect. An unconfirmed status is UNKNOWN.
	AnswerMeanings []answerMeaning `json:"answer_meanings"`
	// FinancialMirror: how long the mirror stays good enough for room charge after a successful complete resync
	// (D48), whether that is the default, and when the mirror was last proven.
	FinancialMirror financialMirror `json:"financial_mirror"`
}

type financialMirror struct {
	MaxAgeSeconds      int        `json:"max_age_seconds"`
	IsDefault          bool       `json:"is_default"`
	LastCompleteSyncAt *time.Time `json:"last_complete_sync_at"`
}

// answerMeaning is one PA status's confirmation state on one interface.
type answerMeaning struct {
	ASStatus   string     `json:"as_status"`
	Effect     string     `json:"effect"`
	Confirmed  bool       `json:"confirmed"`
	Evidence   *string    `json:"evidence,omitempty"`
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
	RecordedBy *string    `json:"recorded_by,omitempty"` // operator e-mail
}

// answerEffects is the contract's fixed mapping (section 9a rule 9) for a vendor-confirmed status.
var answerEffects = []struct{ code, effect string }{
	{"NP", "NO_POST_BLOCK"}, {"NG", "DATA_SUSPECT_BLOCK"}, {"NR", "DATA_SUSPECT_BLOCK"},
	{"NA", "NO_STAY_BLOCK"}, {"RY", "NO_STAY_BLOCK"},
}

func (s *server) listFinancialOnboarding(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT i.id::text, i.display_label, i.connector_kind, i.lifecycle_state, COALESCE(i.current_revision_id::text,''),
		       COALESCE(rv.posting_target_model,'UNSET'), rv.financial_base_currency::text,
		       rv.financial_base_currency_exponent::int, rd.ready, rd.reason,
		       o.approved_at, o.approved_by::text, o.attestation,
		       iam_v2.p4_financial_mirror_max_age_seconds(i.tenant_id, i.site_id, i.id),
		       NOT EXISTS (SELECT 1 FROM iam_v2.pms_interface_financial_settings fs WHERE fs.pms_interface_id = i.id),
		       rt.last_complete_sync_at
		  FROM iam_v2.pms_interfaces i
		  LEFT JOIN iam_v2.pms_interface_runtime rt ON rt.pms_interface_id = i.id
		  LEFT JOIN iam_v2.pms_interface_revisions rv ON rv.id = i.current_revision_id
		  LEFT JOIN iam_v2.pms_financial_onboardings o ON o.revision_id = i.current_revision_id
		  CROSS JOIN LATERAL iam_v2.pms_interface_financially_ready(i.tenant_id, i.site_id, i.id) rd
		 WHERE i.tenant_id=$1::uuid AND i.site_id=$2::uuid
		 ORDER BY i.display_label`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "onboarding_unreadable", "financial onboarding could not be read")
		return
	}
	defer rows.Close()
	out := []financialOnboardingRow{}
	for rows.Next() {
		var e financialOnboardingRow
		if err := rows.Scan(&e.InterfaceID, &e.DisplayLabel, &e.ConnectorKind, &e.LifecycleState, &e.CurrentRevisionID,
			&e.PostingTarget, &e.Currency, &e.CurrencyExponent, &e.Ready, &e.Reason,
			&e.ApprovedAt, &e.ApprovedBy, &e.Attestation,
			&e.FinancialMirror.MaxAgeSeconds, &e.FinancialMirror.IsDefault, &e.FinancialMirror.LastCompleteSyncAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "onboarding_unreadable", "financial onboarding could not be read")
			return
		}
		out = append(out, e)
	}
	rows.Close()
	for k := range out {
		// Answer meanings exist only where room charge can post: a FIAS interface.
		out[k].AnswerMeanings = []answerMeaning{}
		if out[k].ConnectorKind == "protel-fias" {
			out[k].AnswerMeanings = s.answerMeanings(ctx, out[k].InterfaceID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"interfaces":            out,
		"posting_target_models": []string{"RESERVATION"},
	})
}

// answerMeanings reads the latest confirmation per status for one interface.
func (s *server) answerMeanings(ctx context.Context, iface string) []answerMeaning {
	latest := map[string]answerMeaning{}
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT ON (c.as_status) c.as_status, c.action, c.evidence, c.recorded_at, o.email
		  FROM iam_v2.pms_answer_confirmations c
		  LEFT JOIN public.operators o ON o.id = c.recorded_by
		 WHERE c.tenant_id=$1::uuid AND c.site_id=$2::uuid AND c.pms_interface_id=$3::uuid
		 ORDER BY c.as_status, c.recorded_at DESC, c.id DESC`, s.tenantID, s.siteID, iface)
	if err == nil {
		for rows.Next() {
			var code, action, evidence string
			var at time.Time
			var by *string
			if rows.Scan(&code, &action, &evidence, &at, &by) == nil {
				ev, t := evidence, at
				latest[code] = answerMeaning{ASStatus: code, Confirmed: action == "CONFIRM", Evidence: &ev, RecordedAt: &t, RecordedBy: by}
			}
		}
		rows.Close()
	}
	out := make([]answerMeaning, 0, len(answerEffects))
	for _, a := range answerEffects {
		m := latest[a.code]
		m.ASStatus, m.Effect = a.code, a.effect
		out = append(out, m)
	}
	return out
}

func (s *server) recordAnswerConfirmation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		ASStatus string `json:"as_status"`
		Action   string `json:"action"`
		Evidence string `json:"evidence"`
		Reason   string `json:"reason"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil || !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "interface, status, action, evidence and reason are required")
		return
	}
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}
	sess := sessFrom(r.Context())
	if sess == nil || !uuidRe.MatchString(sess.OperatorID) {
		jsonErr(w, http.StatusForbidden, "forbidden", "the confirmation could not be attributed to an operator")
		return
	}
	code, action := strings.ToUpper(strings.TrimSpace(in.ASStatus)), strings.ToUpper(strings.TrimSpace(in.Action))
	ctx, cancel := dbCtx(r)
	defer cancel()
	var rid string
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.pms_answer_confirmation_record($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8::uuid)::text`,
		s.tenantID, s.siteID, id, code, action, strings.TrimSpace(in.Evidence), strings.TrimSpace(in.Reason),
		sess.OperatorID).Scan(&rid); err != nil {
		jsonErr(w, http.StatusBadRequest, "not_recorded", dbMessage(err))
		return
	}
	s.audit(r, "pms_interface.answer_confirmation", "pms_interface", id, map[string]any{
		"as_status": code, "action": action, "reason": strings.TrimSpace(in.Reason),
	})
	writeJSON(w, http.StatusOK, map[string]any{"id": rid, "answer_meanings": s.answerMeanings(ctx, id)})
}

func (s *server) stayAdminBlock(w http.ResponseWriter, r *http.Request) {
	stay := chi.URLParam(r, "stay")
	var in struct {
		Action   string `json:"action"`
		Reason   string `json:"reason"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil || !uuidRe.MatchString(stay) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "stay, action and reason are required")
		return
	}
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}
	sess := sessFrom(r.Context())
	if sess == nil || !uuidRe.MatchString(sess.OperatorID) {
		jsonErr(w, http.StatusForbidden, "forbidden", "the change could not be attributed to an operator")
		return
	}
	action := strings.ToUpper(strings.TrimSpace(in.Action))
	ctx, cancel := dbCtx(r)
	defer cancel()
	var res string
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.p4_admin_posting_block($1::uuid,$2::uuid,$3::uuid,$4,$5,$6::uuid)`,
		s.tenantID, s.siteID, stay, action, strings.TrimSpace(in.Reason), sess.OperatorID).Scan(&res); err != nil {
		jsonErr(w, http.StatusBadRequest, "not_changed", dbMessage(err))
		return
	}
	s.audit(r, "stay.room_charge_admin_block", "stay", stay, map[string]any{
		"action": action, "result": res, "reason": strings.TrimSpace(in.Reason),
	})
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

// setFinancialMirrorMaxAge changes how long room charge trusts the mirror after a successful complete resync.
func (s *server) setFinancialMirrorMaxAge(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		MaxAgeSeconds int    `json:"max_age_seconds"`
		Reason        string `json:"reason"`
		Password      string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil || !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "interface, maximum age and reason are required")
		return
	}
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}
	sess := sessFrom(r.Context())
	if sess == nil || !uuidRe.MatchString(sess.OperatorID) {
		jsonErr(w, http.StatusForbidden, "forbidden", "the change could not be attributed to an operator")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var old int
	_ = s.db.QueryRow(ctx, `SELECT iam_v2.p4_financial_mirror_max_age_seconds($1::uuid,$2::uuid,$3::uuid)`,
		s.tenantID, s.siteID, id).Scan(&old)
	var got int
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.p4_set_financial_mirror_max_age($1::uuid,$2::uuid,$3::uuid,$4,$5,$6::uuid)`,
		s.tenantID, s.siteID, id, in.MaxAgeSeconds, strings.TrimSpace(in.Reason), sess.OperatorID).Scan(&got); err != nil {
		jsonErr(w, http.StatusBadRequest, "not_changed", dbMessage(err))
		return
	}
	s.audit(r, "pms_interface.financial_mirror_max_age", "pms_interface", id, map[string]any{
		"old_seconds": old, "new_seconds": got, "reason": strings.TrimSpace(in.Reason),
	})
	writeJSON(w, http.StatusOK, map[string]any{"max_age_seconds": got})
}

func (s *server) approveFinancialOnboarding(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		ExpectedRevisionID string `json:"expected_revision_id"`
		PostingTarget      string `json:"posting_target_model"`
		Currency           string `json:"currency"`
		Attestation        string `json:"attestation"`
		Reason             string `json:"reason"`
		Password           string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil || !uuidRe.MatchString(id) || !uuidRe.MatchString(in.ExpectedRevisionID) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "interface and expected revision are required")
		return
	}
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}
	sess := sessFrom(r.Context())
	if sess == nil || !uuidRe.MatchString(sess.OperatorID) {
		jsonErr(w, http.StatusForbidden, "forbidden", "the approval could not be attributed to an operator")
		return
	}
	cur := strings.ToUpper(strings.TrimSpace(in.Currency))
	ctx, cancel := dbCtx(r)
	defer cancel()
	var rev string
	err := s.db.QueryRow(ctx, `SELECT iam_v2.pms_interface_financial_onboard($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,2::smallint,$7,$8,$9::uuid)::text`,
		s.tenantID, s.siteID, id, in.ExpectedRevisionID, strings.TrimSpace(in.PostingTarget), cur,
		strings.TrimSpace(in.Attestation), strings.TrimSpace(in.Reason), sess.OperatorID).Scan(&rev)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "40001" {
			jsonErr(w, http.StatusConflict, "revision_conflict", "The interface changed while this form was open. Reload and try again.")
			return
		}
		jsonErr(w, http.StatusBadRequest, "not_approved", dbMessage(err))
		return
	}
	s.audit(r, "pms_interface.financial_onboard", "pms_interface", id, map[string]any{
		"new_revision_id": rev, "previous_revision_id": in.ExpectedRevisionID,
		"posting_target_model": strings.TrimSpace(in.PostingTarget), "currency": cur,
		"reason": strings.TrimSpace(in.Reason),
	})
	s.invalidateModules()
	writeJSON(w, http.StatusOK, map[string]any{"revision_id": rev})
}
