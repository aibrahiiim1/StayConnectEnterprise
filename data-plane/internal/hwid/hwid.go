// Package hwid derives this appliance's STABLE hardware identity — the
// StayConnect serial number and the permanent WAN/LAN interface MAC addresses —
// from real device sources so the values survive reboot, de-enrollment and a
// full factory reset (they are re-derived deterministically from the hardware,
// never from a random database row).
//
// StayConnect serial: SC-XXXX-XXXX-XXXX, a Crockford-base32 encoding of
// sha256("stayconnect-serial-v1:" + hardwareAnchor). The anchor is the DMI
// product UUID (stable per physical/virtual machine), falling back through the
// DMI product/board serial, /etc/machine-id, and finally the first physical
// NIC's permanent MAC. Because it is a pure function of the hardware, the same
// box always yields the same serial — "generated once" in effect, and unchanged
// by anything we wipe on the filesystem.
package hwid

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Info is the appliance's detected hardware identity. Appliance ID is filled in
// by the caller from the cryptographic identity; everything else is hardware.
type Info struct {
	Serial      string `json:"serial"`
	Fingerprint string `json:"hardware_fingerprint"`
	WANInterface string `json:"wan_interface"`
	WANMAC       string `json:"wan_mac"`
	LANInterface string `json:"lan_interface"`
	LANMAC       string `json:"lan_mac"`
	Hostname     string `json:"hostname"`
	Model        string `json:"model"`
	AnchorSource string `json:"anchor_source"`
}

var macRe = regexp.MustCompile(`([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}`)

// crockford base32 alphabet — no I, L, O, U to avoid visual/scan ambiguity.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func encodeCrockford(b []byte, n int) string {
	var sb strings.Builder
	var buf, bits uint32
	for _, c := range b {
		buf = (buf << 8) | uint32(c)
		bits += 8
		for bits >= 5 {
			bits -= 5
			sb.WriteByte(crockford[(buf>>bits)&0x1f])
			if sb.Len() >= n {
				return sb.String()
			}
		}
	}
	for sb.Len() < n {
		sb.WriteByte('0')
	}
	return sb.String()[:n]
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func isBadDMI(v string) bool {
	if v == "" {
		return true
	}
	l := strings.ToLower(v)
	for _, bad := range []string{"none", "not specified", "to be filled by o.e.m.", "default string", "system serial number", "0", "00000000"} {
		if l == bad {
			return true
		}
	}
	return false
}

// hardwareAnchor returns a stable string uniquely identifying this machine and
// the source it came from. Deterministic across reboots and factory resets.
func hardwareAnchor() (anchor, source string) {
	for _, p := range []string{
		"/sys/class/dmi/id/product_uuid",
		"/sys/class/dmi/id/product_serial",
		"/sys/class/dmi/id/board_serial",
		"/sys/class/dmi/id/chassis_serial",
	} {
		if v := readTrim(p); !isBadDMI(v) {
			return v, filepath.Base(p)
		}
	}
	if v := readTrim("/etc/machine-id"); v != "" {
		return v, "machine-id"
	}
	// Last resort: the first physical NIC's permanent MAC (stable per hardware).
	if ifc := firstPhysicalIface(); ifc != "" {
		if mac := permMAC(ifc); mac != "" {
			return mac, "nic-perm-mac"
		}
	}
	return "stayconnect-unknown-anchor", "none"
}

// Serial derives the stable SC-XXXX-XXXX-XXXX serial from the hardware anchor.
func Serial() string {
	anchor, _ := hardwareAnchor()
	sum := sha256.Sum256([]byte("stayconnect-serial-v1:" + anchor))
	s := encodeCrockford(sum[:], 12)
	return "SC-" + s[0:4] + "-" + s[4:8] + "-" + s[8:12]
}

// Fingerprint is a stable hardware fingerprint (hex) over the machine's DMI
// identity fields ONLY — deliberately excluding NIC MACs so a legitimate WAN
// NIC replacement changes the WAN MAC (a soft rebind signal) without changing
// the fingerprint (which still proves "same machine"). A clone onto different
// hardware yields a different product UUID and therefore a different fingerprint.
func Fingerprint() string {
	var parts []string
	for _, p := range []string{"product_uuid", "product_serial", "board_serial", "board_asset_tag", "product_name", "sys_vendor", "chassis_serial"} {
		if v := readTrim("/sys/class/dmi/id/" + p); !isBadDMI(v) {
			parts = append(parts, p+"="+v)
		}
	}
	if len(parts) == 0 {
		if v := readTrim("/etc/machine-id"); v != "" {
			parts = append(parts, "machine-id="+v)
		}
	}
	sum := sha256.Sum256([]byte("stayconnect-hwfp-v1:" + strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:16])
}

