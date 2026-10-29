// Signed-licence enforcement and walled-garden reconciliation into nftables; works fully offline (Central only issues and renews the licence).
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/nft"
	"github.com/stayconnect/enterprise/data-plane/internal/social"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
	lic "github.com/stayconnect/enterprise/license"
)

// licenseRefusal is the ONE decision every guest sign-in method takes before admitting anybody (cross-tenant guard, licence state,
// feature entitlement); nil means proceed. feature == "" means basic access (voucher). It only decides: licenseGate writes the
// JSON refusal, and the PMS room sign-in asks the same function so the methods cannot disagree.
func (s *server) licenseRefusal(feature string) map[string]any {
	// A Central-deleted appliance admits nobody until factory-reset, whatever licence state a development build would fall back to.
	if s.isRemovedFromCentral() {
		return map[string]any{
			"error":   removedFromCentralCode,
			"message": "This appliance was removed from OneGate Central; guest access is unavailable.",
		}
	}
	// Fail CLOSED while a cross-tenant transition is incomplete: never authorize a guest until the previous tenant's data is purged.
	if s.tenantBlocked.Load() {
		return map[string]any{
			"error":   "tenant_transition_pending",
			"message": "This appliance is completing a customer transition; guest access is temporarily unavailable.",
		}
	}
	if s.lic == nil {
		// Fail CLOSED: a missing licence manager is a config fault, never a reason to authorize a guest.
		return map[string]any{"error": "unlicensed", "license_state": string(lic.StateUnlicensed)}
	}
	if !s.lic.AllowsNewSessions() {
		st := string(s.lic.State())
		errCode := "license_expired"
		if st == string(lic.StateUnlicensed) {
			errCode = "unlicensed"
		}
		return map[string]any{"error": errCode, "license_state": st}
	}
	if feature != "" && !s.lic.FeatureEnabled(feature) {
		return map[string]any{
			"error":         "feature_not_licensed",
			"feature":       feature,
			"license_state": string(s.lic.State()),
		}
	}
	// The Hotel module must also be enabled by the site and deployed; a licence alone never executes it.
	if feature == licstate.FeatPMS && s.modules != nil && !s.moduleEffective(context.Background(), lic.ModuleHospitality) {
		return map[string]any{
			"error":         "module_not_enabled",
			"feature":       feature,
			"license_state": string(s.lic.State()),
		}
	}
	return nil
}

// licenseGate answers 403 with the refusal body when licenseRefusal refuses; true means proceed.
func (s *server) licenseGate(w http.ResponseWriter, feature string) bool {
	if body := s.licenseRefusal(feature); body != nil {
		writeJSON(w, http.StatusForbidden, body)
		return false
	}
	return true
}

// licenseInstall: POST /v1/license/install (Upload licence file).
func (s *server) licenseInstall(w http.ResponseWriter, r *http.Request) {
	if s.refuseWhileRemoved(w) {
		return
	}
	if s.lic == nil {
		httpErr(w, http.StatusServiceUnavailable, "license manager unavailable")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(raw) == 0 {
		httpErr(w, http.StatusBadRequest, "empty body")
		return
	}
	doc, err := s.lic.Install(r.Context(), raw)
	if err != nil {
		// Anti-rollback: an older, superseded, revoked or replayed document is never accepted, even validly signed.
		if errors.Is(err, lic.ErrRollback) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":  "LICENSE_ROLLBACK_REJECTED",
				"detail": err.Error(),
			})
			return
		}
		httpErr(w, http.StatusBadRequest, "install failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "installed", "license_id": doc.LicenseID,
		"license_version": doc.LicenseVersion,
		"state":           string(s.lic.State()),
	})
}

func (s *server) gardenReload(w http.ResponseWriter, r *http.Request) {
	n, err := s.reconcileWalledGarden(r.Context())
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "garden reconcile failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "reconciled", "elements": n})
}

const hotelCertManagerBin = "/usr/local/sbin/stayconnect-hotel-admin-cert-manager"

func runHotelCertManager(action string) (int, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, hotelCertManagerBin, action).CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	s := strings.TrimSpace(string(out))
	if len(s) > 800 {
		s = s[len(s)-800:]
	}
	return code, s
}

// hotelAdminCertCheck is a diagnostic-only validation; edged has already authorized the caller.
func (s *server) hotelAdminCertCheck(w http.ResponseWriter, r *http.Request) {
	code, out := runHotelCertManager("check")
	writeJSON(w, http.StatusOK, map[string]any{"ok": code == 0, "exit": code, "output_tail": out})
}

// hotelAdminCertRotate forces a rotation through the manager's full safe lifecycle
// (mint → validate → atomic swap → caddy reload → dual-URL health → rollback).
func (s *server) hotelAdminCertRotate(w http.ResponseWriter, r *http.Request) {
	code, out := runHotelCertManager("rotate")
	writeJSON(w, http.StatusOK, map[string]any{"ok": code == 0 || code == 2, "exit": code, "output_tail": out})
}

