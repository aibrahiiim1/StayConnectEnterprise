package shape

import "net"

// SharedFloorKbpsForTest exports the shared guaranteed-rate rule so the applier's tests need not duplicate it.
func SharedFloorKbpsForTest(groupKbps int) int { return sharedFloorKbps(groupKbps) }

// SetBridgeNetsForTest makes MinorFor see the given subnets (CIDR strings) on the named bridges instead of reading the kernel's interface
// addresses, so a harness over a simulated kernel can exercise a bridge wider than /20. The returned function restores the previous source.
func SetBridgeNetsForTest(m map[string][]string) (restore func()) {
	old := bridgeNets
	bridgeNets = func(b string) []*net.IPNet {
		var out []*net.IPNet
		for _, c := range m[b] {
			if _, n, err := net.ParseCIDR(c); err == nil {
				out = append(out, n)
			}
		}
		return out
	}
	return func() { bridgeNets = old }
}
