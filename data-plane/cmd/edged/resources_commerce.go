package main

// Hotel-Admin commercial-packages resource, mounted only when the Phase-2 admin surface is on (see main.go);
// otherwise the routes are absent and no Phase-2 SQL is issued. Every mutation is audited. The operator is
// trusted, so validation reasons are returned verbatim.

import (
	"encoding/json"
	"errors"
	"fmt"
	lic "github.com/stayconnect/enterprise/license"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

func (s *server) commercialPackagesRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listCommercialPackages)
	r.Post("/", s.publishCommercialPackage)
	// Service plans: static paths are matched before the /{id} param routes.
	r.Get("/plans", s.listServicePlans)
	r.Post("/plans", s.publishServicePlan)
	r.Get("/plans/{id}/revisions", s.listServicePlanRevisions)
	r.Get("/plans/{id}/deletability", s.getServicePlanDeletability)
	// Permanent deletion of a never-used plan: step-up + reason.
	r.Delete("/plans/{id}", s.deleteServicePlan)
	r.Get("/grace", s.getGraceConfig)
	r.Put("/grace", s.setGraceConfig)
	// Read-only inspection (guest-PII-free).
	r.Get("/guest-activity", s.listGuestActivity)
	r.Get("/activity", s.listPackageActivity)
	r.Get("/quotes", s.listCommerceQuotes)
	r.Get("/purchases", s.listCommercePurchases)
	r.Get("/{id}/revisions", s.listCommercialPackageRevisions)
	r.Get("/{id}/current", s.getCommercialPackageCurrent)
	r.Get("/{id}/deletability", s.getCommercialPackageDeletability)
	r.Delete("/{id}", s.deleteCommercialPackage)
	r.Post("/{id}/active", s.setCommercialPackageActive)
	return r
}

func (s *server) listServicePlans(w http.ResponseWriter, r *http.Request) {
	plans, disabled, err := s.commerce.ListPlans(r.Context(), s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	if disabled {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
		return
	}
	writeList(w, plans)
}

type publishPlanReq struct {
	Code                        string `json:"code"`
	Name                        string `json:"name"`
	DownKbps                    *int   `json:"down_kbps"`
	UpKbps                      *int   `json:"up_kbps"`
	MaxConcurrentDevices        int    `json:"max_concurrent_devices"`
	DeviceLimitPolicy           string `json:"device_limit_policy"`
	IdleTimeoutSeconds          *int   `json:"idle_timeout_seconds"`
	MaxContinuousSessionSeconds *int   `json:"max_continuous_session_seconds"`
	TimeQuotaSeconds            *int64 `json:"time_quota_seconds"`
	DataQuotaBytes              *int64 `json:"data_quota_bytes"`
	TimeAccountingMode          string `json:"time_accounting_mode"`
	SpeedAllocation             string `json:"speed_allocation"`
	// CreateOnly is sent by "Add plan": refuse (409 code_exists) rather than revise an existing plan.
	CreateOnly bool `json:"create_only"`
	// Password is the step-up for revising an existing plan.
	Password string `json:"password"`
}

func (s *server) publishServicePlan(w http.ResponseWriter, r *http.Request) {
	var in publishPlanReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	// Revising a plan changes every package on it, so it needs step-up. It keeps the existing time accounting
	// unless the body names one (a missing field must not flip AGGREGATE_ONLINE_TIME to VALIDITY_WINDOW).
	if !in.CreateOnly {
		plans, disabled, perr := s.commerce.ListPlans(r.Context(), s.tenantID, s.siteID)
		if perr != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "the existing plans could not be read")
			return
		}
		if !disabled {
			for _, p := range plans {
				if p.Code != strings.TrimSpace(in.Code) {
					continue
				}
				if strings.TrimSpace(in.TimeAccountingMode) == "" && p.TimeAccountingMode != nil {
					in.TimeAccountingMode = *p.TimeAccountingMode
				}
				if !s.reauth(r, in.Password) {
					jsonErr(w, http.StatusUnauthorized, "reauth_required",
						"Confirm your password: changing a service plan changes what its packages give")
					return
				}
				break
			}
		}
	}
	res, err := s.commerce.PublishPlanRevision(r.Context(), iamv2.PlanPublishSpec{
		TenantID: s.tenantID, SiteID: s.siteID, PlanCode: in.Code, Name: in.Name,
		DownKbps: in.DownKbps, UpKbps: in.UpKbps, MaxConcurrentDevices: in.MaxConcurrentDevices,
		DeviceLimitPolicy: in.DeviceLimitPolicy, IdleTimeoutSeconds: in.IdleTimeoutSeconds,
		MaxContinuousSessionSeconds: in.MaxContinuousSessionSeconds, TimeQuotaSeconds: in.TimeQuotaSeconds,
		DataQuotaBytes: in.DataQuotaBytes, TimeAccountingMode: in.TimeAccountingMode,
		SpeedAllocation: in.SpeedAllocation, CreateOnly: in.CreateOnly,
	})
	if errors.Is(err, iamv2.ErrCodeExists) {
		jsonErr(w, http.StatusConflict, "code_exists",
			fmt.Sprintf("A service plan with the code %q already exists. Open it and choose Edit to change its settings.", in.Code))
		return
	}
	if err != nil {
		// A domain refusal (e.g. a reserved system code) is the operator's to fix, not a 500.
		var de *iamv2.Error
		if errors.As(err, &de) && de.Code == iamv2.ErrInvalidInput {
			jsonErr(w, http.StatusBadRequest, "bad_request", de.Msg)
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "publish failed")
		return
	}
	if res.Disabled {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
		return
	}
	if res.Reason != "published" {
		jsonErr(w, http.StatusBadRequest, "validation", res.Reason)
		return
	}
	s.audit(r, "service_plan.published", "service_plan", res.PackageID, map[string]any{
		"revision_id": res.CurrentRevisionID, "code": in.Code,
	})
	writeJSON(w, http.StatusOK, map[string]any{"plan_id": res.PackageID, "current_revision_id": res.CurrentRevisionID})
}

