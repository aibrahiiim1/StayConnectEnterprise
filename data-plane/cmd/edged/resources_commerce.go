package main

// Phase 2 (DARK) Hotel-Admin commercial-packages resource: revisioned, free-only, non-PMS package
// management. Mounted ONLY when the Phase-2 admin surface is ON (see main.go); while dark the routes are
// absent and the admin engine holds a nil repository, so zero Phase-2 SQL is issued. RBAC is enforced by
// mountResource ("commercial-packages" in the role matrix) and every mutation is audited. Unlike the
// guest portal, the admin operator is trusted, so validation reasons are returned verbatim.

import (
	"net/http"
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

type setGraceReq struct {
	GracePackageRevisionID string         `json:"grace_package_revision_id"`
	Config                 map[string]any `json:"config"`
}

func (s *server) setGraceConfig(w http.ResponseWriter, r *http.Request) {
	var in setGraceReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	res, err := s.commerce.SetGrace(r.Context(), s.tenantID, s.siteID, in.GracePackageRevisionID, in.Config)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	if res.Disabled {
		jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled", "commercial packages are not enabled")
		return
	}
	if res.Reason != "ok" {
		jsonErr(w, http.StatusBadRequest, "validation", res.Reason)
		return
	}
	s.audit(r, "commercial_grace.configured", "commercial_grace", in.GracePackageRevisionID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"grace_package_revision_id": in.GracePackageRevisionID})
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
