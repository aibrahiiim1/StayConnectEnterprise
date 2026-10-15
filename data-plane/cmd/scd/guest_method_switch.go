package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

// HOTEL ADMIN'S SIGN-IN METHOD SWITCHES ARE ENFORCED HERE, NOT ONLY IN THE PORTAL.
//
// The switches (tenants.auth_methods) used to decide only which tabs the portal drew. A method the hotel had
// turned off was hidden but still worked for anyone who posted its form directly -- a guest account signed
// in while Hotel Admin showed accounts disabled. The authoritative answer now comes from the same record, read
// the same way the portal reads it, at the moment a guest tries to sign in.
const (
	guestMethodVoucher = "voucher"
	// guestMethodAccount is staff-created Personal accounts (auth_methods.guest_account).
	guestMethodAccount = "account"
	// guestMethodEmailAccount is self-registered Email accounts (auth_methods.email_account): registration,
	// their sign-in, grace, resend and forgot-password (self_service_handlers.go).
	guestMethodEmailAccount = "email_account"
	guestMethodPMS          = "pms"
)

// methodSwitchOn is the decision itself: absent means off, exactly as the portal's tab logic treats it.
func methodSwitchOn(cfg *tenantcfg.AuthMethods, method string) bool {
	if cfg == nil {
		return false
	}
	switch method {
	case guestMethodVoucher:
		return cfg.Voucher != nil && cfg.Voucher.Enabled
	case guestMethodAccount:
		return cfg.GuestAccount != nil && cfg.GuestAccount.Enabled
	case guestMethodEmailAccount:
		return cfg.EmailAccount != nil && cfg.EmailAccount.Enabled
	case guestMethodPMS:
		return cfg.PMS != nil && cfg.PMS.Enabled
	}
	return false
}

// guestMethodSwitches reads the switches with the licence applied, as the portal's listing does. ok=false when
// the record is unreadable -- callers FAIL CLOSED: the portal cannot draw any tab from it either, and a sign-in
// method the hotel may have disabled is not something to guess about.
func (s *server) guestMethodSwitches(ctx context.Context) (*tenantcfg.AuthMethods, bool) {
	load := s.methodSwitches
	if load == nil {
		load = func(ctx context.Context) (*tenantcfg.AuthMethods, error) { return tenantcfg.Load(ctx, s.db, s.tenID) }
	}
	cfg, err := load(ctx)
	if err != nil {
		slog.Error("guest method switch unreadable; refusing sign-in", "err", err)
		return nil, false
	}
	s.applyLicenseToMethods(cfg)
	return cfg, true
}

// guestMethodEnabled answers one switch (unreadable = off).
func (s *server) guestMethodEnabled(ctx context.Context, method string) bool {
	cfg, ok := s.guestMethodSwitches(ctx)
	return ok && methodSwitchOn(cfg, method)
}

// refuseDisabledMethod answers a sign-in for a switched-off method. It is checked before any throttle charge
// or credential lookup, so a disabled method reveals nothing about any credential.
func (s *server) refuseDisabledMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if s.guestMethodEnabled(r.Context(), method) {
		return false
	}
	writeJSON(w, http.StatusForbidden, map[string]any{"error": "METHOD_DISABLED"})
	return true
}