func (s *server) listServicePlanRevisions(w http.ResponseWriter, r *http.Request) {
	revs, disabled, err := s.commerce.PlanRevisions(r.Context(), s.tenantID, s.siteID, chi.URLParam(r, "id"))
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	if disabled {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
		return
	}
	writeList(w, revs)
}

func (s *server) listCommercialPackageRevisions(w http.ResponseWriter, r *http.Request) {
	revs, disabled, err := s.commerce.PackageRevisions(r.Context(), s.tenantID, s.siteID, chi.URLParam(r, "id"))
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	if disabled {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
		return
	}
	writeList(w, revs)
}

func (s *server) getGraceConfig(w http.ResponseWriter, r *http.Request) {
	gc, disabled, err := s.commerce.GetGrace(r.Context(), s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "load failed")
		return
	}
	if disabled {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
		return
	}
	writeJSON(w, http.StatusOK, gc)
}

// setGraceReq carries policy only: the system derives the grace package, since the checkout validator needs it
// to match the policy exactly. expected_version (the config_version last read) is mandatory so concurrent edits
// conflict instead of overwriting.
type setGraceReq struct {
	Config          map[string]any `json:"config"`
	ExpectedVersion *int           `json:"expected_version"`
	ReasonCode      string         `json:"reason_code"`
	// Decoded only so a caller still sending the retired field gets a clear refusal (the decoder rejects unknown fields).
	RetiredPackageRevisionID string `json:"grace_package_revision_id"`
	// Step-up: required here as on /checkout-grace, since both routes write the same site policy.
	Password string `json:"password"`
}

// graceReasonCode matches the audited boundary's shape; checked here so a bad code is a 400, not a 409 from the database.
var graceReasonCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

