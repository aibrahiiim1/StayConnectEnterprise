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
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/nft"
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

// reconcileWalledGarden loads walled_garden_rules from the site DB, resolves
// domain rules via DNS, and syncs the nft walled_garden_ip set. Baseline
// elements (public DNS used by captive-portal probes) are always kept.
var gardenBaseline = []string{"1.1.1.1", "8.8.8.8", "8.8.4.4"}

func (s *server) reconcileWalledGarden(ctx context.Context) (int, error) {
	rows, err := s.db.Query(ctx, `
        SELECT kind, value FROM walled_garden_rules
         WHERE tenant_id = $1 AND (site_id IS NULL OR site_id::text = $2)
    `, s.tenID, s.siteID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	want := map[string]bool{}
	for _, b := range gardenBaseline {
		want[b] = true
	}
	type domain struct{ name string }
	var domains []domain
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			return 0, err
		}
		switch kind {
		case "ip", "cidr":
			if nft.ValidGardenElem(value) {
				want[value] = true
			} else {
				slog.Warn("walled-garden: skipping invalid element", "kind", kind, "value", value)
			}
		case "domain":
			domains = append(domains, domain{value})
		}
	}
	// Resolve domains (best effort, short timeout each). DNS answers churn;
	// re-resolution every reconcile pass keeps the set fresh enough for
	// login/payment endpoints, which is the walled garden's purpose.
	// Card payment's hosted-page domains (least privilege: only while card payment is licensed, enabled and
	// deployed, only the providers in use, plus the site's bounded extra list).
	for _, d := range s.paymentGardenDomains(ctx) {
		domains = append(domains, domain{d})
	}
	resolver := &net.Resolver{}
	for _, d := range domains {
		rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		ips, err := resolver.LookupIP(rctx, "ip4", d.name)
		cancel()
		if err != nil {
			slog.Warn("walled-garden: domain resolve failed", "domain", d.name, "err", err)
			continue
		}
		for _, ip := range ips {
			want[ip.String()] = true
		}
	}
	elems := make([]string, 0, len(want))
	for e := range want {
		elems = append(elems, e)
	}
	sort.Strings(elems)
	if err := s.nft.client.GardenSync(ctx, elems); err != nil {
		return 0, err
	}
	return len(elems), nil
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
		cfg.Email = nil
	}
	if !s.lic.FeatureEnabled(licstate.FeatSMSOTP) {
		cfg.SMS = nil
	}
	if !s.lic.FeatureEnabled(licstate.FeatSocialLogin) {
		cfg.Social = nil
	}
	// Room sign-in is part of the Hotel module: licensed is not enough, the site must have it enabled and the
	// software deployed (the four-gate resolver decides).
	if !s.lic.FeatureEnabled(licstate.FeatPMS) || (s.modules != nil && !s.moduleEffective(context.Background(), lic.ModuleHospitality)) {
		cfg.PMS = nil
	}
}
