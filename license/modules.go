package license

import (
	"fmt"
	"sort"
)

// Module ids are the stable commercial capabilities a licence may authorise.
//
// The registry below is the ONE definition shared by Central (which issues
// licences) and the appliance (which enforces them). A module is an
// authorisation ceiling only: a licensed module still has to be enabled
// locally by the site and be ready at runtime before anything executes.
//
// Site Type never authorises a module. Nothing in this file reads it.
const (
	ModuleHospitality = "hospitality"  // Hotel section: PMS, Room sign-in, stays, grace, post-stay
	ModulePaidAccess  = "paid_access"  // priced Internet Packages
	ModuleCardPayment = "card_payment" // Card payment through a provider-hosted page
	ModuleRoomCharge  = "room_charge"  // PMS Room Charge (financial posting)
	ModuleSMSOTP      = "sms_otp"
	ModuleEmailOTP    = "email_otp"
	ModuleSocialLogin = "social_login"
	ModuleWhiteLabel  = "white_label"
	ModuleHA          = "ha"
)

// ModuleSpec describes one registered module.
type ModuleSpec struct {
	ID       string
	Label    string
	Requires []string
}

var registry = []ModuleSpec{
	{ID: ModuleHospitality, Label: "Hotel (PMS, Room sign-in, stays)"},
	{ID: ModulePaidAccess, Label: "Paid access (priced Internet Packages)"},
	{ID: ModuleCardPayment, Label: "Card payment", Requires: []string{ModulePaidAccess}},
	{ID: ModuleRoomCharge, Label: "Room charge (PMS posting)", Requires: []string{ModuleHospitality, ModulePaidAccess}},
	{ID: ModuleSMSOTP, Label: "SMS one-time code"},
	{ID: ModuleEmailOTP, Label: "Email one-time code"},
	{ID: ModuleSocialLogin, Label: "Social sign-in"},
	{ID: ModuleWhiteLabel, Label: "White label"},
	{ID: ModuleHA, Label: "High availability"},
}

// Registry returns a copy of the module registry in stable order.
func Registry() []ModuleSpec {
	out := make([]ModuleSpec, len(registry))
	copy(out, registry)
	return out
}

// LookupModule returns the spec for a registered id.
func LookupModule(id string) (ModuleSpec, bool) {
	for _, m := range registry {
		if m.ID == id {
			return m, true
		}
	}
	return ModuleSpec{}, false
}

// ModuleGrant is the per-module payload of a v4 licence. It is deliberately
// small: a licence authorises, it never configures.
type ModuleGrant struct {
	Limits map[string]int `json:"limits,omitempty"`
}

// Modules is the v4 authorisation map: module id → grant.
type Modules map[string]ModuleGrant

// CheckDependencies reports the first registered module whose dependency is
// missing. Unknown ids are ignored (forward compatibility).
func CheckDependencies(ids []string) error {
	have := map[string]bool{}
	for _, id := range ids {
		have[id] = true
	}
	for _, id := range sortedKeys(have) {
		spec, ok := LookupModule(id)
		if !ok {
			continue
		}
		for _, dep := range spec.Requires {
			if !have[dep] {
				return fmt.Errorf("module %q requires %q", id, dep)
			}
		}
	}
	return nil
}

// ModulesFromIDs builds a Modules map from a list of ids, rejecting
// unregistered ids and unmet dependencies. Central uses it at issue time.
func ModulesFromIDs(ids []string) (Modules, error) {
	m := Modules{}
	for _, id := range ids {
		if _, ok := LookupModule(id); !ok {
			return nil, fmt.Errorf("unknown module %q", id)
		}
		m[id] = ModuleGrant{}
	}
	if err := CheckDependencies(ids); err != nil {
		return nil, err
	}
	return m, nil
}

// ProjectFeatures is the ONLY way legacy Features are produced from a v4
// licence. Features is a compatibility projection, never an authority.
func ProjectFeatures(m Modules) Features {
	_, pms := m[ModuleHospitality]
	_, paid := m[ModulePaidAccess]
	_, sms := m[ModuleSMSOTP]
	_, email := m[ModuleEmailOTP]
	_, social := m[ModuleSocialLogin]
	_, ha := m[ModuleHA]
	_, wl := m[ModuleWhiteLabel]
	return Features{PMS: pms, PaidWiFi: paid, SMSOTP: sms, EmailOTP: email, SocialLogin: social, HA: ha, WhiteLabel: wl}
}

// legacyModules maps a pre-v4 Features block onto modules, conservatively.
// paid_wifi is NOT mapped: Central hard-coded it to true, so it never
// recorded a commercial grant. Financial modules are never derived.
func legacyModules(f Features) Modules {
	m := Modules{}
	if f.PMS {
		m[ModuleHospitality] = ModuleGrant{}
	}
	if f.SMSOTP {
		m[ModuleSMSOTP] = ModuleGrant{}
	}
	if f.EmailOTP {
		m[ModuleEmailOTP] = ModuleGrant{}
	}
	if f.SocialLogin {
		m[ModuleSocialLogin] = ModuleGrant{}
	}
	if f.HA {
		m[ModuleHA] = ModuleGrant{}
	}
	if f.WhiteLabel {
		m[ModuleWhiteLabel] = ModuleGrant{}
	}
	return m
}

// AuthorizedModules returns the registered module ids this document
// authorises, with every dependency satisfied. v4 reads `modules` only;
// older schemas use the conservative legacy mapping. Unknown ids and modules
// with unmet dependencies are dropped (fail closed).
func (d *Document) AuthorizedModules() map[string]bool {
	var src Modules
	if d.SchemaVersion >= 4 {
		src = d.Modules
	} else {
		src = legacyModules(d.Features)
	}
	out := map[string]bool{}
	for id := range src {
		if _, ok := LookupModule(id); ok {
			out[id] = true
		}
	}
	// Drop anything with an unmet dependency until stable.
	for changed := true; changed; {
		changed = false
		for id := range out {
			spec, _ := LookupModule(id)
			for _, dep := range spec.Requires {
				if !out[dep] {
					delete(out, id)
					changed = true
				}
			}
		}
	}
	return out
}

// ModuleIDs returns the sorted ids of a Modules map.
func (m Modules) ModuleIDs() []string {
	set := map[string]bool{}
	for id := range m {
		set[id] = true
	}
	return sortedKeys(set)
}

func sortedKeys(s map[string]bool) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