// NormalizeMAC lower-cases and colon-normalizes a MAC for storage/comparison.
func NormalizeMAC(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", ":")
	if !strings.Contains(s, ":") && len(s) == 12 {
		var p []string
		for i := 0; i < 12; i += 2 {
			p = append(p, s[i:i+2])
		}
		s = strings.Join(p, ":")
	}
	return s
}

// permMAC returns the PERMANENT physical MAC of iface (via ethtool -P), falling
// back to the current /sys address. Randomized/overridden MACs are avoided by
// preferring the permanent address the NIC firmware reports.
func permMAC(iface string) string {
	if iface == "" {
		return ""
	}
	if out, err := exec.Command("/usr/sbin/ethtool", "-P", iface).Output(); err == nil {
		if m := macRe.FindString(string(out)); m != "" && m != "00:00:00:00:00:00" {
			return NormalizeMAC(m)
		}
	}
	if v := readTrim("/sys/class/net/" + iface + "/address"); v != "" {
		return NormalizeMAC(v)
	}
	return ""
}

// isPhysical reports whether iface is a real NIC (has a device symlink, is not a
// bridge/vlan/veth/loopback).
func isPhysical(iface string) bool {
	if iface == "" || iface == "lo" || strings.Contains(iface, ".") {
		return false
	}
	if _, err := os.Stat("/sys/class/net/" + iface + "/device"); err != nil {
		return false
	}
	return true
}

func firstPhysicalIface() string {
	ents, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range ents {
		if isPhysical(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

// wanInterface picks the WAN NIC: explicit env override, else the interface of
// the default route, else the first physical NIC.
func wanInterface() string {
	for _, k := range []string{"HWID_WAN_INTERFACE", "NETD_WAN_INTERFACE", "SCD_WAN_INTERFACE"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	if out, err := exec.Command("/usr/sbin/ip", "route", "show", "default").Output(); err == nil {
		f := strings.Fields(string(out))
		for i := 0; i < len(f)-1; i++ {
			if f[i] == "dev" {
				return f[i+1]
			}
		}
	}
	return firstPhysicalIface()
}

// lanInterface picks the LAN NIC: env override, else the physical member of the
// legacy LAN bridge (br-lan), else the second physical NIC.
func lanInterface(wan string) string {
	for _, k := range []string{"HWID_LAN_INTERFACE", "NETD_LAN_MEMBER", "SCD_LAN_INTERFACE"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	br := strings.TrimSpace(os.Getenv("NETD_LEGACY_BRIDGE"))
	if br == "" {
		br = "br-lan"
	}
	if ents, err := os.ReadDir("/sys/class/net/" + br + "/brif"); err == nil {
		var members []string
		for _, e := range ents {
			if isPhysical(e.Name()) {
				members = append(members, e.Name())
			}
		}
		sort.Strings(members)
		if len(members) > 0 {
			return members[0]
		}
	}
	// else the first physical NIC that isn't WAN
	ents, err := os.ReadDir("/sys/class/net")
	if err == nil {
		var names []string
		for _, e := range ents {
			if isPhysical(e.Name()) && e.Name() != wan {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		if len(names) > 0 {
			return names[0]
		}
	}
	return ""
}

func model() string {
	vendor := readTrim("/sys/class/dmi/id/sys_vendor")
	product := readTrim("/sys/class/dmi/id/product_name")
	parts := []string{}
	if !isBadDMI(vendor) {
		parts = append(parts, vendor)
	}
	if !isBadDMI(product) {
		parts = append(parts, product)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// Detect gathers the full hardware identity for this appliance.
func Detect() Info {
	anchor, src := hardwareAnchor()
	_ = anchor
	wan := wanInterface()
	lan := lanInterface(wan)
	host, _ := os.Hostname()
	return Info{
		Serial:       Serial(),
		Fingerprint:  Fingerprint(),
		WANInterface: wan,
		WANMAC:       permMAC(wan),
		LANInterface: lan,
		LANMAC:       permMAC(lan),
		Hostname:     host,
		Model:        model(),
		AnchorSource: src,
	}
}
