package license

import "strings"

// The identity key anchors trust; hardware fields help detect clones and replacements.
type LocalIdentity struct {
	ApplianceID            string
	IdentityKeyFingerprint string
	Serial                 string
	HardwareFingerprint    string
	WANMAC                 string
}

type BindingResult int

const (
	BindingOK BindingResult = iota
	// Identity or hardware mismatches must prevent activation.
	BindingWrongDevice
	// A WAN MAC change gets grace and an alert so NIC replacement does not hard-reject the license.
	BindingWANMismatch
)

func normMAC(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.ReplaceAll(s, "-", ":")
}

// Unbound legacy licenses remain valid across upgrades.
func (d *Document) CheckBinding(local LocalIdentity) (BindingResult, string) {
	if d.IdentityKeyFingerprint == "" && d.ApplianceID == "" && d.ApplianceSerial == "" && d.HardwareFingerprint == "" {
		return BindingOK, ""
	}
	if d.IdentityKeyFingerprint != "" && local.IdentityKeyFingerprint != "" &&
		!strings.EqualFold(d.IdentityKeyFingerprint, local.IdentityKeyFingerprint) {
		return BindingWrongDevice, "identity key fingerprint mismatch"
	}
	if d.ApplianceID != "" && local.ApplianceID != "" && d.ApplianceID != local.ApplianceID {
		return BindingWrongDevice, "appliance id mismatch"
	}
	if d.ApplianceSerial != "" && local.Serial != "" && !strings.EqualFold(d.ApplianceSerial, local.Serial) {
		return BindingWrongDevice, "serial mismatch"
	}
	if d.HardwareFingerprint != "" && local.HardwareFingerprint != "" &&
		!strings.EqualFold(d.HardwareFingerprint, local.HardwareFingerprint) {
		return BindingWrongDevice, "hardware fingerprint mismatch"
	}
	if d.WANMAC != "" && local.WANMAC != "" && normMAC(d.WANMAC) != normMAC(local.WANMAC) {
		return BindingWANMismatch, "WAN MAC mismatch"
	}
	return BindingOK, ""
}
