// Package hwid derives the appliance serial and permanent NIC MACs as a pure function of the hardware, so
// they survive reset and de-enrollment.
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

type Info struct {
	Serial       string `json:"serial"`
	Fingerprint  string `json:"hardware_fingerprint"`
	WANInterface string `json:"wan_interface"`
	WANMAC       string `json:"wan_mac"`
	LANInterface string `json:"lan_interface"`
	LANMAC       string `json:"lan_mac"`
	Hostname     string `json:"hostname"`
	Model        string `json:"model"`
	AnchorSource string `json:"anchor_source"`
}

var macRe = regexp.MustCompile(`([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}`)

// Crockford base32: no I, L, O, U.
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
	if ifc := firstPhysicalIface(); ifc != "" {
		if mac := permMAC(ifc); mac != "" {
			return mac, "nic-perm-mac"
		}
	}
	return "stayconnect-unknown-anchor", "none"
}

func Serial() string {
	anchor, _ := hardwareAnchor()
	sum := sha256.Sum256([]byte("stayconnect-serial-v1:" + anchor))
	s := encodeCrockford(sum[:], 12)
	return "SC-" + s[0:4] + "-" + s[4:8] + "-" + s[8:12]
}

// Fingerprint covers DMI identity fields only, never NIC MACs, so a WAN NIC replacement does not change it.
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

// permMAC prefers the NIC firmware's permanent address over randomized/overridden ones.
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
