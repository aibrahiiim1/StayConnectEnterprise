// Edge-first refactor wiring: signed-license enforcement, walled-garden
// reconciliation from the site-local DB into nftables, and the durable
// telemetry outbox. Everything here works fully offline; the cloud only
// supplies license renewals and receives aggregated telemetry.
package main

import (
	"context"
	"encoding/json"
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
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
	lic "github.com/stayconnect/enterprise/license"
)

// licenseGate blocks a guest auth request when the license state refuses new
// sessions, or when the method's commercial feature is not entitled.
// feature == "" means "basic access" (voucher) — allowed in every state that
// permits new sessions. Returns true when the request may proceed.
func (s *server) licenseGate(w http.ResponseWriter, feature string) bool {
	if s.lic == nil {
		return true
	}
	if !s.lic.AllowsNewSessions() {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":         "license_expired",
			"license_state": string(s.lic.State()),
		})
		return false
	}
	if feature != "" && !s.lic.FeatureEnabled(feature) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":         "feature_not_licensed",
			"feature":       feature,
			"license_state": string(s.lic.State()),
		})
		return false
	}
	return true
}

// ----- license admin endpoints (unix socket; consumed by edged) -------------

// cloudInfo returns the appliance's Cloud connection identity + endpoints for
// the Hotel Admin Cloud Connection page. Secrets (NATS password) are masked; no
// keys/tokens are ever returned.
func (s *server) cloudInfo(w http.ResponseWriter, r *http.Request) {
	// Separate real transport states (5F): API mTLS and NATS mTLS are reported
	// distinctly — never a single generic "connected".
	apiMTLS := map[string]any{"ready": false}
	if s.certMgr != nil {
		apiMTLS = s.certMgr.Status() // {mtls_ready, cert_fingerprint, not_after}
	}
	natsMTLS := map[string]any{
		"url":       maskCreds(s.natsURL),
		"mtls":      strings.HasPrefix(s.natsURL, "tls://") && !strings.Contains(s.natsURL, "@"),
		"connected": s.natsConn != nil && s.natsConn.IsConnected(),
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cloud_api_url": s.ctrlBase,
		"nats_url":      maskCreds(s.natsURL),
		"tenant_id":     s.tenID,
		"site_id":       s.siteID,
		"appliance_id":  s.applID,
		"serial":        s.serial,
		"enrolled":      s.applID != "" && s.tenID != "",
		"api_mtls":      apiMTLS,
		"nats_mtls":     natsMTLS,
	})
}

// maskCreds strips any user:password from a URL (scheme://user:pass@host -> scheme://***@host).
func maskCreds(u string) string {
	at := strings.Index(u, "@")
	sep := strings.Index(u, "://")
	if at < 0 || sep < 0 || at < sep {
		return u
	}
	return u[:sep+3] + "***@" + u[at+1:]
}

func (s *server) licenseStatus(w http.ResponseWriter, r *http.Request) {
	if s.lic == nil {
		writeJSON(w, http.StatusOK, map[string]any{"state": "Active", "mode": "unlicensed-dev"})
		return
	}
	ev, loaded := s.lic.Evaluation()
	if !loaded {
		writeJSON(w, http.StatusOK, map[string]any{
			"state": string(s.lic.State()), "installed": false,
		})
		return
	}
	// Live usage against the licensed concurrent-online-guest cap.
	var current int64 = -1
	if s.sess != nil {
		if n, err := s.sess.ActiveCount(r.Context()); err == nil {
			current = n
		}
	}
	maxGuests := s.lic.MaxConcurrentOnlineGuests()
	var remaining any = "unlimited"
	var usagePct any
	if maxGuests > 0 {
		rem := maxGuests - current
		if rem < 0 {
			rem = 0
		}
		remaining = rem
		usagePct = float64(current) / float64(maxGuests) * 100
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state":                        string(ev.State),
		"installed":                    true,
		"license_id":                   ev.Doc.LicenseID,
		"license_version":              ev.Doc.LicenseVersion,
		"commercial_plan_code":         ev.Doc.CommercialPlanCode,
		"issued_at":                    ev.Doc.IssuedAt,
		"valid_from":                   ev.Doc.ValidFrom,
		"valid_until":                  ev.Doc.ValidUntil,
		"offline_grace_days":           ev.Doc.OfflineGraceDays,
		"grace_period_days":            ev.Doc.EffectiveGraceDays(),
		"grace_until":                  ev.GraceUntil,
		"restricted_until":             ev.RestrictedUntil,
		"features":                     ev.Doc.Features,
		"limits":                       ev.Doc.Limits,
		"max_concurrent_online_guests": maxGuests,
		"current_online_guests":        current,
		"remaining_capacity":           remaining,
		"usage_percent":                usagePct,
		"cloud_stale":                  ev.CloudStale,
		"clock_rollback":               ev.ClockRollback,
		"last_cloud_validation":        ev.LastCloudValidation,
	})
}

