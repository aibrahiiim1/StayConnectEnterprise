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

// CheckInstallBinding is the strict check applied when a licence is INSTALLED (AD-15); CheckBinding stays the
// evaluation-time check, so a legacy licence already installed keeps being evaluated and is never bricked by this.
//
//   - a v4 licence that names no appliance at all is refused (it would install on any box);
//   - a field the licence names must be known locally: a blank local value no longer disables the comparison. First
//     activation from a file satisfies this by binding the identity key and the package's appliance id into the local
//     identity before it installs the licence (cmd/scd/offline_first_activation.go);
//   - an appliance id list that does not contain this appliance is refused (it used to be a warning).
//
// Older (v1-v3) unbound licences keep the legacy allowance.
func (d *Document) CheckInstallBinding(local LocalIdentity) (BindingResult, string) {
	if d.IsUnbound() && d.SchemaVersion >= 4 {
		return BindingWrongDevice, "the licence is not bound to any appliance"
	}
	need := func(docValue, localValue, what string) (BindingResult, string, bool) {
		if docValue != "" && strings.TrimSpace(localValue) == "" {
			return BindingWrongDevice, "this appliance's own " + what + " is not known, so the licence cannot be checked against it", true
		}
		return BindingOK, "", false
	}
	if r, why, bad := need(d.IdentityKeyFingerprint, local.IdentityKeyFingerprint, "identity key"); bad {
		return r, why
	}
	if r, why, bad := need(d.ApplianceID, local.ApplianceID, "appliance id"); bad {
		return r, why
	}
	if r, why, bad := need(d.ApplianceSerial, local.Serial, "serial number"); bad {
		return r, why
	}
	if r, why, bad := need(d.HardwareFingerprint, local.HardwareFingerprint, "hardware fingerprint"); bad {
		return r, why
	}
	if local.ApplianceID != "" && len(d.ApplianceIDs) > 0 {
		listed := false
		for _, id := range d.ApplianceIDs {
			if id == local.ApplianceID {
				listed = true
			}
		}
		if !listed {
			return BindingWrongDevice, "this appliance is not listed in the licence"
		}
	}
	return d.CheckBinding(local)
}

// IsUnbound reports a licence that carries no appliance binding field at all.
func (d *Document) IsUnbound() bool {
	return d.IdentityKeyFingerprint == "" && d.ApplianceID == "" && d.ApplianceSerial == "" && d.HardwareFingerprint == ""
}
