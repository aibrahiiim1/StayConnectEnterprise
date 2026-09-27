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

	"github.com/stayconnect/enterprise/data-plane/internal/cloudmode"
	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/nft"
	"github.com/stayconnect/enterprise/data-plane/internal/outbox"
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

func (s *server) outboxStats(w http.ResponseWriter, r *http.Request) {
	if s.obx == nil {
		// enabled:false has always meant "this appliance is not reporting upward". It now carries WHY,
		// because "not in use" and "switched off by a decision" look identical to an operator and only one
		// of them is something to investigate. The accounting still comes from the database: the records
		// are retained, and a screen that hid them would be implying a deletion that did not happen.
		out := map[string]any{"enabled": false, "mode": string(s.cloudMode)}
		if s.cloudMode == cloudmode.LicensingOnly {
			out["reason"] = "licensing_only"
		}
		if acct, err := (&outbox.Outbox{DB: s.db}).Account(r.Context()); err == nil {
			out["delivered"] = acct.Delivered
			out["pending"] = acct.Pending
			out["dead"] = acct.Exhausted
			out["total"] = acct.Total
			out["bytes"] = acct.Bytes
			out["balanced"] = acct.Balanced()
			out["oldest_pending"] = acct.OldestPending
			out["retention_days"] = (&outbox.Outbox{DB: s.db}).RetentionDays(r.Context(), s.tenID, s.siteID)
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	acct, err := s.obx.Account(r.Context())
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "stats failed")
		return
	}
	out := s.obx.LastOutcome()
	// pending/dead/oldest_pending keep their names and meaning so nothing reading this breaks. What is new
	// is the rest: how many were DELIVERED (previously unknowable from here, which is why "is it draining?"
	// had to be guessed from the pending count moving), how much disk the queue holds, whether the three
	// buckets still account for every record, and what the last attempt actually learned.
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "mode": string(s.cloudMode),
		"pending": acct.Pending, "dead": acct.Exhausted, "oldest_pending": acct.OldestPending,
		"delivered": acct.Delivered, "total": acct.Total, "bytes": acct.Bytes,
		"oldest_exhausted": acct.OldestExhausted, "balanced": acct.Balanced(),
		"retention_days": s.obx.RetentionDays(r.Context(), s.tenID, s.siteID),
		"delivery": map[string]any{
			"state": string(out.State), "at": out.At, "sent": out.Sent, "detail": out.Detail,
		},
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
	// Telemetry counts from the single session authority. Aggregates only -- no guest PII.
	_ = s.db.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.sessions WHERE tenant_id=$1 AND site_id=$2 AND state = 'active'`,
		s.tenID, s.siteID).Scan(&active)
	_ = s.db.QueryRow(ctx, `
        SELECT count(*), COALESCE(sum(bytes_up),0), COALESCE(sum(bytes_down),0)
          FROM iam_v2.sessions
         WHERE tenant_id=$1 AND site_id=$2 AND started >= date_trunc('day', now())
    `, s.tenID, s.siteID).Scan(&today, &upToday, &downToday)
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