func (s *server) licenseInstall(w http.ResponseWriter, r *http.Request) {
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

func (s *server) licenseRefresh(w http.ResponseWriter, r *http.Request) {
	if s.lic == nil || s.licFetch == nil {
		httpErr(w, http.StatusServiceUnavailable, "cloud fetch not configured")
		return
	}
	if err := s.licFetch(r.Context()); err != nil {
		httpErr(w, http.StatusBadGateway, "refresh failed: "+err.Error())
		return
	}
	s.licenseStatus(w, r)
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

func (s *server) outboxStats(w http.ResponseWriter, r *http.Request) {
	if s.obx == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	pending, dead, oldest, err := s.obx.Stats(r.Context())
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "stats failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "pending": pending, "dead": dead, "oldest_pending": oldest,
	})
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

// ----- aggregated telemetry ---------------------------------------------------

// telemetryLoop enqueues non-PII operational summaries into the outbox:
// usage (session counts + byte totals) and health (disk, memory, uptime,
// outbox depth, license state). Aggregates only — never per-guest rows.
func (s *server) telemetryLoop(ctx context.Context, started time.Time) {
	if s.obx == nil {
		return
	}
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.enqueueUsage(ctx)
			s.enqueueHealth(ctx, started)
		}
	}
}

func (s *server) enqueueUsage(ctx context.Context) {
	var active, today int64
	var upToday, downToday int64
	_ = s.db.QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE state = 'active'`).Scan(&active)
	_ = s.db.QueryRow(ctx, `
        SELECT count(*), COALESCE(sum(bytes_up),0), COALESCE(sum(bytes_down),0)
          FROM sessions WHERE started_at >= date_trunc('day', now())
    `).Scan(&today, &upToday, &downToday)
	err := s.obx.Enqueue(ctx, "usage", map[string]any{
		"active_sessions":  active,
		"sessions_today":   today,
		"bytes_up_today":   upToday,
		"bytes_down_today": downToday,
	})
	if err != nil {
		slog.Debug("telemetry: usage enqueue failed", "err", err)
	}
}

func (s *server) enqueueHealth(ctx context.Context, started time.Time) {
	diskFree, diskTotal := diskStats("/var")
	pending, dead, _, _ := s.obx.Stats(ctx)
	licState := "unlicensed"
	if s.lic != nil {
		licState = string(s.lic.State())
	}
	payload := map[string]any{
		"uptime_seconds":  int64(time.Since(started).Seconds()),
		"disk_free_bytes": diskFree, "disk_total_bytes": diskTotal,
		"outbox_pending": pending, "outbox_dead": dead,
		"license_state": licState,
		"version":       "0.0.3-dev",
	}
	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		payload["loadavg"] = string(raw[:min(len(raw), 14)])
	}
	// Sanitized Hotel Admin TLS certificate health (no key, no guest data) so the
	// Central Fleet view can surface warning/critical/expired/renewal-failure.
	if hc := hotelAdminCertHealth(); hc != nil {
		payload["hotel_admin_cert"] = hc
	}
	if err := s.obx.Enqueue(ctx, "health", payload); err != nil {
		slog.Debug("telemetry: health enqueue failed", "err", err)
	}
}

// hotelAdminCertHealth reads the renewal manager's status file and returns ONLY
// non-sensitive fields for Central telemetry. Never includes the private key.
func hotelAdminCertHealth() map[string]any {
	raw, err := os.ReadFile("/etc/caddy/hotel-admin/status.json")
	if err != nil {
		return nil
	}
	var st map[string]any
	if json.Unmarshal(raw, &st) != nil {
		return nil
	}
	pick := func(k string) any { return st[k] }
	return map[string]any{
		"serial":                  pick("serial"),
		"fingerprint_sha256":      pick("fingerprint_sha256"),
		"expires_at":              pick("expires_at"),
		"days_remaining":          pick("days_remaining"),
		"status_threshold":        pick("status_threshold"),
		"san_config_match":        pick("san_config_match"),
		"current_management_ip":   pick("current_management_ip"),
		"last_renewal_result":     pick("last_renewal_result"),
		"last_successful_renewal": pick("last_successful_renewal"),
		"last_error":              pick("last_error"),
	}
}

// enqueueLicenseAck reports the outcome of a license (re)load to the cloud.
func (s *server) enqueueLicenseAck(ctx context.Context) {
	if s.obx == nil || s.lic == nil {
		return
	}
	ev, loaded := s.lic.Evaluation()
	payload := map[string]any{"state": string(s.lic.State()), "installed": loaded}
	if loaded {
		payload["license_ref"] = ev.Doc.LicenseID
		payload["valid_until"] = ev.Doc.ValidUntil
	}
	if err := s.obx.Enqueue(ctx, "license_ack", payload); err != nil {
		slog.Debug("telemetry: license_ack enqueue failed", "err", err)
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
	if !s.lic.FeatureEnabled(licstate.FeatPMS) {
		cfg.PMS = nil
	}
}
