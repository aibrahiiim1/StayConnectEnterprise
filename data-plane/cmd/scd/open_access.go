package main

// Open package selection ("Choose a package without signing in"). The switch is the Sign-in methods "open" key (default off).
// The subject is an opaque anonymous access subject (internal/iamv2/open_access.go), never the MAC; portald derives MAC and IP
// from the connection, and the body carries only the client's resume token or recovery code.

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/localkeys"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

const anonymousAccessKeyFile = "anonymous_access.key"

// initOpenAccess loads the anonymous-access HMAC key; without it open selection is unavailable (never degraded to a weaker scheme).
func (s *server) initOpenAccess(secretsDir string) {
	key, err := localkeys.LoadExistingKey(filepath.Join(secretsDir, anonymousAccessKeyFile))
	if err != nil {
		slog.Warn("open package selection unavailable: anonymous-access key missing (run keybootstrap at deploy)", "err", err)
		return
	}
	s.openAccess = &iamv2.OpenAccess{DB: s.db, Key: key, KeyID: "anon-1"}
	s.openLimiter = &recoveryLimiter{hits: map[string][]time.Time{}}
}

// recoveryLimiter bounds recovery-code guesses per device to 5 per 10 minutes; codes carry about 50 bits.
type recoveryLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func (l *recoveryLimiter) allow(mac string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	kept := l.hits[mac][:0]
	for _, t := range l.hits[mac] {
		if now.Sub(t) < 10*time.Minute {
			kept = append(kept, t)
		}
	}
	if len(kept) >= 5 {
		l.hits[mac] = kept
		return false
	}
	l.hits[mac] = append(kept, now)
	// Bounded by pruning aged entries, never by wiping the map: a wipe reset every device's count, so flooding the map would clear one's own limit.
	if len(l.hits) > 20000 {
		for k, ts := range l.hits {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= 10*time.Minute {
				delete(l.hits, k)
			}
		}
	}
	return true
}

type openAuthReq struct {
	IP           string `json:"ip"`
	MAC          string `json:"mac"`
	ResumeToken  string `json:"resume_token,omitempty"`
	RecoveryCode string `json:"recovery_code,omitempty"`
}

func (s *server) openEnabled(r *http.Request) bool {
	var cfg *tenantcfg.AuthMethods
	var err error
	if s.methodSwitches != nil {
		cfg, err = s.methodSwitches(r.Context())
	} else if s.db != nil {
		cfg, err = tenantcfg.Load(r.Context(), s.db, s.tenID)
	}
	return err == nil && cfg != nil && cfg.Open != nil && cfg.Open.Enabled
}

func (s *server) authorizeOpen(w http.ResponseWriter, r *http.Request) {
	var req openAuthReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	ip := net.ParseIP(req.IP)
	mac, err := net.ParseMAC(req.MAC)
	if ip == nil || ip.To4() == nil || err != nil {
		httpErr(w, http.StatusBadRequest, "bad identity")
		return
	}
	if !s.licenseGate(w, "") {
		return
	}
	if s.openAccess == nil || !s.openEnabled(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "METHOD_DISABLED", "authority": "iam_v2"})
		return
	}
	nc := s.resolveNetwork(r.Context(), ip)
	if nc.NetworkID == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "NO_GUEST_NETWORK", "authority": "iam_v2"})
		return
	}
	if strings.TrimSpace(req.RecoveryCode) != "" && !s.openLimiter.allow(mac.String()) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "TOO_MANY_ATTEMPTS", "authority": "iam_v2"})
		return
	}
	res, err := s.openAccess.Begin(r.Context(), iamv2.OpenRequest{
		TenantID: s.tenID, SiteID: s.siteID, ApplianceID: s.applianceID,
		MAC: mac.String(), IP: ip.String(), GuestNetworkID: nc.NetworkID,
		ResumeToken: strings.TrimSpace(req.ResumeToken), RecoveryCode: req.RecoveryCode,
	})
	if errors.Is(err, iamv2.ErrOpenRecoveryUnknown) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "RECOVERY_CODE_UNKNOWN", "authority": "iam_v2"})
		return
	}
	if err != nil {
		slog.Error("open package selection failed", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "IAMV2_UNAVAILABLE", "authority": "iam_v2"})
		return
	}
	out := map[string]any{
		"auth_context_id": res.AuthContextID, "device_id": res.DeviceID, "guest_network_id": nc.NetworkID,
		"method": "OPEN", "authority": "iam_v2", "resumed": res.Resumed,
	}
	if res.LiveEntitlementID != "" {
		out["live_entitlement_id"] = res.LiveEntitlementID
	}
	if res.NewResumeToken != "" {
		out["resume_token"] = res.NewResumeToken
		out["recovery_code"] = res.NewRecoveryCode
	}
	writeJSON(w, http.StatusOK, out)
}
