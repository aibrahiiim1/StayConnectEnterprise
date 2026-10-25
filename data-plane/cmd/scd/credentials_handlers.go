package main

// Guest username/password authorization: this handler parses and throttles, IAM-v2 owns verification, lockout and the auth context
// (two lockout counters disagreed about the same attempt). Personal accounts are governed by the guest_account switch,
// self-registered Email accounts by email_account and its licence; both sign in through this route.

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/passwordhash"
)

// authorizeGuestAccount: every failure mode (unknown, wrong password, disabled, expired, locked, switch off) is answered identically and creates no auth context, device, entitlement or session.
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
	// Both switches are read before any throttle charge or lookup; with both off the answer says nothing about any credential.
	cfg, ok := s.guestMethodSwitches(r.Context())
	personalOn := ok && methodSwitchOn(cfg, guestMethodAccount)
	emailOn := ok && methodSwitchOn(cfg, guestMethodEmailAccount)
	if !personalOn && !emailOn {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "METHOD_DISABLED"})
		return
	}
	username := strings.TrimSpace(req.Username)

	// IAM-v2 owns credential verification and lockout; a second set of counters charged one attempt twice. The durable throttle is
	// charged before the credential check so a throttled attempt creates nothing and never reveals whether a username exists.
	if s.loginRL != nil && !s.loginRL.allow(username, ip.String(), mac.String()) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "TOO_MANY_ATTEMPTS"})
		return
	}
	if ok, retry := s.throttleGuard(r.Context(), "account", ip, mac, strings.ToLower(username)); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "TOO_MANY_ATTEMPTS"})
		return
	}

	// A switched-off account kind is answered as a wrong password after the same argon2 work: saying "Email accounts are off" would be an enumeration oracle.
	credential := iamv2.CredentialAccount
	kindKnown := false
	if s.db != nil {
		var selfRegistered bool
		err := s.db.QueryRow(r.Context(), `SELECT self_registered FROM iam_v2.guest_access_accounts
		     WHERE tenant_id = $1 AND site_id = $2 AND lower(username) = lower($3)`, s.tenID, s.siteID, username).Scan(&selfRegistered)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			slog.Error("account kind unreadable; refusing sign-in", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "IAMV2_UNAVAILABLE", "authority": "iam_v2"})
			return
		case (selfRegistered && !emailOn) || (!selfRegistered && !personalOn):
			passwordhash.BurnVerify(req.Password)
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "AUTH_DENIED", "reason": "invalid_credentials", "authority": "iam_v2"})
			return
		case selfRegistered:
			credential = iamv2.CredentialEmailAccount
			kindKnown = true
		default:
			kindKnown = true
		}
	}
	// The account kind decides which network setting applies. A network that does not offer this kind answers exactly as a wrong password does, so it cannot be
	// used to learn which kind a username is; a username that exists nowhere is judged by either kind and fails on its credentials.
	if kindKnown {
		key := keyGuestAccount
		if credential == iamv2.CredentialEmailAccount {
			key = keyEmailAccount
		}
		if !s.networkOffersIP(r.Context(), ip, key) {
			passwordhash.BurnVerify(req.Password)
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "AUTH_DENIED", "reason": "invalid_credentials", "authority": "iam_v2"})
			return
		}
		r = withMethodKey(r, key)
	}
	s.authorizeViaIAMv2(w, r, iamv2.MethodAccount,
		iamv2.Request{Username: username, Secret: req.Password, Credential: credential,
			Device: iamv2.DeviceContext{MAC: mac.String()}}, ip)
}