// hotelAdminCertRenew runs the idempotent renew check (renews only if due, IP
// changed, or SAN drift). Triggered after a confirmed management-IP change.
func (s *server) hotelAdminCertRenew(w http.ResponseWriter, r *http.Request) {
	code, out := runHotelCertManager("renew")
	writeJSON(w, http.StatusOK, map[string]any{"ok": code == 0 || code == 2, "exit": code, "output_tail": out})
}

// The garden is least privilege by port as well as address (internal/nft/garden.go): baseline resolvers on 53 only; a sign-in opens only the
// port it needs (identity provider consent and payment pages, TCP 443); an operator rule keeps its stated ports, or all when it names none.
// No mail-service entry: Email accounts need no pre-sign-in Internet (docs/architecture/ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md section 1).
var gardenBaselineDNS = []string{"1.1.1.1", "8.8.8.8", "8.8.4.4"}

// gardenRule is one operator rule as stored (walled_garden_rules). Ports empty = every port.
type gardenRule struct {
	Kind, Value string
	Ports       []int
}

type gardenSources struct {
	Rules   []gardenRule
	Payment []string // host names -> TCP 443
	Social  []string // host names -> TCP 443
}

// planGarden turns the sources into the three sets' contents; pure, so it is tested without a kernel or resolver.
func planGarden(src gardenSources, resolve func(name string) []string) nft.GardenPlan {
	ip, tcp, udp := map[string]bool{}, map[string]bool{}, map[string]bool{}
	pair := func(set map[string]bool, addr string, port int) {
		if e := nft.GardenPortElem(addr, port); e != "" {
			set[e] = true
		}
	}
	for _, b := range gardenBaselineDNS {
		pair(tcp, b, 53)
		pair(udp, b, 53)
	}
	addrsOf := func(r gardenRule) []string {
		switch r.Kind {
		case "ip", "cidr":
			if nft.ValidGardenElem(r.Value) {
				return []string{r.Value}
			}
			slog.Warn("walled-garden: skipping invalid element", "kind", r.Kind, "value", r.Value)
		case "domain":
			return resolve(r.Value)
		}
		return nil
	}
	for _, r := range src.Rules {
		for _, a := range addrsOf(r) {
			if len(r.Ports) == 0 {
				if nft.ValidGardenElem(a) {
					ip[a] = true
				}
				continue
			}
			for _, p := range r.Ports {
				pair(tcp, a, p)
				pair(udp, a, p)
			}
		}
	}
	for _, d := range append(append([]string{}, src.Payment...), src.Social...) {
		for _, a := range resolve(d) {
			pair(tcp, a, 443)
		}
	}
	keys := func(m map[string]bool) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	return nft.GardenPlan{IP: keys(ip), TCP: keys(tcp), UDP: keys(udp)}
}

// reconcileWalledGarden syncs the three garden sets in one transaction and returns the element count.
func (s *server) reconcileWalledGarden(ctx context.Context) (int, error) {
	rows, err := s.db.Query(ctx, `
        SELECT kind, value, COALESCE(ports, '{}') FROM walled_garden_rules
         WHERE tenant_id = $1 AND (site_id IS NULL OR site_id::text = $2)
    `, s.tenID, s.siteID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var src gardenSources
	for rows.Next() {
		var r gardenRule
		if err := rows.Scan(&r.Kind, &r.Value, &r.Ports); err != nil {
			return 0, err
		}
		src.Rules = append(src.Rules, r)
	}
	// Card payment's hosted-page domains: least privilege, only while payment is licensed, enabled and deployed.
	src.Payment = s.paymentGardenDomains(ctx)
	// Identity providers' consent pages only; the token exchange is server-side and needs none.
	src.Social = s.socialGardenDomains(ctx)

	// Resolve names best effort, once per name per pass; re-resolving each pass keeps churning DNS answers fresh.
	resolver := gardenResolver()
	cache := map[string][]string{}
	resolve := func(name string) []string {
		name = strings.ToLower(strings.TrimSpace(name))
		if v, ok := cache[name]; ok {
			return v
		}
		rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		ips, err := resolver.LookupIP(rctx, "ip4", name)
		cancel()
		var out []string
		if err != nil {
			slog.Warn("walled-garden: domain resolve failed", "domain", name, "err", err)
		}
		for _, ip := range ips {
			if v4 := ip.To4(); v4 != nil {
				out = append(out, v4.String())
			}
		}
		cache[name] = out
		return out
	}
	plan := planGarden(src, resolve)
	if err := s.nft.client.GardenSync(ctx, plan); err != nil {
		return 0, err
	}
	return len(plan.IP) + len(plan.TCP) + len(plan.UDP), nil
}

func (s *server) socialGardenDomains(ctx context.Context) []string {
	if s.db == nil {
		return nil
	}
	snap := s.moduleSnapshot(ctx)
	st := snap.Modules[lic.ModuleSocialLogin]
	if !(st.Deployed && st.Licensed && st.Enabled) {
		return nil
	}
	cfg, err := tenantcfg.Load(ctx, s.db, s.tenID)
	if err != nil || cfg == nil {
		return nil
	}
	rows, err := s.db.Query(ctx, `SELECT DISTINCT provider FROM social_oauth_providers WHERE tenant_id=$1 AND enabled`, s.tenID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) != nil {
			continue
		}
		if m := cfg.Social[p]; m == nil || !m.Enabled {
			continue
		}
		for _, d := range social.PreAuthDomains(p) {
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	return out
}

// gardenResolver resolves THROUGH THE GUEST RESOLVER (unbound), the nameserver clients are handed by DHCP, so the opened addresses are the
// ones clients were told; systemd-resolved can answer differently behind a CDN. SCD_GARDEN_RESOLVER overrides; "system" uses the host's.
func gardenResolver() *net.Resolver {
	addr := strings.TrimSpace(os.Getenv("SCD_GARDEN_RESOLVER"))
	if addr == "" {
		addr = "127.0.0.1:53"
	}
	if addr == "system" {
		return &net.Resolver{}
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 2 * time.Second}
			return d.DialContext(ctx, network, addr)
		},
	}
}

