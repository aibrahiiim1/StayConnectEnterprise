package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// CloudRoutes is the whole operator API, mounted at /cloud/v1 behind the sc_session cookie
// (docs/CENTRAL_CONTROL_PLANE.md §6). Nothing else is mounted.
//
// Authorization happens in two places, both server-side:
//   - route level: RequirePermission for platform-only writes, RequireReauth for step-up (SU) writes;
//   - handler level: readScope pins every read to the caller's customer unless the caller holds fleet.view,
//     and customer-level writes (sites, users) check canManageForCustomer against the target customer.
//
// off may be nil (no vendor signing key): the offline routes then answer 503.
func (b *Base) CloudRoutes(off *OfflineBase) http.Handler {
	r := chi.NewRouter()
	su := RequireReauth(b.Redis)
	perm := auth.RequirePermission

	r.Get("/overview", b.overview)

	// Customers.
	r.Get("/customers", b.listCustomers)
	r.With(perm(auth.PermCustomersManage)).Post("/customers", b.postCustomer)
	r.Get("/customers/{id}", b.getCustomer)
	r.With(perm(auth.PermCustomersManage)).Patch("/customers/{id}", b.patchCustomer)
	r.With(perm(auth.PermCustomersManage)).Post("/customers/{id}/archive", b.setCustomerStatus("archived"))
	r.With(perm(auth.PermCustomersManage)).Post("/customers/{id}/restore", b.setCustomerStatus("active"))
	r.With(perm(auth.PermCustomersManage), su).Delete("/customers/{id}", b.deleteCustomer)

	// Sites (customer-level writes: platform or the customer's own admins).
	r.Get("/customers/{id}/sites", b.listSites)
	r.Post("/customers/{id}/sites", b.postSite)
	r.Patch("/sites/{id}", b.patchSite)
	r.Post("/sites/{id}/archive", b.setSiteStatus("archived"))
	r.Post("/sites/{id}/restore", b.setSiteStatus("active"))
	r.With(su).Delete("/sites/{id}", b.deleteSite)

	// Customer users (customer-level writes).
	r.Get("/customers/{id}/users", b.listCustomerUsers)
	// Every change to who can sign in to Central, or with what, re-confirms the operator's password.
	r.With(su).Post("/customers/{id}/users", b.postCustomerUser)
	r.With(su).Patch("/customers/{id}/users/{uid}", b.patchCustomerUser)
	r.With(su).Delete("/customers/{id}/users/{uid}", b.deleteCustomerUser)

	// Appliances.
	r.Get("/appliances", b.listAppliances)
	r.Get("/appliances/{id}", b.getAppliance)
	am := r.With(perm(auth.PermAppliancesManage), su)
	am.Post("/appliances/{id}/activate", b.activate)
	am.Post("/appliances/{id}/move", b.move)
	am.Post("/appliances/{id}/retire", b.retire)
	am.Post("/appliances/{id}/replace", b.replace)
	am.Post("/appliances/{id}/rebind-wan-mac", b.rebindWANMAC)
	am.Post("/appliances/{id}/reissue-certificate", b.reissueCertificate)
	am.Delete("/appliances/{id}", b.deleteAppliance)

	// Licences.
	r.Get("/licenses", b.listLicenses)
	lm := r.With(perm(auth.PermLicensesManage), su)
	lm.Post("/appliances/{id}/license", b.setLicense)
	lm.Post("/licenses/{id}/suspend", b.licenseAction("suspend"))
	lm.Post("/licenses/{id}/resume", b.licenseAction("resume"))
	lm.Post("/licenses/{id}/revoke", b.licenseAction("revoke"))

	// Offline files.
	if off != nil {
		r.With(perm(auth.PermAppliancesManage)).Post("/offline-activation/requests", off.importRequest)
		am.Post("/appliances/{id}/offline-activation-package", off.activationPackage)
		lm.Post("/appliances/{id}/offline-license", off.offlineLicense)
	} else {
		na := unavailable("offline_unavailable", "offline files need the vendor signing key, which is not configured")
		r.With(perm(auth.PermAppliancesManage)).Post("/offline-activation/requests", na)
		am.Post("/appliances/{id}/offline-activation-package", na)
		lm.Post("/appliances/{id}/offline-license", na)
	}

	// System.
	r.Get("/security-alerts", b.listSecurityAlerts)
	r.With(perm(auth.PermSecurityManage)).Patch("/security-alerts/{id}", b.patchSecurityAlert)
	r.With(perm(auth.PermFleetView)).Get("/trust", b.trust)
	r.Get("/audit", b.listAudit)
	r.With(perm(auth.PermFleetView)).Get("/team", b.listTeam)
	tm := r.With(perm(auth.PermTeamManage), su)
	tm.Post("/team", b.postTeam)
	tm.Patch("/team/{id}", b.patchTeam)
	tm.Delete("/team/{id}", b.deleteTeam)
	tm.Post("/team/{id}/password", b.teamPassword)
	r.With(perm(auth.PermFleetView)).Get("/backup-health", b.BackupHealthHandler)

	return r
}
