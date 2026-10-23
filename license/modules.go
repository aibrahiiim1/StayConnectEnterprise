package license

import (
	"fmt"
	"sort"
)

// A grant only permits a module; local enablement and runtime readiness still apply.
const (
	ModuleHospitality = "hospitality"
	ModulePaidAccess  = "paid_access"
	ModuleCardPayment = "card_payment"
	ModuleRoomCharge  = "room_charge"
	ModuleSMSOTP      = "sms_otp"
	// WhatsApp requires its own grant; SMS and legacy grants cannot authorize it.
	ModuleWhatsAppOTP = "whatsapp_otp"
	// This signed ID grants email and password accounts despite its OTP name.
	ModuleEmailOTP    = "email_otp"
	ModuleSocialLogin = "social_login"
	ModuleWhiteLabel  = "white_label"
	ModuleHA          = "ha"
)

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
	{ID: ModuleWhatsAppOTP, Label: "WhatsApp one-time code"},
	{ID: ModuleEmailOTP, Label: "Email accounts"},
	{ID: ModuleSocialLogin, Label: "Social sign-in"},
	{ID: ModuleWhiteLabel, Label: "White label"},
	{ID: ModuleHA, Label: "High availability"},
}

func Registry() []ModuleSpec {
	out := make([]ModuleSpec, len(registry))
	copy(out, registry)
	return out
}

func LookupModule(id string) (ModuleSpec, bool) {
	for _, m := range registry {
		if m.ID == id {
			return m, true
		}
	}
	return ModuleSpec{}, false
}

// A license grant authorizes a module but does not configure it.
type ModuleGrant struct {
	Limits map[string]int `json:"limits,omitempty"`
}

type Modules map[string]ModuleGrant

// Unknown modules are ignored so newer licenses remain readable.
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

// Features mirrors existing signed documents; new modules have no legacy feature authority.
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

// paid_wifi never proves a grant because Central hard-coded it true; financial modules stay ungranted.
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

// Unknown modules and unmet dependencies are dropped so authorization fails closed.
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
