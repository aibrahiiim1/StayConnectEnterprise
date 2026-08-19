package main

// Phase 2 (DARK) Hotel-Admin commercial-packages resource: revisioned, free-only, non-PMS package
// management. Mounted ONLY when the Phase-2 admin surface is ON (see main.go); while dark the routes are
// absent and the admin engine holds a nil repository, so zero Phase-2 SQL is issued. RBAC is enforced by
// mountResource ("commercial-packages" in the role matrix) and every mutation is audited. Unlike the
// guest portal, the admin operator is trusted, so validation reasons are returned verbatim.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

func (s *server) commercialPackagesRoutes() http.Handler {
	r := chi.NewRouter()
	// packages
	r.Get("/", s.listCommercialPackages)
	r.Post("/", s.publishCommercialPackage)
	// service plans (static paths matched before the /{id} param routes)
	r.Get("/plans", s.listServicePlans)
	r.Post("/plans", s.publishServicePlan)
	r.Get("/plans/{id}/revisions", s.listServicePlanRevisions)
	// site checkout-grace configuration
	r.Get("/grace", s.getGraceConfig)
	r.Put("/grace", s.setGraceConfig)
	// read-only inspection (guest-PII-free)
	r.Get("/quotes", s.listCommerceQuotes)
	r.Get("/purchases", s.listCommercePurchases)
	// package-scoped
	r.Get("/{id}/revisions", s.listCommercialPackageRevisions)
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
}

func (s *server) publishServicePlan(w http.ResponseWriter, r *http.Request) {
	var in publishPlanReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	res, err := s.commerce.PublishPlanRevision(r.Context(), iamv2.PlanPublishSpec{
		TenantID: s.tenantID, SiteID: s.siteID, PlanCode: in.Code, Name: in.Name,
		DownKbps: in.DownKbps, UpKbps: in.UpKbps, MaxConcurrentDevices: in.MaxConcurrentDevices,
		DeviceLimitPolicy: in.DeviceLimitPolicy, IdleTimeoutSeconds: in.IdleTimeoutSeconds,
		MaxContinuousSessionSeconds: in.MaxContinuousSessionSeconds, TimeQuotaSeconds: in.TimeQuotaSeconds,
		DataQuotaBytes: in.DataQuotaBytes, TimeAccountingMode: in.TimeAccountingMode,
	})
	if err != nil {
		// A domain REFUSAL is not an internal error. Publishing onto a reserved system code is a policy
		// decision the operator can act on ("that code is not yours"), and reporting it as 500 "internal"
		// tells them the server broke instead. Only genuinely unexpected failures stay 500.
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

// setGraceReq is POLICY ONLY (D32).
//
// grace_package_revision_id is deliberately absent. Under D32 the operator does not choose a package: the
// system derives the one that exactly expresses this policy, because the checkout validator demands exact
// equality between the two and a package the operator picked by hand can only accidentally satisfy it.
// expected_version is the config_version the operator last read -- mandatory, so two operators editing at
// once produce one winner and one explicit conflict rather than a silent overwrite.
type setGraceReq struct {
	Config          map[string]any `json:"config"`
	ExpectedVersion *int           `json:"expected_version"`
	ReasonCode      string         `json:"reason_code"`
	// Accepted by the decoder ONLY so that a caller still sending the retired field gets told what changed.
	// The decoder rejects unknown fields, so without this line an old client's request fails as "bad body" --
	// a caller reading that would look for a malformed JSON problem that does not exist. It is never used.
	RetiredPackageRevisionID string `json:"grace_package_revision_id"`
}

// graceReasonCode bounds the reason code to ^[A-Z][A-Z0-9_]{0,63}$ -- the same shape the audited boundary
// enforces. Checked here as well so a bad code is a 400 naming the rule, rather than a 409 raised from inside
// a database function, which reads like a concurrency conflict and is not one.
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
	// The actor is taken from the authenticated SESSION, never from the body. The audited boundary records
	// who changed what every departing guest receives, and a caller-supplied actor would make that record
	// worth nothing.
	sess := sessFrom(r.Context())
	if sess == nil || sess.OperatorID == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthenticated", "an operator session is required")
		return
	}
	reason := strings.TrimSpace(in.ReasonCode)
	if reason == "" {
		// A bounded default rather than a free-text one: the boundary requires ^[A-Z][A-Z0-9_]{0,63}$, and a
		// publication with no recorded reason is an unattributable change.
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
		// Every refusal the boundary raises is actionable by the operator -- a stale version, an invalid
		// policy, a derivation the checkout validator rejected -- so it is reported as such rather than as an
		// internal error.
		var de *iamv2.Error
		if errors.As(err, &de) && de.Code == iamv2.ErrInvalidInput {
			jsonErr(w, http.StatusBadRequest, "validation", de.Msg)
			return
		}
		jsonErr(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	// The audit payload records the version and the derived package, never the operator's raw config blob.
	s.audit(r, "commercial_grace.published", "commercial_grace", pkgRev,
		map[string]any{"config_version": version, "reason_code": reason})
	writeJSON(w, http.StatusOK, map[string]any{
		"grace_package_revision_id": pkgRev,
		"config_version":            version,
		"authority":                 "iam_v2",
	})
}

// gracePolicyFromConfig maps the operator's typed config keys onto the policy the system derives from.
//
// The key names are the ones the schema itself designates as typed (grace_config_no_dup_policy_keys), so the
// operator-facing vocabulary and the columns cannot drift apart.
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
		// The only policy the enforcement path implements; defaulted so an operator supplying the other five
		// is not tripped by a field with exactly one legal value.
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
	}
	for _, ru := range in.EligibilityRules {
		spec.EligibilityRules = append(spec.EligibilityRules, iamv2.EligibilityRule{Type: ru.Type, Value: ru.Value})
	}
	for _, ti := range in.GrantTiers {
		spec.GrantTiers = append(spec.GrantTiers, iamv2.GrantTier{Order: ti.Order, Value: ti.Grant})
	}
	res, err := s.commerce.PublishRevision(r.Context(), spec)
	if err != nil {
		// A domain REFUSAL is not an internal error. Publishing onto a reserved system code is a policy
		// decision the operator can act on ("that code is not yours"), and reporting it as 500 "internal"
		// tells them the server broke instead. Only genuinely unexpected failures stay 500.
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
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"package_id":          res.PackageID,
		"current_revision_id": res.CurrentRevisionID,
	})
}

type setActiveReq struct {
	Active   bool   `json:"active"`
	Password string `json:"password"` // step-up, required for destructive deactivation
	Reason   string `json:"reason"`
}

func (s *server) setCommercialPackageActive(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in setActiveReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	// Deactivation is destructive (it withdraws a live package from guests): require a reason + password
	// step-up, mirroring the destructive service-restart / cert-rotate policy.
	if !in.Active {
		if strings.TrimSpace(in.Reason) == "" {
			jsonErr(w, http.StatusBadRequest, "reason_required", "a reason is required to deactivate a package")
			return
		}
		if !s.reauth(r, in.Password) {
			jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
			return
		}
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