func (s *server) setGraceConfig(w http.ResponseWriter, r *http.Request) {
	var in setGraceReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if !s.commerceCfg.AdminOn() {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
		return
	}
	if in.RetiredPackageRevisionID != "" {
		jsonErr(w, http.StatusBadRequest, "validation",
			"grace_package_revision_id is no longer accepted: the system derives the grace package from the "+
				"published policy, because the checkout validator requires the two to match exactly")
		return
	}
	if in.ExpectedVersion == nil {
		jsonErr(w, http.StatusBadRequest, "bad_request",
			"expected_version is required: publish against the config_version you last read")
		return
	}
	// Step-up: publishing changes what every departing guest receives.
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation required")
		return
	}
	// The actor comes from the session, never the body, or the audited record would be worthless.
	sess := sessFrom(r.Context())
	if sess == nil || sess.OperatorID == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthenticated", "an operator session is required")
		return
	}
	reason := strings.TrimSpace(in.ReasonCode)
	if reason == "" {
		// A bounded default: a publication without a recorded reason is unattributable.
		reason = "HOTEL_ADMIN_UPDATE"
	}
	if !graceReasonCode.MatchString(reason) {
		jsonErr(w, http.StatusBadRequest, "validation",
			"reason_code must be a bounded machine code matching ^[A-Z][A-Z0-9_]{0,63}$, not free text")
		return
	}
	pol, perr := gracePolicyFromConfig(in.Config)
	if perr != nil {
		jsonErr(w, http.StatusBadRequest, "validation", perr.Error())
		return
	}
	repo, ok := s.commerceRepo.(iamv2.GracePublishRepository)
	if !ok || repo == nil {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	version, pkgRev, err := iamv2.PublishSystemGracePolicy(ctx, repo, iamv2.GracePublishRequest{
		TenantID: s.tenantID, SiteID: s.siteID, Policy: pol,
		ActorOperatorID: sess.OperatorID, ReasonCode: reason, ExpectedVersion: *in.ExpectedVersion,
	})
	if err != nil {
		// Boundary refusals (stale version, invalid policy, rejected derivation) are the operator's to act on.
		var de *iamv2.Error
		if errors.As(err, &de) && de.Code == iamv2.ErrInvalidInput {
			jsonErr(w, http.StatusBadRequest, "validation", de.Msg)
			return
		}
		jsonErr(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	// Audit the version and derived package, not the raw config blob.
	s.audit(r, "commercial_grace.published", "commercial_grace", pkgRev,
		map[string]any{"config_version": version, "reason_code": reason})
	writeJSON(w, http.StatusOK, map[string]any{
		"grace_package_revision_id": pkgRev,
		"config_version":            version,
		"authority":                 "iam_v2",
	})
}

// gracePolicyFromConfig maps typed config keys (those designated by grace_config_no_dup_policy_keys) onto the policy.
func gracePolicyFromConfig(cfg map[string]any) (iamv2.SystemGracePolicy, error) {
	num := func(k string) (int64, bool) {
		v, ok := cfg[k]
		if !ok {
			return 0, false
		}
		switch n := v.(type) {
		case json.Number:
			i, err := n.Int64()
			return i, err == nil
		case float64:
			return int64(n), n == float64(int64(n))
		case int:
			return int64(n), true
		case int64:
			return n, true
		}
		return 0, false
	}
	var p iamv2.SystemGracePolicy
	get := func(k string) (int, error) {
		n, ok := num(k)
		if !ok {
			return 0, fmt.Errorf("%s is required and must be an integer", k)
		}
		return int(n), nil
	}
	var err error
	if p.DurationSeconds, err = get("grace_duration_seconds"); err != nil {
		return p, err
	}
	if p.DownKbps, err = get("grace_down_kbps"); err != nil {
		return p, err
	}
	if p.UpKbps, err = get("grace_up_kbps"); err != nil {
		return p, err
	}
	q, ok := num("grace_data_quota_bytes")
	if !ok {
		return p, fmt.Errorf("grace_data_quota_bytes is required and must be an integer")
	}
	p.DataQuotaBytes = q
	if p.DeviceLimit, err = get("grace_device_limit"); err != nil {
		return p, err
	}
	pol, _ := cfg["grace_device_limit_policy"].(string)
	if pol == "" {
		// The only policy enforcement implements; defaulted.
		pol = "REJECT_NEW_DEVICE"
	}
	p.DeviceLimitPolicy = pol
	if p.EligibilityWindowSeconds, err = get("eligibility_window_seconds"); err != nil {
		return p, err
	}
	return p, nil
}

func (s *server) listCommerceQuotes(w http.ResponseWriter, r *http.Request) {
	q, disabled, err := s.commerce.Quotes(r.Context(), s.tenantID, s.siteID, 100)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	if disabled {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
		return
	}
	writeList(w, q)
}

func (s *server) listCommercePurchases(w http.ResponseWriter, r *http.Request) {
	p, disabled, err := s.commerce.Purchases(r.Context(), s.tenantID, s.siteID, 100)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	if disabled {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
		return
	}
	writeList(w, p)
}

func (s *server) listCommercialPackages(w http.ResponseWriter, r *http.Request) {
	pkgs, disabled, err := s.commerce.ListPackages(r.Context(), s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	if disabled {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
		return
	}
	writeList(w, pkgs)
}

type commerceRuleDTO struct {
	Type  string         `json:"type"`
	Value map[string]any `json:"value"`
}
type commerceTierDTO struct {
	Order int            `json:"order"`
	Grant map[string]any `json:"grant"`
}
type publishPackageReq struct {
	Code                  string            `json:"code"`
	ServicePlanRevisionID string            `json:"service_plan_revision_id"`
	Display               map[string]any    `json:"display"`
	DurationPolicy        map[string]any    `json:"duration_policy"`
	EligibilityRules      []commerceRuleDTO `json:"eligibility_rules"`
	GrantTiers            []commerceTierDTO `json:"grant_tiers"`
	VisibleFrom           *time.Time        `json:"visible_from"`
	VisibleUntil          *time.Time        `json:"visible_until"`
	// Absent DataAllocationPolicy means FIXED.
	DataAllocationPolicy map[string]any `json:"data_allocation_policy"`
	// CreateOnly (Add package) answers 409 code_exists instead of silently revising an existing code.
	CreateOnly bool `json:"create_only"`
	// Price and acquisition are pointers because absent differs from zero: a new package naming none is Free, but a
	// revision of an existing package must state every one (commercial_fields_required), or it would be republished free.
	PriceMinor         *int64                     `json:"price_minor"`
	Currency           *string                    `json:"currency"`
	CurrencyExponent   *int                       `json:"currency_exponent"`
	AcquisitionMethods *[]string                  `json:"acquisition_methods"`
	RoomChargeMappings *[]iamv2.RoomChargeMapping `json:"room_charge_mappings"`
	// Fields the editor does not show: absent means keep the current revision's value.
	PackageType         *string         `json:"package_type"`
	PlanOverrides       *map[string]any `json:"plan_overrides"`
	Renewable           *bool           `json:"renewable"`
	MaxPurchasesPerStay optionalInt     `json:"max_purchases_per_stay"`
	// Step-up is required when a revision changes cost, payment method or service-plan revision (separation of duties).
	Password string `json:"password"`
}

// optionalInt tells "absent" from "null" from a number: absent keeps, null clears, a number sets.
type optionalInt struct {
	Set   bool
	Value *int
}

func (o *optionalInt) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	o.Value = &n
	return nil
}

// acquisitionModuleRefusal names the module a publication needs and lacks. Only licensed-and-enabled is required
// to publish; readiness is gated again when the offer is made.
func (s *server) acquisitionModuleRefusal(r *http.Request, spec iamv2.PackagePublishSpec) (string, bool) {
	need := map[string]bool{}
	if spec.PriceMinor > 0 {
		need[lic.ModulePaidAccess] = true
	}
	for _, m := range spec.AcquisitionMethods {
		switch m {
		case iamv2.AcquireCard:
			need[lic.ModuleCardPayment] = true
		case iamv2.AcquireRoomCharge:
			need[lic.ModuleRoomCharge] = true
		}
	}
	if len(need) == 0 {
		return "", true
	}
	rep, ok := s.moduleReport(r.Context())
	if !ok {
		return "", false
	}
	for _, id := range []string{lic.ModulePaidAccess, lic.ModuleCardPayment, lic.ModuleRoomCharge} {
		if !need[id] {
			continue
		}
		st, found := rep.Modules[id]
		if !found || !st.Licensed || !st.Enabled {
			label := id
			if found && st.Label != "" {
				label = st.Label
			}
			return fmt.Sprintf("%s is not licensed and switched on at this site (System > Modules).", label), true
		}
	}
	return "", true
}

func (s *server) publishCommercialPackage(w http.ResponseWriter, r *http.Request) {
	var in publishPackageReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	spec := iamv2.PackagePublishSpec{
		TenantID: s.tenantID, SiteID: s.siteID,
		PackageCode: in.Code, ServicePlanRevisionID: in.ServicePlanRevisionID,
		Display: in.Display, DurationPolicy: in.DurationPolicy,
		VisibleFrom: in.VisibleFrom, VisibleUntil: in.VisibleUntil,
		DataAllocationPolicy: in.DataAllocationPolicy,
		CreateOnly:           in.CreateOnly,
	}
	if in.PriceMinor != nil {
		spec.PriceMinor = *in.PriceMinor
	}
	if in.Currency != nil {
		spec.Currency = *in.Currency
	}
	if in.CurrencyExponent != nil {
		spec.CurrencyExponent = *in.CurrencyExponent
	}
	if in.AcquisitionMethods != nil {
		spec.AcquisitionMethods = *in.AcquisitionMethods
	}
	if in.RoomChargeMappings != nil {
		spec.RoomChargeMappings = *in.RoomChargeMappings
	}
	if in.PackageType != nil {
		spec.PackageType = *in.PackageType
	}
	if in.PlanOverrides != nil {
		spec.PlanOverrides = *in.PlanOverrides
	}
	if in.Renewable != nil {
		spec.Renewable = *in.Renewable
	}
	if in.MaxPurchasesPerStay.Set {
		spec.MaxPurchasesPerStay = in.MaxPurchasesPerStay.Value
	}

	// Revising an existing package needs explicit commercial terms, and a step-up when terms or plan change.
	if !in.CreateOnly {
		cur, exists, ok := s.currentPackageByCode(w, r, in.Code)
		if !ok {
			return
		}
		if exists {
			if missing := missingCommercialFields(in); len(missing) > 0 {
				jsonErr(w, http.StatusBadRequest, "commercial_fields_required",
					"A change to an existing package must state its price and how clients get it; missing: "+
						strings.Join(missing, ", ")+". Nothing was changed.")
				return
			}
			if in.PackageType == nil {
				spec.PackageType = cur.PackageType
			}
			if in.PlanOverrides == nil {
				spec.PlanOverrides = cur.PlanOverrides
			}
			if in.Renewable == nil {
				spec.Renewable = cur.Renewable
			}
			if !in.MaxPurchasesPerStay.Set {
				spec.MaxPurchasesPerStay = cur.MaxPurchasesPerStay
			}
			spec.PriorDurationPolicy = cur.DurationPolicy
			if commercialTermsChanged(cur, spec) && !s.reauth(r, in.Password) {
				jsonErr(w, http.StatusUnauthorized, "reauth_required",
					"Confirm your password: this change alters the package's price, how it is paid for or its service plan")
				return
			}
		}
	}
	if msg, ok := s.acquisitionModuleRefusal(r, spec); !ok {
		jsonErr(w, http.StatusServiceUnavailable, "modules_unavailable", "the module state could not be read")
		return
	} else if msg != "" {
		jsonErr(w, http.StatusConflict, "module_not_enabled", msg)
		return
	}
	for _, ru := range in.EligibilityRules {
		spec.EligibilityRules = append(spec.EligibilityRules, iamv2.EligibilityRule{Type: ru.Type, Value: ru.Value})
	}
	for _, ti := range in.GrantTiers {
		spec.GrantTiers = append(spec.GrantTiers, iamv2.GrantTier{Order: ti.Order, Value: ti.Grant})
	}
	// A package may have one grant step with no overrides (see iamv2.GrantTiersEnforceableRefusal).
	if msg := iamv2.GrantTiersEnforceableRefusal(spec.GrantTiers); msg != "" {
		jsonErr(w, http.StatusBadRequest, "validation", msg)
		return
	}
	res, err := s.commerce.PublishRevision(r.Context(), spec)
	if errors.Is(err, iamv2.ErrCodeExists) {
		jsonErr(w, http.StatusConflict, "code_exists",
			fmt.Sprintf("A package with the code %q already exists. Open it and choose Edit to change it.", in.Code))
		return
	}
	if err != nil {
		// A domain refusal (e.g. a reserved system code) is the operator's to fix, not a 500.
		var de *iamv2.Error
		if errors.As(err, &de) && de.Code == iamv2.ErrInvalidInput {
			jsonErr(w, http.StatusBadRequest, "bad_request", de.Msg)
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "publish failed")
		return
	}
	if res.Disabled {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
		return
	}
	if res.Reason != "published" {
		jsonErr(w, http.StatusBadRequest, "validation", res.Reason)
		return
	}
	s.audit(r, "commercial_package.published", "commercial_package", res.PackageID, map[string]any{
		"revision_id": res.CurrentRevisionID, "code": in.Code,
		"price_minor": spec.PriceMinor, "currency": spec.Currency, "acquisition_methods": spec.AcquisitionMethods,
		"service_plan_revision_id": spec.ServicePlanRevisionID,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"package_id":          res.PackageID,
		"current_revision_id": res.CurrentRevisionID,
	})
}

// currentPackageByCode reads the current configuration of the package owning code. ok=false means the error response was already written.
func (s *server) currentPackageByCode(w http.ResponseWriter, r *http.Request, code string) (iamv2.PackageCurrent, bool, bool) {
	pkgs, disabled, err := s.commerce.ListPackages(r.Context(), s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the existing packages could not be read")
		return iamv2.PackageCurrent{}, false, false
	}
	if disabled {
		return iamv2.PackageCurrent{}, false, true
	}
	for _, p := range pkgs {
		if p.Code != strings.TrimSpace(code) {
			continue
		}
		cur, _, gerr := s.commerce.GetPackageCurrent(r.Context(), s.tenantID, s.siteID, p.PackageID)
		if gerr != nil {
			jsonErr(w, http.StatusConflict, "conditions_unreadable",
				"This package's current configuration cannot be read, so it cannot be changed safely here.")
			return iamv2.PackageCurrent{}, false, false
		}
		return cur, true, true
	}
	return iamv2.PackageCurrent{}, false, true
}

func missingCommercialFields(in publishPackageReq) []string {
	var out []string
	if in.PriceMinor == nil {
		out = append(out, "price_minor")
	}
	if in.Currency == nil {
		out = append(out, "currency")
	}
	if in.CurrencyExponent == nil {
		out = append(out, "currency_exponent")
	}
	if in.AcquisitionMethods == nil {
		out = append(out, "acquisition_methods")
	}
	if in.RoomChargeMappings == nil {
		out = append(out, "room_charge_mappings")
	}
	return out
}

func commercialTermsChanged(cur iamv2.PackageCurrent, spec iamv2.PackagePublishSpec) bool {
	if cur.PriceMinor != spec.PriceMinor || !strings.EqualFold(strings.TrimSpace(cur.Currency), strings.TrimSpace(spec.Currency)) {
		return true
	}
	if cur.CurrencyExponent != nil && *cur.CurrencyExponent != spec.CurrencyExponent {
		return true
	}
	if cur.ServicePlanRevisionID != spec.ServicePlanRevisionID {
		return true
	}
	set := func(ms []string) map[string]bool {
		out := map[string]bool{}
		for _, m := range ms {
			out[strings.ToUpper(strings.TrimSpace(m))] = true
		}
		return out
	}
	a, b := set(cur.SettlementMethods), set(spec.AcquisitionMethods)
	if len(a) != len(b) {
		return true
	}
	for m := range a {
		if !b[m] {
			return true
		}
	}
	return false
}

type setActiveReq struct {
	Active   bool   `json:"active"`
	Password string `json:"password"`
	Reason   string `json:"reason"`
}

func (s *server) setCommercialPackageActive(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in setActiveReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	// Deactivation withdraws a live package: require a reason and step-up.
	if !in.Active {
		if strings.TrimSpace(in.Reason) == "" {
			jsonErr(w, http.StatusBadRequest, "reason_required", "a reason is required to deactivate a package")
			return
		}
		if !s.reauth(r, in.Password) {
			s.reauthRefused(w, r, "password confirmation required")
			return
		}
	} else if !s.reauth(r, in.Password) {
		// Reactivation re-offers the package at its price and plan, so it needs step-up too.
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}
	res, err := s.commerce.SetActive(r.Context(), s.tenantID, s.siteID, id, in.Active)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	if res.Disabled {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
		return
	}
	action := "commercial_package.deactivated"
	if in.Active {
		action = "commercial_package.activated"
	}
	s.audit(r, action, "commercial_package", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"package_id": id, "active": in.Active})
}

func (s *server) getCommercialPackageCurrent(w http.ResponseWriter, r *http.Request) {
	cur, disabled, err := s.commerce.GetPackageCurrent(r.Context(), s.tenantID, s.siteID, chi.URLParam(r, "id"))
	if err != nil {
		// Unreadable conditions must not read as "not found": saving republishes the whole package, so a form
		// loaded without eligibility rules and speed steps would silently drop them.
		if errors.Is(err, iamv2.ErrPackageConditionsUnreadable) {
			jsonErr(w, http.StatusConflict, "conditions_unreadable",
				"This package cannot be edited here yet: its eligibility conditions and speed steps are not "+
					"readable by this service, and saving without them would change who the package is "+
					"offered to. Ask your StayConnect administrator to grant read access.")
			return
		}
		jsonErr(w, http.StatusNotFound, "not_found", "no current configuration for this package")
		return
	}
	if disabled {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
		return
	}
	writeJSON(w, http.StatusOK, cur)
}

// guestActivityRow is what happened between one guest and one internet package.
type guestActivityRow struct {
	QuoteID    string     `json:"quote_id,omitempty"`
	PurchaseID string     `json:"purchase_id,omitempty"`
	RevisionID string     `json:"package_revision_id,omitempty"`
	OfferedAt  *time.Time `json:"offered_at,omitempty"`
	TakenAt    *time.Time `json:"taken_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`

	// A room is always shown with its PMS interface; voucher and account guests have no room.
	Room         string `json:"room,omitempty"`
	PMSInterface string `json:"pms_interface,omitempty"`
	Reservation  string `json:"reservation,omitempty"`
	StayID       string `json:"stay_id,omitempty"`
	SignInMethod string `json:"sign_in_method,omitempty"`

	Package     string `json:"package"`
	PackageType string `json:"package_type,omitempty"`
	PriceMinor  int64  `json:"price_minor"`
	Currency    string `json:"currency,omitempty"`

	// Outcome is TAKEN, NOT_TAKEN, EXPIRED, or the purchase state.
	Outcome string `json:"outcome"`
	Trigger string `json:"trigger,omitempty"`

	ServicePlan   string `json:"service_plan,omitempty"`
	QuotaBytes    *int64 `json:"quota_bytes,omitempty"`
	EntitlementID string `json:"entitlement_id,omitempty"`
}

func (s *server) listGuestActivity(w http.ResponseWriter, r *http.Request) {
	limit := queryLimit(r, 100, 500)

	// Starts from the offer so never-taken offers are included. The stay resolves through the purchase when there
	// is one, else through the auth context the quote was issued under (the only recorded quote-to-stay link).
	// The auth_contexts join may read only the five columns svc_edged is granted (id, tenant_id, site_id, stay_id,
	// pms_interface_id): wider access would expose credential linkage, so adding a column is a privilege change.
	// Nothing is inferred: no recorded link means a blank room, and a room always travels with its pms_interface.
	rows, err := s.db.Query(r.Context(), `
		SELECT q.id::text,
		       COALESCE(p.id::text, ''),
		       COALESCE(q.package_revision_id::text, ''),
		       q.expires_at, q.consumed_at,
		       COALESCE(st.normalized_room_number, ''),
		       COALESCE(pi.display_label, ''),
		       COALESCE(st.external_reservation_id, ''),
		       COALESCE(st.id::text, ''),
		       COALESCE(sess.credential_method, ''),
		       COALESCE(NULLIF(ipr.display->>'name', ''), 'Internet package'),
		       COALESCE(ipr.package_type, ''),
		       COALESCE(q.price_minor, 0),
		       COALESCE(NULLIF(q.currency, ''), NULLIF(ipr.currency, ''), ''),
		       COALESCE(p.state, ''),
		       COALESCE(p.trigger, ''),
		       COALESCE(spr.name, ''),
		       e.data_quota_bytes,
		       COALESCE(e.id::text, '')
		  FROM iam_v2.offer_quotes q
		  LEFT JOIN iam_v2.purchases p ON p.offer_quote_id = q.id
		  -- Tenant and site are matched as well as the id: the join asserts isolation rather than assuming it.
		  LEFT JOIN iam_v2.auth_contexts ac
		         ON ac.id = q.auth_context_id AND ac.tenant_id = q.tenant_id AND ac.site_id = q.site_id
		  LEFT JOIN iam_v2.stays st ON st.id = COALESCE(p.stay_id, ac.stay_id)
		  LEFT JOIN iam_v2.pms_interfaces pi
		         ON pi.id = COALESCE(st.pms_interface_id, ac.pms_interface_id, q.pms_interface_id)
		  LEFT JOIN iam_v2.internet_package_revisions ipr ON ipr.id = q.package_revision_id
		  LEFT JOIN iam_v2.entitlements e ON e.purchase_id = p.id
		  LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
		  -- The earliest session on the entitlement: how the guest actually signed in to use what they took.
		  LEFT JOIN LATERAL (
		      SELECT s2.credential_method FROM iam_v2.sessions s2
		       WHERE s2.entitlement_id = e.id ORDER BY s2.started LIMIT 1
		  ) sess ON true
		 WHERE q.tenant_id = $1 AND q.site_id = $2
		 ORDER BY COALESCE(q.consumed_at, q.expires_at) DESC NULLS LAST
		 LIMIT `+strconv.Itoa(limit), s.tenantID, s.siteID)
	if err != nil {
		slog.Error("guest activity read failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "client activity could not be read")
		return
	}
	defer rows.Close()

	out := []guestActivityRow{}
	now := time.Now().UTC()
	for rows.Next() {
		var x guestActivityRow
		var state string
		if err := rows.Scan(&x.QuoteID, &x.PurchaseID, &x.RevisionID, &x.ExpiresAt, &x.TakenAt,
			&x.Room, &x.PMSInterface, &x.Reservation, &x.StayID, &x.SignInMethod,
			&x.Package, &x.PackageType, &x.PriceMinor, &x.Currency,
			&state, &x.Trigger, &x.ServicePlan, &x.QuotaBytes, &x.EntitlementID); err != nil {
			slog.Error("guest activity scan failed", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal", "client activity could not be read")
			return
		}
		// OfferedAt stays empty: offer_quotes records expiry and consumption, not when the offer was made.
		switch {
		case state == "GRANTED":
			x.Outcome = "TAKEN"
		case state != "":
			x.Outcome = state
		case x.TakenAt != nil:
			x.Outcome = "TAKEN"
		case x.ExpiresAt != nil && x.ExpiresAt.Before(now):
			x.Outcome = "EXPIRED"
		default:
			x.Outcome = "NOT_TAKEN"
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		slog.Error("guest activity read failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "client activity could not be read")
		return
	}
	writeList(w, out)
}