func (s *server) gardenReconcileLoop(ctx context.Context) {
	if n, err := s.reconcileWalledGarden(ctx); err != nil {
		slog.Warn("walled-garden: boot reconcile failed", "err", err)
	} else {
		slog.Info("walled-garden: reconciled", "elements", n)
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.reconcileWalledGarden(ctx); err != nil {
				slog.Warn("walled-garden: reconcile failed", "err", err)
			}
			// Prune SMS/WhatsApp code copies that can no longer be used; verified codes are already deleted, so this only catches expired, locked and unused ones.
			s.pruneOTPEscrow(ctx)
		}
	}
}

// applyLicenseToMethods removes unlicensed auth methods before the config reaches the portal; voucher stays as basic access.
func (s *server) applyLicenseToMethods(cfg *tenantcfg.AuthMethods) {
	if s.lic == nil || cfg == nil {
		return
	}
	if !s.lic.FeatureEnabled(licstate.FeatEmailOTP) {
		cfg.EmailAccount = nil
	}
	if !s.lic.FeatureEnabled(licstate.FeatSMSOTP) {
		cfg.SMS = nil
	}
	if !s.lic.FeatureEnabled(licstate.FeatWhatsAppOTP) {
		cfg.WhatsApp = nil
	}
	if !s.lic.FeatureEnabled(licstate.FeatSocialLogin) {
		cfg.Social = nil
	}
	// Licensed is not enough: the deployed guest authority must accept the identity kind, or a client would be refused after doing everything right.
	if s.modules != nil {
		if !s.ceiling.ModuleDeployed(lic.ModuleEmailOTP) {
			cfg.EmailAccount = nil
		}
		if !s.ceiling.ModuleDeployed(lic.ModuleSMSOTP) {
			cfg.SMS = nil
		}
		if !s.ceiling.ModuleDeployed(lic.ModuleWhatsAppOTP) {
			cfg.WhatsApp = nil
		}
		if !s.ceiling.ModuleDeployed(lic.ModuleSocialLogin) {
			cfg.Social = nil
		}
	}
	// Room sign-in is part of the Hotel module: the site must have it enabled and deployed (four-gate resolver).
	if !s.lic.FeatureEnabled(licstate.FeatPMS) || (s.modules != nil && !s.moduleEffective(context.Background(), lic.ModuleHospitality)) {
		cfg.PMS = nil
	}
}

// licenseClockReset is the audited operator action "the clock was wrong" (AD-17). It asks Central for its authenticated
// time over mutual TLS and sets the licence clock high-water mark to it, which is the only way a far-future boot is
// recovered without deleting the state file (and with it the rollback protection). It refuses while the local clock
// still disagrees with Central, and without a working mutual-TLS link: offline appliances correct their clock first
// and the mark then follows normal time.
func (s *server) licenseClockReset(w http.ResponseWriter, r *http.Request) {
	if s.refuseWhileRemoved(w) {
		return
	}
	if s.lic == nil || s.certMgr == nil || !s.certMgr.Ready() {
		httpErr(w, http.StatusServiceUnavailable, "the secure link to Central is not up, so Central's time cannot be read")
		return
	}
	hello, err := s.certMgr.MTLSHello(r.Context())
	if err != nil {
		httpErr(w, http.StatusBadGateway, "could not read Central's time: "+err.Error())
		return
	}
	raw, _ := hello["server_time"].(string)
	central, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		httpErr(w, http.StatusBadGateway, "Central did not return a usable time")
		return
	}
	old, err := s.lic.ResetClock(r.Context(), central)
	if err != nil {
		httpErr(w, http.StatusConflict, err.Error())
		return
	}
	slog.Warn("licstate: licence clock high-water mark reset to Central time by an operator", "previous", old, "central_time", central)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "reset", "previous_high_water": old, "high_water": central.UTC(), "state": string(s.lic.State()),
	})
}
