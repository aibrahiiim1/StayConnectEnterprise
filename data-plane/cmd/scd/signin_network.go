package main

// A client network can only NARROW what the site offers: it can switch sign-in methods off, lead with one of them, and remember devices for fewer days. Nothing here
// enables a method the licence, the deployment or the site does not already allow. networkOffers is the one place the override is applied, and every sign-in entry
// point asks it; it fails closed, so an unreadable override refuses rather than offering everything.

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

const (
	keyVoucher      = "voucher"
	keyEmailAccount = "email_account"
	keyGuestAccount = "guest_account"
	keySMS          = "sms"
	keyWhatsApp     = "whatsapp"
	keySocial       = "social"
	keyPMS          = "pms"
	keyOpen         = "open"
)

type networkSignin struct {
	disabled map[string]bool
	primary  string
	remember *int
}

type networkCtxKey struct{}

type methodKeyCtxKey struct{}

// withMethodKey narrows an authority method to the one offered-method key the calling route serves (the two account routes share a method).
func withMethodKey(r *http.Request, key string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), methodKeyCtxKey{}, key))
}

func withNetwork(ctx context.Context, networkID string) context.Context {
	return context.WithValue(ctx, networkCtxKey{}, networkID)
}

func networkFrom(ctx context.Context) string {
	v, _ := ctx.Value(networkCtxKey{}).(string)
	return v
}

// networkSigninFor reads the override of one network; a network without one has none.
func (s *server) networkSigninFor(ctx context.Context, networkID string) (networkSignin, error) {
	out := networkSignin{disabled: map[string]bool{}}
	if networkID == "" || s.db == nil {
		return out, nil
	}
	var off []string
	var primary *string
	err := s.db.QueryRow(ctx, `SELECT disabled_methods, primary_method, remember_days FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid AND tenant_id=$2::uuid`,
		networkID, s.tenID).Scan(&off, &primary, &out.remember)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	for _, m := range off {
		out.disabled[m] = true
	}
	// A network whose room sign-in is deliberately NONE does not offer the room method at all, however the site's setting reads.
	var policy string
	if perr := s.db.QueryRow(ctx, `SELECT mode FROM iam_v2.guest_network_pms_policy WHERE guest_network_id=$1::uuid`, networkID).Scan(&policy); perr != nil && !errors.Is(perr, pgx.ErrNoRows) {
		return out, perr
	}
	if policy == "NONE" {
		out.disabled[keyPMS] = true
	}
	if primary != nil {
		out.primary = *primary
	}
	return out, nil
}

// networkOffers reports whether at least one of the methods is offered on the network. Unreadable means not offered.
func (s *server) networkOffers(ctx context.Context, networkID string, keys ...string) bool {
	ov, err := s.networkSigninFor(ctx, networkID)
	if err != nil {
		slog.Error("network sign-in settings unreadable; refusing", "network", networkID, "err", err)
		return false
	}
	for _, k := range keys {
		if !ov.disabled[k] {
			return true
		}
	}
	return len(keys) == 0
}

// networkOffersIP is networkOffers for an address; an address that belongs to no enabled network is judged by the site's settings alone.
func (s *server) networkOffersIP(ctx context.Context, ip net.IP, keys ...string) bool {
	if nc := s.resolveNetwork(ctx, ip); nc.NetworkID != "" {
		return s.networkOffers(ctx, nc.NetworkID, keys...)
	}
	return true
}

// methodKeys are the offered-method keys a given authority method stands for. The account paths narrow it further with the key they serve.
func methodKeys(m iamv2.Method) []string {
	switch m {
	case iamv2.MethodVoucher:
		return []string{keyVoucher}
	case iamv2.MethodAccount:
		return []string{keyEmailAccount, keyGuestAccount}
	case iamv2.MethodOTP:
		return []string{keySMS, keyWhatsApp}
	case iamv2.MethodSocial:
		return []string{keySocial}
	}
	return nil
}

// apply narrows the site's configuration for a listing: methods are removed, the lead method is set only to one that remains, and the remember period never grows.
func (ov networkSignin) apply(cfg *tenantcfg.AuthMethods) {
	if cfg == nil {
		return
	}
	if ov.disabled[keyVoucher] {
		cfg.Voucher = nil
	}
	if ov.disabled[keyEmailAccount] {
		cfg.EmailAccount = nil
	}
	if ov.disabled[keyGuestAccount] {
		cfg.GuestAccount = nil
	}
	if ov.disabled[keySMS] {
		cfg.SMS = nil
	}
	if ov.disabled[keyWhatsApp] {
		cfg.WhatsApp = nil
	}
	if ov.disabled[keySocial] {
		cfg.Social = nil
	}
	if ov.disabled[keyOpen] {
		cfg.Open = nil
	}
	if ov.disabled[keyPMS] && cfg.PMS != nil {
		off := *cfg.PMS
		off.Enabled = false
		cfg.PMS = &off
	}
	if ov.primary != "" && !ov.disabled[ov.primary] {
		if cfg.Portal == nil {
			cfg.Portal = &tenantcfg.PortalConfig{}
		}
		cp := *cfg.Portal
		cp.PrimaryMethod = ov.primary
		cfg.Portal = &cp
	}
	if ov.remember != nil {
		site := cfg.RememberDeviceDays()
		days := *ov.remember
		if days > site {
			days = site
		}
		if cfg.Portal == nil {
			cfg.Portal = &tenantcfg.PortalConfig{}
		}
		cp := *cfg.Portal
		cp.RememberDeviceDays = &days
		cfg.Portal = &cp
	}
}
