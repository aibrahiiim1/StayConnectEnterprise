package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/portaltls"
)

// Handlers are thin: internal/portaltls decides, and says why it refuses (portaltls.Refusal).

// portalTLSFail writes a refusal as the standard envelope; anything else is a generic internal error whose
// detail stays in the log (it may name a database object).
func portalTLSFail(w http.ResponseWriter, r *http.Request, err error) {
	var ref *portaltls.Refusal
	if errors.As(err, &ref) {
		Fail(w, r, ref.Status, ref.Code, ref.Message, ref.Extra)
		return
	}
	slog.Error("portal tls request failed", "path", r.URL.Path, "err", err)
	Fail(w, r, http.StatusInternalServerError, CodeInternal, "the request could not be completed")
}

// PortalTLSBase serves the three appliance messages: hostnames, portal address, CSRs and certificate status; no guest data.
type PortalTLSBase struct {
	Svc *portaltls.Service
}

// Sync: POST /v1/appliance/portal-tls/sync {portal_ip, custom_domain|null}.
func (p *PortalTLSBase) Sync(w http.ResponseWriter, r *http.Request) {
	ident := auth.ApplianceFromContext(r.Context())
	if ident == nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "no appliance context")
		return
	}
	var in portaltls.SyncRequest
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	out, err := p.Svc.Sync(r.Context(), ident.ApplianceID, in)
	if err != nil {
		portalTLSFail(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// Order: POST /v1/appliance/portal-tls/orders {hostname, csr_pem} -> 202 {order_id, status}.
func (p *PortalTLSBase) Order(w http.ResponseWriter, r *http.Request) {
	ident := auth.ApplianceFromContext(r.Context())
	if ident == nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "no appliance context")
		return
	}
	var in portaltls.OrderRequest
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	out, err := p.Svc.CreateOrder(r.Context(), ident.ApplianceID, in)
	if err != nil {
		portalTLSFail(w, r, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, out)
}

// GetOrder: GET /v1/appliance/portal-tls/orders/{id} — the appliance's own orders only.
func (p *PortalTLSBase) GetOrder(w http.ResponseWriter, r *http.Request) {
	ident := auth.ApplianceFromContext(r.Context())
	if ident == nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "no appliance context")
		return
	}
	out, err := p.Svc.GetOrder(r.Context(), ident.ApplianceID, chi.URLParam(r, "id"))
	if err != nil {
		portalTLSFail(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// portalTLSRoutes mounts the console API. Reads need fleet.view; every write needs security.manage plus a fresh
// password (step-up), because these settings decide who can obtain certificates for the whole zone.
func (b *Base) portalTLSRoutes(r chi.Router) {
	if b.PortalTLS == nil {
		na := unavailable("portal_tls_unavailable", "portal certificates are not available on this Central")
		r.Get("/portal-tls/settings", na)
		return
	}
	perm := auth.RequirePermission
	su := RequireReauth(b.Redis)
	read := r.With(perm(auth.PermFleetView))
	read.Get("/portal-tls/settings", b.portalTLSSettings)
	read.Get("/portal-tls/fleet", b.portalTLSFleet)
	read.Get("/portal-tls/events", b.portalTLSEvents)
	write := r.With(perm(auth.PermSecurityManage), su)
	write.Put("/portal-tls/settings", b.putPortalTLSSettings)
	write.Put("/portal-tls/dns-token", b.putPortalTLSToken)
	write.Post("/portal-tls/test", b.testPortalTLS)
	write.Post("/portal-tls/caa", b.applyPortalTLSCAA)
	write.Post("/portal-tls/appliances/{id}/revoke", b.revokePortalTLS)
}

func (b *Base) portalTLSSettings(w http.ResponseWriter, r *http.Request) {
	v, err := b.PortalTLS.View(r.Context())
	if err != nil {
		portalTLSFail(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, v)
}

func (b *Base) putPortalTLSSettings(w http.ResponseWriter, r *http.Request) {
	var in portaltls.SettingsUpdate
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	_, email := actorOf(r)
	if _, err := b.PortalTLS.UpdateSettings(r.Context(), in, email); err != nil {
		portalTLSFail(w, r, err)
		return
	}
	audit.Op(r.Context(), b.DB, r, "portal_tls.settings_changed", "portal_tls", "settings", map[string]any{
		"acme_directory": in.ACMEDirectory, "contact_email": in.ContactEmail, "zone_name": in.ZoneName,
		"weekly_new_limit": in.WeeklyNewLimit})
	b.portalTLSSettings(w, r)
}

// putPortalTLSToken stores the DNS API token. Write-only: neither the answer, the audit record nor the event holds any of it.
func (b *Base) putPortalTLSToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	_, email := actorOf(r)
	err := b.PortalTLS.SetToken(r.Context(), in.Token, email)
	in.Token = ""
	if err != nil {
		portalTLSFail(w, r, err)
		return
	}
	audit.Op(r.Context(), b.DB, r, "portal_tls.dns_token_set", "portal_tls", "dns-token", nil)
	WriteJSON(w, http.StatusOK, map[string]any{"set": true})
}

func (b *Base) testPortalTLS(w http.ResponseWriter, r *http.Request) {
	_, email := actorOf(r)
	res, err := b.PortalTLS.TestAccess(r.Context(), email)
	if err != nil {
		portalTLSFail(w, r, err)
		return
	}
	audit.Op(r.Context(), b.DB, r, "portal_tls.access_tested", "portal_tls", "settings", map[string]any{
		"zone_id": res.ZoneID, "directory": res.Directory})
	WriteJSON(w, http.StatusOK, res)
}

func (b *Base) applyPortalTLSCAA(w http.ResponseWriter, r *http.Request) {
	_, email := actorOf(r)
	res, err := b.PortalTLS.ApplyCAA(r.Context(), email)
	if err != nil {
		portalTLSFail(w, r, err)
		return
	}
	audit.Op(r.Context(), b.DB, r, "portal_tls.caa_applied", "portal_tls", "zone", map[string]any{
		"issue": res.Issue, "other_issuers": res.OtherIssuers})
	WriteJSON(w, http.StatusOK, res)
}

func (b *Base) portalTLSFleet(w http.ResponseWriter, r *http.Request) {
	rows, err := b.PortalTLS.Fleet(r.Context())
	if err != nil {
		portalTLSFail(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": rows})
}

// portalTLSEvents: GET /cloud/v1/portal-tls/events?appliance_id=&limit=
func (b *Base) portalTLSEvents(w http.ResponseWriter, r *http.Request) {
	appID := strings.TrimSpace(r.URL.Query().Get("appliance_id"))
	limit := 200
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 1000 {
		limit = v
	}
	rows, err := b.PortalTLS.Events(r.Context(), appID, limit)
	if err != nil {
		portalTLSFail(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": rows})
}

// revokePortalTLS: POST /cloud/v1/portal-tls/appliances/{id}/revoke {reason, hostname?}.
func (b *Base) revokePortalTLS(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Reason   string `json:"reason"`
		Hostname string `json:"hostname"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if !requireReason(w, r, in.Reason) {
		return
	}
	_, email := actorOf(r)
	n, err := b.PortalTLS.RevokeAppliance(r.Context(), id, portaltls.NormalizeFQDN(in.Hostname), email, in.Reason)
	if err != nil {
		portalTLSFail(w, r, err)
		return
	}
	audit.Op(r.Context(), b.DB, r, "portal_tls.certificates_revoked", "appliance", id, map[string]any{
		"reason": in.Reason, "hostname": in.Hostname, "certificates": n})
	WriteJSON(w, http.StatusOK, map[string]any{"revocations_requested": n})
}
