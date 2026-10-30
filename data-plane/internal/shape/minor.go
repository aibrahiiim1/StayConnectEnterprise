package shape

// Class-id allocation. See docs/reviews/SHAPING_SCALE_BEYOND_2047.md.
//
// A session's HTB class is named by its address alone (no table, no allocator state), so netd, acctd and a restarted
// process all derive the same classid. Within one guest bridge (one subnet) the host index selects the minor:
//
//	host index 0..4095      -> 0x1000 + index        (the original band; identical to the pre-existing scheme for every subnet of /20 or narrower)
//	host index 4096..32767  -> 0x3000 + index - 4096 (the extension band, only for a bridge whose subnet is /19../17)
//	SHARED group aggregates -> 0x2000..0x2fff        (unchanged)
//
// The host index of an address is its offset in the bridge's subnet, read from the bridge's own IPv4 addresses. A bridge with
// no address, or a prefix narrower than /17 or wider than /19, keeps the original rule (the low 12 bits), so nothing already
// deployed changes its class ids.

import (
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	// ExtMinorBase..ExtMinorMax is the band for host indexes 4096 and above (after the SHARED group band).
	ExtMinorBase = 0x3000
	ExtMinorMax  = 0xffff
	// legacyHosts is the number of hosts one /20 band holds.
	legacyHosts = 0x1000
	// WidestPrefix is the widest subnet the extension band can name: /17 = 32768 host indexes.
	WidestPrefix = 17
	// geometryTTL bounds how stale a bridge's address can be when read by another process (netd and acctd read it independently).
	geometryTTL = 5 * time.Second
)

// IsGuestMinor reports whether a class minor belongs to a managed per-session class (either band), as opposed to the
// root's default class, a SHARED group aggregate or somebody else's class.
func IsGuestMinor(minor int) bool {
	return (minor >= GuestMinorBase && minor <= GuestMinorMax) || (minor >= ExtMinorBase && minor <= ExtMinorMax)
}

// minorForHostIndex names the class of the idx-th host of a subnet (idx counted from the network address).
func minorForHostIndex(idx int) (int, bool) {
	switch {
	case idx < 0:
		return 0, false
	case idx < legacyHosts:
		return GuestMinorBase + idx, true
	default:
		m := ExtMinorBase + idx - legacyHosts
		if m > ExtMinorMax {
			return 0, false
		}
		return m, true
	}
}

// MinorInNet is the class minor of ip given the bridge subnet it lives in. A nil subnet, or one outside /17../19, applies the original rule.
func MinorInNet(ip net.IP, n *net.IPNet) (int, bool) {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, false
	}
	if n != nil {
		if ones, bits := n.Mask.Size(); bits == 32 && ones >= WidestPrefix && ones < 20 && n.Contains(ip4) {
			base := n.IP.To4()
			idx := int(uint32(ip4[0])<<24|uint32(ip4[1])<<16|uint32(ip4[2])<<8|uint32(ip4[3])) -
				int(uint32(base[0])<<24|uint32(base[1])<<16|uint32(base[2])<<8|uint32(base[3]))
			return minorForHostIndex(idx)
		}
	}
	return MinorForIP(ip4)
}

// bridgeNets returns the IPv4 subnets configured on a bridge. It is a variable so tests can supply geometry without a kernel.
var bridgeNets = kernelBridgeNets

type geomEntry struct {
	nets []*net.IPNet
	at   time.Time
}

var (
	geomMu    sync.Mutex
	geomCache = map[string]geomEntry{}
)

func kernelBridgeNets(bridge string) []*net.IPNet {
	geomMu.Lock()
	defer geomMu.Unlock()
	if e, ok := geomCache[bridge]; ok && time.Since(e.at) < geometryTTL {
		return e.nets
	}
	var nets []*net.IPNet
	if ifc, err := net.InterfaceByName(bridge); err == nil {
		if addrs, err := ifc.Addrs(); err == nil {
			for _, a := range addrs {
				if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
					nets = append(nets, &net.IPNet{IP: n.IP.To4().Mask(n.Mask), Mask: n.Mask})
				}
			}
		}
	}
	geomCache[bridge] = geomEntry{nets: nets, at: time.Now()}
	return nets
}

// MinorFor is the class minor of ip on the given guest bridge.
func MinorFor(bridge string, ip net.IP) (int, bool) {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, false
	}
	var best *net.IPNet
	for _, n := range bridgeNets(bridge) {
		if !n.Contains(ip4) {
			continue
		}
		// An address inside two configured subnets takes the narrower, as the kernel's routing does.
		if best == nil || ones(n) > ones(best) {
			best = n
		}
	}
	return MinorInNet(ip4, best)
}

// ClassidFor is the "1:minor" class id of ip on the given guest bridge.
func ClassidFor(bridge string, ip net.IP) string {
	m, ok := MinorFor(bridge, ip)
	if !ok {
		return ""
	}
	return classidOf(m)
}

func ones(n *net.IPNet) int { o, _ := n.Mask.Size(); return o }

func classidOf(minor int) string { return RootMajor + ":" + strconv.FormatInt(int64(minor), 16) }
