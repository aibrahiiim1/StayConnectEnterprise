package main

import (
	"bufio"
	"net"
	"os"
	"strings"
)

type arpLookup func(ip net.IP) (net.HardwareAddr, bool)

// defaultArp reads /proc/net/arp to map an IP to a MAC. On a captive-portal
// gateway the client is on the LAN, so the ARP cache has the entry as long as
// the kernel has seen a packet from it (which DHCP and any TCP SYN ensure).
func defaultArp(ip net.IP) (net.HardwareAddr, bool) {
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return nil, false
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	_ = s.Scan() // header
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 4 {
			continue
		}
		// IP address, HW type, Flags, HW address, Mask, Device
		if fields[0] == ip.String() && fields[3] != "00:00:00:00:00:00" {
			if mac, err := net.ParseMAC(fields[3]); err == nil {
				return mac, true
			}
		}
	}
	return nil, false
}
