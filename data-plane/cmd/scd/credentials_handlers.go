package main

// Guest Username/Password authorization. Uses the SAME production pipeline as
// every other method (license gate → atomic capacity reservation → session → nft
// → shaping → accounting). Never a separate or weaker path.

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/stayconnect/enterprise/data-plane/internal/session"
	"github.com/stayconnect/enterprise/data-plane/internal/voucher"
)

const (
	gaMaxFailedAttempts = 5
	gaLockMinutes       = 15
)

// gaDummyHash lets us run a constant-shape argon2 verify for unknown usernames so
// response time does not reveal whether a username exists (anti-enumeration).
var gaDummyHash = func() string {
	salt := make([]byte, 16)
	key := argon2.IDKey([]byte("x"), salt, 1, 64*1024, 4, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", 64*1024, 1, 4,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}()

func verifyArgon2(pw, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// authorizeGuestAccount validates a username/password against the CURRENT tenant's
// guest_accounts and, on success, drives the normal session pipeline. Every
// failure mode (unknown username, wrong password, disabled, expired, locked out)
// returns the SAME generic INVALID_USERNAME_OR_PASSWORD and creates no session,
// nft, shaping or accounting state.
func (s *server) authorizeGuestAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		IP       string `json:"ip"`
		MAC      string `json:"mac"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if !s.licenseGate(w, "") {
		return
	}
	ip := net.ParseIP(req.IP)
	if ip == nil || ip.To4() == nil {
		httpErr(w, http.StatusBadRequest, "bad ip")
		return
	}
	mac, err := net.ParseMAC(req.MAC)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "bad mac")
		return
	}
	username := strings.TrimSpace(req.Username)

	genericFail := func() {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "INVALID_USERNAME_OR_PASSWORD"})
	}

	var (
		accountID, hash, displayName, tplID string
		enabled                             bool
		validFrom, validUntil, lockedUntil  *time.Time
		failed                              int
	)
	err = s.db.QueryRow(r.Context(), `
        SELECT id, password_hash, COALESCE(display_name,''), template_id, enabled,
               valid_from, valid_until, locked_until, failed_attempts
          FROM guest_accounts WHERE tenant_id=$1 AND lower(username)=lower($2)`,
		s.tenID, username).Scan(&accountID, &hash, &displayName, &tplID, &enabled,
		&validFrom, &validUntil, &lockedUntil, &failed)
	if err != nil {
		_ = verifyArgon2(req.Password, gaDummyHash) // burn comparable time
		genericFail()
		return
	}
	now := time.Now()
	if lockedUntil != nil && now.Before(*lockedUntil) {
		_ = verifyArgon2(req.Password, gaDummyHash)
		genericFail()
		return
	}
	ok := verifyArgon2(req.Password, hash)
	valid := ok && enabled &&
		(validFrom == nil || !now.Before(*validFrom)) &&
		(validUntil == nil || now.Before(*validUntil))
	if !valid {
		if failed+1 >= gaMaxFailedAttempts {
			_, _ = s.db.Exec(r.Context(),
				`UPDATE guest_accounts SET failed_attempts=0, locked_until=now()+make_interval(mins=>$2), updated_at=now() WHERE id=$1`,
				accountID, gaLockMinutes)
		} else {
			_, _ = s.db.Exec(r.Context(),
				`UPDATE guest_accounts SET failed_attempts=failed_attempts+1, updated_at=now() WHERE id=$1`, accountID)
		}
		genericFail()
		return
	}
	_, _ = s.db.Exec(r.Context(),
		`UPDATE guest_accounts SET failed_attempts=0, locked_until=NULL, last_login_at=now(), login_count=login_count+1, updated_at=now() WHERE id=$1`, accountID)

	// Plan parameters (duration / data cap / shaping) from the bound access plan.
	red := &voucher.Redeemed{}
	if tplID != "" {
		if r2, e := s.vou.LoadTemplate(r.Context(), tplID); e == nil {
			red = r2
		}
	}
	// Atomic licensed-capacity gate + session creation (same as OTP/voucher).
	au, err := s.sess.StartGuestAccount(r.Context(), mac, ip, accountID, displayName, red.DurationSeconds)
	if err != nil {
		if capErr := (*session.CapacityError)(nil); errors.As(err, &capErr) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "LICENSE_CAPACITY_REACHED", "limit": capErr.Limit, "current": capErr.Current,
			})
			return
		}
		slog.Error("session start (account)", "err", err)
		httpErr(w, http.StatusInternalServerError, "session start failed")
		return
	}
	nc := s.resolveNetwork(r.Context(), ip)
	ttl := time.Duration(red.DurationSeconds) * time.Second
	if err := s.nft.Allow(r.Context(), nc.Bridge, ip, ttl); err != nil {
		slog.Error("nft allow", "err", err)
		_ = s.sess.End(context.Background(), ip, "policy")
		httpErr(w, http.StatusInternalServerError, "nft allow failed")
		return
	}
	if err := s.shp.AddSession(r.Context(), nc.Bridge, ip, red.DownKbps, red.UpKbps); err != nil {
		slog.Error("shape add", "err", err)
		_ = s.nft.Deny(context.Background(), nc.Bridge, ip)
		_ = s.sess.End(context.Background(), ip, "policy")
		httpErr(w, http.StatusInternalServerError, "shape add failed")
		return
	}
	s.recordSessionNetwork(r.Context(), au.SessionID, nc)
	s.met.SessionsStarted.WithLabelValues("account").Inc()

	resp := authorizeResp{SessionID: au.SessionID, GuestID: au.GuestID, DurationSeconds: red.DurationSeconds}
	if au.ExpiresAt != nil {
		resp.ExpiresAt = au.ExpiresAt.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}
