package main

import (
	"bufio"
	"net"
	"os"
	"strings"
)

type arpLookup func(ip net.IP) (net.HardwareAddr, bool)

// defaultArp reads /proc/net/arp; the entry exists once the kernel has seen any packet from the client.
func defaultArp(ip net.IP) (net.HardwareAddr, bool) {
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return nil, false
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	_ = s.Scan()
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 4 {
			continue
		}
		if fields[0] == ip.String() && fields[3] != "00:00:00:00:00:00" {
			if mac, err := net.ParseMAC(fields[3]); err == nil {
				return mac, true
			}
		}
	}
	return nil, false
}
