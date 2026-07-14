package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// setupStatus backs the local /setup/enrollment wizard. It reports the full
// real onboarding state: identity + distinct key fingerprints, network/DNS/
// clock/port checks, cert, NATS mTLS, license and enrollment lock. Read-only.
func (s *server) setupStatus(w http.ResponseWriter, r *http.Request) {
	api := map[string]any{"ready": false}
	nats := map[string]any{"connected": false}
	if s.certMgr != nil {
		api = s.certMgr.Status()
	}
	if s.natsConn != nil {
		nats = map[string]any{"connected": s.natsConn.IsConnected(), "mtls": strings.HasPrefix(s.natsURL, "tls://") && !strings.Contains(s.natsURL, "@")}
	}
	lic := map[string]any{}
	if s.lic != nil {
		if ev, ok := s.lic.Evaluation(); ok && ev.Doc != nil {
			lic = map[string]any{"state": string(ev.State), "license_id": ev.Doc.LicenseID,
				"plan": ev.Doc.CommercialPlanCode, "valid_until": ev.Doc.ValidUntil,
				"offline_grace_days": ev.Doc.OfflineGraceDays,
				// Simple license model: usage against the licensed cap.
				"license_version":   ev.Doc.LicenseVersion,
				"valid_from":        ev.Doc.ValidFrom,
				"grace_period_days": ev.Doc.EffectiveGraceDays(),
				"grace_ends_at":     ev.GraceUntil,
			}
			maxGuests := s.lic.MaxConcurrentOnlineGuests()
			lic["max_concurrent_online_guests"] = maxGuests
			if s.sess != nil {
				if n, err := s.sess.ActiveCount(r.Context()); err == nil {
					lic["current_online_guests"] = n
					if maxGuests > 0 {
						rem := maxGuests - n
						if rem < 0 {
							rem = 0
						}
						lic["remaining_capacity"] = rem
						lic["usage_percent"] = float64(n) / float64(maxGuests) * 100
					}
				}
			}
		} else {
			lic = map[string]any{"state": string(s.lic.State())}
		}
	}
	outbox := map[string]any{"pending": 0, "dead": 0}
	if s.obx != nil {
		if p, d, _, err := s.obx.Stats(r.Context()); err == nil {
			outbox = map[string]any{"pending": p, "dead": d}
		}
	}

	// Stable hardware identity — shown on the License/Activation screen before
	// (and after) activation. The StayConnect serial is the operator-facing id;
	// the WAN MAC is the licensing anchor.
	hw := map[string]any{
		"serial":        s.hw.Serial,
		"wan_interface": s.hw.WANInterface,
		"wan_mac":       s.hw.WANMAC,
		"lan_interface": s.hw.LANInterface,
		"lan_mac":       s.hw.LANMAC,
		"hostname":      s.hw.Hostname,
		"model":         s.hw.Model,
	}
	// activation_status: the one-word state the operator cares about. A real
	// signed license (non-empty license_id) is required — the permissive
	// unlicensed-dev licstate reports state="Active" with no license_id and must
	// NOT read as licensed.
	licState, _ := lic["state"].(string)
	licID, _ := lic["license_id"].(string)
	licLive := map[string]bool{"active": true, "licensed": true, "grace": true, "graceperiod": true}[strings.ToLower(licState)]
	realLicensed := licLive && licID != ""
	mtlsReady, _ := api["mtls_ready"].(bool)
	// Hardware Binding Mismatch (WAN NIC replaced / VM migration): the license is
	// bound to this identity but a different WAN MAC. A time-limited, audited
	// grace keeps the hotel running until an authorized rebind; surfaced here so
	// the operator sees a clear Mismatch state.
	hwMismatch := ""
	if s.lic != nil {
		hwMismatch = s.lic.HardwareMismatch()
	}
	activation := "unlicensed"
	switch {
	case s.enrolled && realLicensed && hwMismatch != "":
		activation = "mismatch"
	case s.enrolled && realLicensed && mtlsReady:
		activation = "activated"
	case s.enrolled && realLicensed:
		activation = "licensed"
	case s.enrolled:
		activation = "pending_activation"
	}

	// The operator-facing serial IS the hardware serial. Fall back to the
	// enrolled identity serial only if hardware detection yielded nothing.
	serial := s.hw.Serial
	if serial == "" {
		serial = s.serial
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"serial":                   serial,
		"hardware":                 hw,
		"activation_status":        activation,
		"hardware_mismatch":        hwMismatch,
		"appliance_id":             s.applID,
		"identity_key_fingerprint": s.identityKeyFpr,
		"version":                  scdVersion,
		"enrolled":                 s.enrolled,
		"locked":                   s.enrolled, // bootstrap setup is locked once enrolled
		"tenant_id":                s.tenID,
		"site_id":                  s.siteID,
		"api_mtls":                 api,
		"nats_mtls":                nats,
		"license":                  lic,
		"assignment":               s.assignmentStatus(),
		"network":                  s.networkChecks(),
		"outbox":                   outbox,
	})
}

// networkChecks probes the management/WAN + Central reachability without
// leaking secrets: DNS resolution, HTTPS/mTLS/NATS port reachability, clock.
func (s *server) networkChecks() map[string]any {
	out := map[string]any{}
	host := ""
	if u, err := url.Parse(s.ctrlBase); err == nil {
		host = u.Hostname()
	}
	// DNS
	if host != "" {
		_, err := net.LookupHost(host)
		out["dns_ok"] = err == nil
	}
	dial := func(addr string) bool {
		c, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}
	if host != "" {
		out["central_https_443"] = dial(host + ":443")
		out["mtls_9443"] = dial(host + ":9443")
	}
	// NATS mTLS port (from natsURL host:port)
	if nu, err := url.Parse(strings.Replace(s.natsURL, "tls://", "https://", 1)); err == nil && nu.Host != "" {
		out["nats_4223"] = dial(nu.Host)
	}
	out["clock"] = time.Now().UTC()
	return out
}

// setupEnroll accepts an Enrollment Token at runtime (installer flow, no env or
// DB editing). Once enrolled the flow is LOCKED — re-enrollment is refused here
// (reset is a separate Hotel-IT-gated path) and identity is never silently
// regenerated.
func (s *server) setupEnroll(w http.ResponseWriter, r *http.Request) {
	if s.enrolled {
		httpErr(w, http.StatusConflict, "already enrolled — setup is locked")
		return
	}
	var req struct {
		Token  string `json:"token"`
		Serial string `json:"serial"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" || req.Serial == "" {
		httpErr(w, http.StatusBadRequest, "token and serial required")
		return
	}
	if s.idStore == nil {
		httpErr(w, http.StatusServiceUnavailable, "identity store unavailable")
		return
	}
	id, err := s.idStore.LoadOrEnroll(r.Context(), s.ctrlBase, req.Token, req.Serial, false)
	if err != nil || id == nil {
		msg := "enrollment failed"
		if err != nil {
			msg = err.Error()
		}
		httpErr(w, http.StatusBadGateway, msg)
		return
	}
	// Identity persisted. Restart scd so it initializes cloud transports with
	// the new identity (installer sees "pending → connected" after restart).
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = exec.Command("systemctl", "restart", "stayconnect-scd").Run()
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "enrolled", "appliance_id": id.ApplianceID, "note": "identity installed; scd restarting to connect",
	})
}
