package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

// Hotel Admin's sign-in method switches (tenants.auth_methods) are enforced here, not only in the portal: a method turned off was hidden but still worked for a directly posted form.
const (
	guestMethodVoucher = "voucher"
	guestMethodAccount = "account"
	// guestMethodEmailAccount covers self-registered Email accounts: registration, sign-in, grace, resend and forgot-password.
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

// guestMethodSwitches reads the switches with the licence applied; ok=false when unreadable, and callers FAIL CLOSED.
func (s *server) guestMethodSwitches(ctx context.Context) (*tenantcfg.AuthMethods, bool) {
	load := s.methodSwitches
	if load == nil {
		// Cached for settingsCacheTTL and cleared by the database's notification when the switches change (AD-52). A
		// test that supplies its own loader is never cached.
		load = func(ctx context.Context) (*tenantcfg.AuthMethods, error) {
			v, err := s.settings.get("tenant-auth-methods", func() (any, error) { return tenantcfg.Load(ctx, s.db, s.tenID) })
			if err != nil {
				return nil, err
			}
			// A copy: the licence is applied to it below and must not leak into the cached value.
			c := *(v.(*tenantcfg.AuthMethods))
			return &c, nil
		}
	}
	cfg, err := load(ctx)
	if err != nil {
		slog.Error("guest method switch unreadable; refusing sign-in", "err", err)
		return nil, false
	}
	s.applyLicenseToMethods(cfg)
	return cfg, true
}

func (s *server) guestMethodEnabled(ctx context.Context, method string) bool {
	cfg, ok := s.guestMethodSwitches(ctx)
	return ok && methodSwitchOn(cfg, method)
}

// refuseDisabledMethod is checked before any throttle charge or credential lookup, so a disabled method reveals nothing about any credential.
func (s *server) refuseDisabledMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if s.guestMethodEnabled(r.Context(), method) {
		return false
	}
	writeJSON(w, http.StatusForbidden, map[string]any{"error": "METHOD_DISABLED"})
	return true
}
