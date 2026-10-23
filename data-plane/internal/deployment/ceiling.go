// Package deployment is the deployment ceiling: whether the deployed software may run a capability at all.
// New code asks about a capability or module, never a phase. It is set by the deployer, not the licence or site.
package deployment

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/payment"
	"github.com/stayconnect/enterprise/data-plane/internal/posting"
	lic "github.com/stayconnect/enterprise/license"
)

// EnvPaymentLiveAllowed is the only switch that lets a LIVE-mode provider account execute (moves real money).
const EnvPaymentLiveAllowed = "STAYCONNECT_PAYMENT_LIVE_ALLOWED"

type Capability string

const (
	CapClientPackages     Capability = "client_packages"
	CapPackageAdmin       Capability = "package_admin"
	CapHospitality        Capability = "hospitality"
	CapPaidAccess         Capability = "paid_access"
	CapCardPayment        Capability = "card_payment"
	CapCardLive           Capability = "card_payment_live" // LIVE-mode (real money) provider execution
	CapRoomChargeConfig   Capability = "room_charge_config"
	CapRoomChargeReview   Capability = "room_charge_review"
	CapRoomChargeTransmit Capability = "room_charge_transmit" // PS bytes may reach a PMS (real posting)
	CapOTPSignIn          Capability = "otp_sign_in"
	CapSocialSignIn       Capability = "social_sign_in"
	CapEmailAccounts      Capability = "email_accounts" // Email accounts are ACCOUNT subjects
)

// Ceiling's zero value allows nothing optional.
type Ceiling struct {
	commerce    iamv2.CommerceConfig
	pms         iamv2.PMSConfig
	pay         payment.Config
	post        posting.Config
	liveAllowed bool
	// otp and social: IAM-v2 accepts that kind of identity; without it the identity modules are not deployed.
	otp, social bool
	// account: IAM-v2 authenticates ACCOUNT subjects. The email sign-in module (email_otp) is deployed exactly
	// when this is set, not when one-time codes are.
	account bool
}

type Getenv func(string) string

// Load fails on a malformed or incoherent flag set.
func Load(get Getenv) (Ceiling, error) {
	if get == nil {
		get = os.Getenv
	}
	var c Ceiling
	var err error
	if c.commerce, err = iamv2.LoadCommerceConfigFromEnv(iamv2.Getenv(get)); err != nil {
		return Ceiling{}, err
	}
	if c.pms, err = iamv2.LoadPMSConfigFromEnv(iamv2.Getenv(get)); err != nil {
		return Ceiling{}, err
	}
	if c.pay, err = payment.LoadConfigFromEnv(payment.Getenv(get)); err != nil {
		return Ceiling{}, err
	}
	if c.post, err = posting.LoadConfigFromEnv(posting.Getenv(get)); err != nil {
		return Ceiling{}, err
	}
	if v := strings.TrimSpace(get(EnvPaymentLiveAllowed)); v != "" {
		b, perr := strconv.ParseBool(v)
		if perr != nil {
			return Ceiling{}, fmt.Errorf("%s: %q is not a boolean", EnvPaymentLiveAllowed, v)
		}
		c.liveAllowed = b
	}
	if c.liveAllowed && !c.pay.ProviderOn() {
		return Ceiling{}, fmt.Errorf("%s set while the payment provider is not deployed", EnvPaymentLiveAllowed)
	}
	flag := func(name string) (bool, error) {
		v := strings.TrimSpace(get(name))
		if v == "" {
			return false, nil
		}
		b, perr := strconv.ParseBool(v)
		if perr != nil {
			return false, fmt.Errorf("%s: %q is not a boolean", name, v)
		}
		return b, nil
	}
	master, err := flag(iamv2.EnvMaster)
	if err != nil {
		return Ceiling{}, err
	}
	if c.otp, err = flag(iamv2.EnvOTP); err != nil {
		return Ceiling{}, err
	}
	if c.social, err = flag(iamv2.EnvSocial); err != nil {
		return Ceiling{}, err
	}
	if c.account, err = flag(iamv2.EnvAccount); err != nil {
		return Ceiling{}, err
	}
	// A production build locks ACCOUNT on regardless of the environment; the ceiling must match the authenticator.
	if iamv2.BuildProfile() == "production" && iamv2.GuestAuthorityLocked(iamv2.MethodAccount) {
		c.account = true
	}
	c.otp, c.social, c.account = c.otp && master, c.social && master, c.account && master
	return c, nil
}

func (c Ceiling) WithIdentity(otp, social bool) Ceiling {
	c.otp, c.social = otp, social
	return c
}

func (c Ceiling) WithAccounts(account bool) Ceiling {
	c.account = account
	return c
}

func New(commerce iamv2.CommerceConfig, pms iamv2.PMSConfig, pay payment.Config, post posting.Config, liveAllowed bool) Ceiling {
	return Ceiling{commerce: commerce, pms: pms, pay: pay, post: post, liveAllowed: liveAllowed && pay.ProviderOn()}
}

func (c Ceiling) Available(cp Capability) bool {
	switch cp {
	case CapClientPackages:
		return c.commerce.PortalOn()
	case CapPackageAdmin:
		return c.commerce.AdminOn()
	case CapHospitality:
		return c.pms.MasterEnabled
	case CapPaidAccess:
		return c.commerce.MasterEnabled
	case CapCardPayment:
		return c.pay.ProviderOn() && c.commerce.PortalOn()
	case CapCardLive:
		return c.liveAllowed && c.pay.ProviderOn()
	case CapRoomChargeConfig:
		return c.post.PostingOn() && c.pms.MasterEnabled
	case CapRoomChargeReview:
		return c.post.ReviewOn()
	case CapRoomChargeTransmit:
		return c.post.TransmitOn() && c.post.PostingOn()
	case CapOTPSignIn:
		return c.otp
	case CapSocialSignIn:
		return c.social
	case CapEmailAccounts:
		return c.account
	}
	return false
}

// ModuleDeployed: identity add-ons need their IAM-v2 method; modules with no optional code path are always deployed.
func (c Ceiling) ModuleDeployed(id string) bool {
	switch id {
	case lic.ModuleHospitality:
		return c.Available(CapHospitality)
	case lic.ModulePaidAccess:
		return c.Available(CapPaidAccess)
	case lic.ModuleCardPayment:
		return c.Available(CapCardPayment)
	case lic.ModuleRoomCharge:
		return c.Available(CapRoomChargeConfig)
	case lic.ModuleEmailOTP:
		return c.Available(CapEmailAccounts)
	case lic.ModuleSMSOTP, lic.ModuleWhatsAppOTP:
		return c.Available(CapOTPSignIn)
	case lic.ModuleSocialLogin:
		return c.Available(CapSocialSignIn)
	}
	_, known := lic.LookupModule(id)
	return known
}

func (c Ceiling) Summary() string {
	parts := []string{}
	for _, cp := range []Capability{CapClientPackages, CapPackageAdmin, CapHospitality, CapPaidAccess, CapCardPayment,
		CapCardLive, CapRoomChargeConfig, CapRoomChargeReview, CapRoomChargeTransmit, CapOTPSignIn, CapSocialSignIn, CapEmailAccounts} {
		parts = append(parts, string(cp)+"="+strconv.FormatBool(c.Available(cp)))
	}
	return "deployment ceiling " + strings.Join(parts, " ")
}
