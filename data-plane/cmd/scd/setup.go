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
				"offline_grace_days": ev.Doc.OfflineGraceDays}
		} else {
			lic = map[string]any{"state": string(s.lic.State())}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"serial":                  s.serial,
		"appliance_id":            s.applID,
		"identity_key_fingerprint": s.identityKeyFpr,
		"version":                 scdVersion,
		"enrolled":                s.enrolled,
		"locked":                  s.enrolled, // bootstrap setup is locked once enrolled
		"tenant_id":               s.tenID,
		"site_id":                 s.siteID,
		"api_mtls":                api,
		"nats_mtls":               nats,
		"license":                 lic,
		"assignment":              s.assignmentStatus(),
		"network":                 s.networkChecks(),
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
	id, err := s.idStore.LoadOrEnroll(r.Context(), s.ctrlBase, req.Token, req.Serial)
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
