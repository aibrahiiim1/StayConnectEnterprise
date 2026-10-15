package main

// Guest Username/Password authorization. IAM-v2 is the authority: this handler parses and throttles, and
// hands the credential to the IAM-v2 adapter, which owns verification, lockout and the auth context.
//
// The superseded implementation that used to live here -- a direct public.guest_accounts lookup with its own
// argon2 verification, its own failed_attempts/locked_until counters and its own public.sessions pipeline --
// has been REMOVED, along with the local argon2 helpers that existed only to serve it. Password verification
// belongs to exactly one authority; two of them meant two lockout counters disagreeing about the same
// attempt.
//
// TWO KINDS OF ACCOUNT SIGN IN HERE. Staff-created Personal accounts are governed by the guest_account switch;
// self-registered Email accounts (self_service.go) by the email_account switch and its licence. Both are the
// same ACCOUNT subject to IAM-v2 and sign in through this one route, so a self-registered account's reply also
// carries its verification state and the account token.

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

// authorizeGuestAccount hands a username/password to the IAM-v2 authority. Every failure mode -- unknown
// username, wrong password, disabled, expired, locked out, or an account of a kind whose switch is off -- is
// answered identically and creates no auth context, device, entitlement or session.
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
	// Both switches are read before any throttle charge or lookup. With both off the method is off, and the
	// answer says nothing about any credential -- exactly the previous behaviour of the one switch.
	cfg, ok := s.guestMethodSwitches(r.Context())
	personalOn := ok && methodSwitchOn(cfg, guestMethodAccount)
	emailOn := ok && methodSwitchOn(cfg, guestMethodEmailAccount)
	if !personalOn && !emailOn {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "METHOD_DISABLED"})
		return
	}
	username := strings.TrimSpace(req.Username)

	// IAM-v2 IS THE GUEST AUTHORITY for account credentials. The superseded public.guest_accounts lookup,
	// its argon2 verification, its own lockout counters and the public.sessions pipeline that followed have
	// been REMOVED from the product. IAM-v2's adapter owns credential verification and account lockout
	// (failed_attempts / locked_until on iam_v2.guest_access_accounts); running a second set of counters
	// alongside it charged one attempt twice, which is why only one may exist.
	//
	// The durable endpoint throttle stays, and now applies to the single remaining path. It is charged before
	// the credential check so a throttled attempt establishes no auth context, device or entitlement, and it
	// never reveals whether a username exists.
	if s.loginRL != nil && !s.loginRL.allow(username, ip.String(), mac.String()) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "TOO_MANY_ATTEMPTS"})
		return
	}
	if ok, retry := s.throttleGuard(r.Context(), "account", ip, mac, strings.ToLower(username)); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "TOO_MANY_ATTEMPTS"})
		return
	}

	// WHICH SWITCH GOVERNS THIS ACCOUNT. Learnt from the account itself, and a switched-off kind is answered as
	// a wrong password, after the same argon2 work: "this address has an Email account but Email accounts are
	// off" would be an enumeration oracle the portal never needs. An unknown username goes on to the
	// authenticator, which does its own constant-work refusal.
	credential := iamv2.CredentialAccount
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
		}
	}
	s.authorizeViaIAMv2(w, r, iamv2.MethodAccount,
		iamv2.Request{Username: username, Secret: req.Password, Credential: credential,
			Device: iamv2.DeviceContext{MAC: mac.String()}}, ip)
}
