package license

import "strings"

// LocalIdentity is what an appliance knows about itself when checking whether a
// signed license was actually minted for THIS box. The identity key fingerprint
// is the primary cryptographic anchor; serial / hardware fingerprint / WAN MAC
// are hardware-mismatch and clone-detection signals.
type LocalIdentity struct {
	ApplianceID            string
	IdentityKeyFingerprint string
	Serial                 string
	HardwareFingerprint    string
	WANMAC                 string
}

// BindingResult classifies how a document's binding matches the local box.
type BindingResult int

const (
	// BindingOK — the license is bound to this appliance (or is an unbound
	// legacy/site-wide v1 document).
	BindingOK BindingResult = iota
	// BindingWrongDevice — a hard identity/appliance/serial/hardware mismatch.
	// The license was minted for a different device (or this is a clone); it
	// MUST be rejected and never activated.
	BindingWrongDevice
	// BindingWANMismatch — everything matches except the WAN MAC. This is a
	// genuine hardware-binding signal (NIC replaced / VM migration). It is NOT
	// a hard reject: the appliance enters a mismatch grace state and raises an
	// alert until an authorized rebind issues a corrected license.
	BindingWANMismatch
)

func normMAC(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.ReplaceAll(s, "-", ":")
}

// CheckBinding compares a document's v2 hardware/identity binding to the local
// identity. A document with no binding fields (v1 / site-wide) returns
// BindingOK so older licenses keep working across the upgrade.
func (d *Document) CheckBinding(local LocalIdentity) (BindingResult, string) {
	if d.IdentityKeyFingerprint == "" && d.ApplianceID == "" && d.ApplianceSerial == "" && d.HardwareFingerprint == "" {
		return BindingOK, "" // unbound legacy/site-wide document
	}
	// Primary trust anchor: the cryptographic identity key.
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
	// WAN MAC last: mismatch is a soft signal, not a hard reject.
	if d.WANMAC != "" && local.WANMAC != "" && normMAC(d.WANMAC) != normMAC(local.WANMAC) {
		return BindingWANMismatch, "WAN MAC mismatch"
	}
	return BindingOK, ""
}
