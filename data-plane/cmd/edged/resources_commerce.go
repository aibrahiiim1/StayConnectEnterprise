package main

// Phase 2 (DARK) Hotel-Admin commercial-packages resource: revisioned, free-only, non-PMS package
// management. Mounted ONLY when the Phase-2 admin surface is ON (see main.go); while dark the routes are
// absent and the admin engine holds a nil repository, so zero Phase-2 SQL is issued. RBAC is enforced by
// mountResource ("commercial-packages" in the role matrix) and every mutation is audited. Unlike the
// guest portal, the admin operator is trusted, so validation reasons are returned verbatim.

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

func (s *server) commercialPackagesRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listCommercialPackages)
	r.Post("/", s.publishCommercialPackage)
	r.Post("/{id}/active", s.setCommercialPackageActive)
	return r
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
	Active bool `json:"active"`
}

func (s *server) setCommercialPackageActive(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in setActiveReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
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
