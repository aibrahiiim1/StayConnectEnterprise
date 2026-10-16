// Edge-first refactor wiring: signed-license enforcement and walled-garden
// reconciliation from the site-local DB into nftables. Everything here works
// fully offline; Central only issues and renews the licence.
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

// licenseRefusal is the ONE decision every guest sign-in method takes before it may admit anybody: whether
// the cross-tenant transition guard, the local licence state and (for a method that is a commercial feature)
// the feature entitlement permit a new guest. It returns nil when the request may proceed, otherwise the
// refusal body -- the same codes whichever method refused, so operators and the portal read one vocabulary.
// feature == "" means "basic access" (voucher): allowed in every state that permits new sessions.
//
// It decides only. licenseGate writes the refusal in the JSON shape the voucher, account, OTP and social
// routes answer with; the PMS room sign-in (phase3_auth.go) answers in its own uniform envelope and records
// the refusal as a sign-in attempt, but it asks this same function, so the methods cannot disagree about who
// is refused.
func (s *server) licenseRefusal(feature string) map[string]any {
	// An appliance Central deleted after it held a customer admits nobody until it is factory-reset, whatever
	// licence state a development build would otherwise fall back to (removed_from_central.go).
	if s.isRemovedFromCentral() {
		return map[string]any{
			"error":   removedFromCentralCode,
			"message": "This appliance was removed from OneGate Central; guest access is unavailable.",
		}
	}
	// Fail CLOSED while a cross-tenant data transition is incomplete: never
	// authorize a guest until the previous tenant's local data has been fully
	// purged, so one customer's data can never be exposed under another's ownership.
	if s.tenantBlocked.Load() {
		return map[string]any{
			"error":   "tenant_transition_pending",
			"message": "This appliance is completing a customer transition; guest access is temporarily unavailable.",
		}
	}
	if s.lic == nil {
		// Fail CLOSED: a missing license manager is a startup/config fault, never
		// a reason to authorize a guest. (In practice s.lic is always set before
		// the listener starts; this is defence in depth.)
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

// licenseGate blocks a guest auth request that licenseRefusal refuses, answering 403 with the refusal body.
// Returns true when the request may proceed.
func (s *server) licenseGate(w http.ResponseWriter, feature string) bool {
	if body := s.licenseRefusal(feature); body != nil {
		writeJSON(w, http.StatusForbidden, body)
		return false
	}
	return true
}

// ----- license admin endpoints (unix socket; consumed by edged) -------------

// licenseInstall: POST /v1/license/install -- the Upload licence file action. Every other licence and
// activation read goes through /v1/central/status (central.go).
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
		// Anti-rollback: an older/superseded/revoked/replayed document is never
		// accepted — even with a valid signature, unexpired dates, a higher
		// guest limit, or Central offline.
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

func (s *server) pmsAdminReload(w http.ResponseWriter, r *http.Request) {
	if err := s.reloadPMS(r.Context()); err != nil {
		httpErr(w, http.StatusInternalServerError, "reload failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "reloaded"})
}

func (s *server) gardenReload(w http.ResponseWriter, r *http.Request) {
	n, err := s.reconcileWalledGarden(r.Context())
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "garden reconcile failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "reconciled", "elements": n})
}

// ----- Hotel Admin TLS certificate lifecycle (root-privileged exec) ----------

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

// hotelAdminCertCheck runs a diagnostic-only validation of the active cert.
// edged has already authenticated + authorized the caller before proxying here.
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

// ----- walled-garden reconciliation ------------------------------------------

// THE GARDEN IS LEAST PRIVILEGE BY PORT AS WELL AS BY ADDRESS (internal/nft/garden.go).
//
// The three baseline resolvers are reachable on port 53 only. What this appliance adds for a sign-in opens the
// port that sign-in needs and nothing else: an identity provider's consent page and a payment page on TCP 443.
// An operator's own rule keeps its stated meaning: the ports it names, or every port when it names none.
//
// There is no mail-service entry any more. It existed so a device could read an emailed one-time code before
// signing in; Email accounts need no pre-sign-in Internet at all (the verification link opens the local
// portal), so nothing is opened for mail (contract docs/architecture/ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md §1).
var gardenBaselineDNS = []string{"1.1.1.1", "8.8.8.8", "8.8.4.4"}

// gardenRule is one operator rule as stored (walled_garden_rules). Ports empty = every port.
type gardenRule struct {
	Kind, Value string
	Ports       []int
}

// gardenSources is everything the garden is built from, gathered by reconcileWalledGarden.
type gardenSources struct {
	Rules   []gardenRule
	Payment []string // host names -> TCP 443
	Social  []string // host names -> TCP 443
}

// planGarden turns the sources into the three sets' contents. resolve answers a host name with its IPv4
// addresses (nil when it cannot). Pure, so the classification is tested without a kernel or a resolver.
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

// reconcileWalledGarden loads the operator rules and the sign-in-derived host names, resolves names through
// the guest resolver, and syncs the three garden sets in one transaction. It returns the element count.
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
	// Card payment's hosted-page domains (least privilege: only while card payment is licensed, enabled and
	// deployed, only the providers in use, plus the site's bounded extra list).
	src.Payment = s.paymentGardenDomains(ctx)
	// The identity providers' consent pages (contract §8): only providers that are deployed, licensed and
	// switched on, with an enabled application. The token exchange is server-side and needs none of these.
	src.Social = s.socialGardenDomains(ctx)

	// Resolve names (best effort, short timeout each, once per name per pass). DNS answers churn; re-resolution
	// every reconcile pass keeps the sets fresh enough for login/payment endpoints, which is the garden's purpose.
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

// socialGardenDomains lists the pre-auth host names of every social provider the site currently offers.
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

// gardenResolver resolves garden host names THROUGH THE GUEST RESOLVER (unbound on the appliance, which is the
// nameserver every client device is handed by DHCP), so the addresses opened are the ones the clients were
// told -- answers cached by unbound are served to scd and to the phone alike. The appliance's own
// systemd-resolved can answer differently for a service behind a large CDN, which left a client holding an
// address the garden had never opened. SCD_GARDEN_RESOLVER overrides the address; "system" uses the host's.
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
	// Immediate pass on boot, then every minute.
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
			// SMS / WhatsApp code copies that can no longer be used (temp_access_admin.go). They used to be
			// pruned by the temporary-verification-window loop, which is removed with the email code; a copy is
			// already deleted the moment its code is verified, so this minute-cadence sweep only catches the
			// expired, locked and never-used ones.
			s.pruneOTPEscrow(ctx)
		}
	}
}

// applyLicenseToMethods removes unlicensed auth methods from the tenant
// config before it reaches the portal, so their tabs never render. Voucher
// stays — it is the basic-access method available in every non-terminal
// license state.
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
	// Licensed is not enough: the deployed guest authority must accept that kind of identity, or a client who
	// verifies a code or a social account would be refused after doing everything right.
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
	// Room sign-in is part of the Hotel module: licensed is not enough, the site must have it enabled and the
	// software deployed (the four-gate resolver decides).
	if !s.lic.FeatureEnabled(licstate.FeatPMS) || (s.modules != nil && !s.moduleEffective(context.Background(), lic.ModuleHospitality)) {
		cfg.PMS = nil
	}
}
