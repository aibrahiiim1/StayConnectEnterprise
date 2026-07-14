package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/pms"
	"github.com/stayconnect/enterprise/data-plane/internal/pmsguard"
	"github.com/stayconnect/enterprise/data-plane/internal/session"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
	"github.com/stayconnect/enterprise/data-plane/internal/voucher"
)

// ---- /v1/auth/pms/verify ---------------------------------------------------

type pmsVerifyReq struct {
	Room              string `json:"room"`
	FirstName         string `json:"first_name"`
	LastName          string `json:"last_name"`
	ReservationNumber string `json:"reservation_number"`
	IP                string `json:"ip"`
	MAC               string `json:"mac"`
}

func (s *server) pmsVerify(w http.ResponseWriter, r *http.Request) {
	if !s.licenseGate(w, licstate.FeatPMS) {
		return
	}
	var req pmsVerifyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	room := strings.TrimSpace(req.Room)
	if room == "" {
		httpErr(w, http.StatusBadRequest, "room required")
		return
	}
	first := strings.TrimSpace(req.FirstName)
	last := strings.TrimSpace(req.LastName)
	resID := strings.TrimSpace(req.ReservationNumber)
	if first == "" && last == "" && resID == "" {
		httpErr(w, http.StatusBadRequest, "first_name, last_name, or reservation_number required")
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

	cfg, err := tenantcfg.Load(r.Context(), s.db, s.tenID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "tenant config unavailable")
		return
	}
	pcfg := cfg.PMS
	if pcfg == nil || !pcfg.Enabled {
		httpErr(w, http.StatusForbidden, "PMS auth disabled for this tenant")
		return
	}
	if !pms.ValidMode(pcfg.Mode) {
		httpErr(w, http.StatusInternalServerError, "tenant PMS mode invalid: "+pcfg.Mode)
		return
	}

	provider, ok := s.currentPMSReg().Get(pcfg.Provider)
	if !ok {
		httpErr(w, http.StatusInternalServerError, "PMS provider not registered: "+pcfg.Provider)
		return
	}

	guard := pmsguard.Config{
		MaxFailuresPerRoom: pcfg.MaxFailuresPerRoom,
		LockoutWindow:      time.Duration(pcfg.LockoutWindowMinutes) * time.Minute,
	}

	// 1. Per-IP rate limit (cheapest check; rejects scanners early).
	if err := pmsguard.CheckIP(r.Context(), s.db, guard, s.tenID, req.IP); err != nil {
		pmsguard.Record(context.Background(), s.db, s.tenID, s.applID, room, primarySecondaryKind(first, last, resID), req.IP, "ip_rate_limited", false)
		httpErr(w, http.StatusTooManyRequests, err.Error())
		return
	}
	// 2. Per-room lockout.
	if err := pmsguard.CheckRoom(r.Context(), s.db, guard, s.tenID, room); err != nil {
		pmsguard.Record(context.Background(), s.db, s.tenID, s.applID, room, primarySecondaryKind(first, last, resID), req.IP, "locked", false)
		httpErr(w, http.StatusTooManyRequests, err.Error())
		return
	}

	// 3. Provider validation.
	validateStart := time.Now()
	defer func() {
		s.met.PMSValidateDuration.WithLabelValues(pcfg.Provider).Observe(time.Since(validateStart).Seconds())
	}()
	res, err := provider.ValidateGuest(r.Context(), pms.Query{
		RoomNumber:        room,
		FirstName:         first,
		LastName:          last,
		ReservationNumber: resID,
		Mode:              pms.Mode(pcfg.Mode),
	})
	if err != nil {
		errCode := "not_found"
		switch {
		case errors.Is(err, pms.ErrUpstreamFail):
			errCode = "upstream_fail"
		case errors.Is(err, pms.ErrCheckedOut):
			errCode = "checked_out"
		}
		pmsguard.Record(context.Background(), s.db, s.tenID, s.applID, room, primarySecondaryKind(first, last, resID), req.IP, errCode, false)
		s.met.PMSValidate.WithLabelValues(pcfg.Provider, errCode).Inc()
		httpErr(w, http.StatusUnauthorized, "verification failed")
		return
	}

	// 4. Stay-window check: apply per-tenant grace + min-remaining policy.
	stay := provider.Config().StayPolicy
	now := time.Now()
	effIn, effOut := pms.EffectiveWindow(stay, res.CheckIn, res.CheckOut)
	if !res.CheckIn.IsZero() && now.Before(effIn) {
		pmsguard.Record(context.Background(), s.db, s.tenID, s.applID, room, primarySecondaryKind(first, last, resID), req.IP, "before_checkin", false)
		httpErr(w, http.StatusForbidden, "stay has not started yet")
		return
	}
	if !res.CheckOut.IsZero() && !now.Before(effOut) {
		pmsguard.Record(context.Background(), s.db, s.tenID, s.applID, room, primarySecondaryKind(first, last, resID), req.IP, "after_checkout", false)
		httpErr(w, http.StatusForbidden, "stay has ended")
		return
	}
	if !pms.MinRemainingOK(stay, effOut, now) {
		httpErr(w, http.StatusForbidden, "less than a minute of stay remaining")
		return
	}

	// 5. Resolve template parameters and cap session duration to remaining stay.
	red := &voucher.Redeemed{}
	if pcfg.TemplateID != "" {
		if r2, err := s.vou.LoadTemplate(r.Context(), pcfg.TemplateID); err == nil {
			red = r2
		}
	}
	if !effOut.IsZero() {
		remaining := int(time.Until(effOut).Seconds())
		if red.DurationSeconds <= 0 || remaining < red.DurationSeconds {
			red.DurationSeconds = remaining
		}
	}

	// 6+7. Atomic licensed-capacity gate + session creation, then data plane
	// (see main.go authorize for the ordering rationale).
	au, err := s.sess.StartPMS(r.Context(), mac, ip, res.RoomNumber, res.GuestName, res.ReservationID, red.DurationSeconds)
	if err != nil {
		if capErr := (*session.CapacityError)(nil); errors.As(err, &capErr) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "LICENSE_CAPACITY_REACHED", "limit": capErr.Limit, "current": capErr.Current,
			})
			return
		}
		slog.Error("session start (pms)", "err", err)
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
	s.met.SessionsStarted.WithLabelValues("pms").Inc()
	s.met.PMSValidate.WithLabelValues(pcfg.Provider, "ok").Inc()

	pmsguard.Record(context.Background(), s.db, s.tenID, s.applID, room, primarySecondaryKind(first, last, resID), req.IP, "", true)

	resp := authorizeResp{
		SessionID:       au.SessionID,
		GuestID:         au.GuestID,
		DurationSeconds: red.DurationSeconds,
	}
	if au.ExpiresAt != nil {
		resp.ExpiresAt = au.ExpiresAt.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

// primarySecondaryKind labels which secondary field the user actually filled
// (used for analytics in pms_attempts).
func primarySecondaryKind(first, last, res string) string {
	switch {
	case res != "":
		return "reservation"
	case last != "":
		return "last_name"
	case first != "":
		return "first_name"
	}
	return "either"
}
