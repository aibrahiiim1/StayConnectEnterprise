// Package deployment is the DEPLOYMENT CEILING: whether the software deployed on this appliance may run a
// capability at all. It is the first of OneGate's four module gates (ceiling, licence, local enablement,
// readiness) and the only place new product logic learns anything from the historical STAYCONNECT_PHASE*
// environment flags. New code asks the ceiling about a CAPABILITY or a MODULE; it never names a phase.
//
// The ceiling is set by whoever deploys the appliance and changes only with a controlled redeploy. It is not
// a commercial switch (that is the licence) and not a site choice (that is local enablement).
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

// EnvPaymentLiveAllowed is the ONLY switch that lets a LIVE-mode provider account execute. It is not set on
// any appliance. Setting it moves real money and requires a separate Product-Owner authorisation.
const EnvPaymentLiveAllowed = "STAYCONNECT_PAYMENT_LIVE_ALLOWED"

// Capability names what the deployed software can do, independent of phase numbering.
type Capability string

const (
	CapClientPackages     Capability = "client_packages"      // clients choose Internet Packages in the portal
	CapPackageAdmin       Capability = "package_admin"        // the Admin Console edits Internet Packages
	CapHospitality        Capability = "hospitality"          // PMS, Room sign-in, stays (the Hotel section)
	CapPaidAccess         Capability = "paid_access"          // priced packages exist in the commerce engine
	CapCardPayment        Capability = "card_payment"         // the payment domain and a provider adapter may run
	CapCardLive           Capability = "card_payment_live"    // LIVE-mode (real money) provider execution
	CapRoomChargeConfig   Capability = "room_charge_config"   // posting domain: onboarding, mappings, quotes
	CapRoomChargeReview   Capability = "room_charge_review"   // financial review and recovery surfaces
	CapRoomChargeTransmit Capability = "room_charge_transmit" // PS bytes may reach a PMS (real posting)
)

// Ceiling is the evaluated deployment state. The zero value allows nothing optional.
type Ceiling struct {
	commerce    iamv2.CommerceConfig
	pms         iamv2.PMSConfig
	pay         payment.Config
	post        posting.Config
	liveAllowed bool
}

// Getenv reads one variable.
type Getenv func(string) string

// Load evaluates the ceiling from the environment. A malformed or incoherent flag set is a startup failure,
// exactly as it already is for each underlying configuration.
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
	return c, nil
}

// New builds a ceiling from already-loaded configurations (tests and callers that load them anyway).
func New(commerce iamv2.CommerceConfig, pms iamv2.PMSConfig, pay payment.Config, post posting.Config, liveAllowed bool) Ceiling {
	return Ceiling{commerce: commerce, pms: pms, pay: pay, post: post, liveAllowed: liveAllowed && pay.ProviderOn()}
}

// Available reports whether the deployed software may run a capability.
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
	}
	return false
}

// ModuleDeployed reports whether the software for a licensable module is deployed here. Modules with no
// optional code path (identity add-ons, white label, HA) are always deployed.
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
	}
	_, known := lic.LookupModule(id)
	return known
}

// Summary is a log-safe description.
func (c Ceiling) Summary() string {
	parts := []string{}
	for _, cp := range []Capability{CapClientPackages, CapPackageAdmin, CapHospitality, CapPaidAccess, CapCardPayment,
		CapCardLive, CapRoomChargeConfig, CapRoomChargeReview, CapRoomChargeTransmit} {
		parts = append(parts, string(cp)+"="+strconv.FormatBool(c.Available(cp)))
	}
	return "deployment ceiling " + strings.Join(parts, " ")
}
