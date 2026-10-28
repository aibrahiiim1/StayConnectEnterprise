package main

import (
	"bufio"
	"bytes"
	"io"
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
	return arpFind(f, ip)
}

// arpFind formats the wanted address once and splits only the line that names it: it runs on every landing page and
// sign-in, and splitting every line of a large neighbour table allocated per line (perf D3-L1).
func arpFind(rd io.Reader, ip net.IP) (net.HardwareAddr, bool) {
	want := []byte(ip.String())
	s := bufio.NewScanner(rd)
	_ = s.Scan()
	for s.Scan() {
		line := bytes.TrimLeft(s.Bytes(), " \t")
		if !bytes.HasPrefix(line, want) || len(line) == len(want) || (line[len(want)] != ' ' && line[len(want)] != '\t') {
			continue
		}
		fields := strings.Fields(string(line))
		if len(fields) < 4 {
			continue
		}
		if fields[3] != "00:00:00:00:00:00" {
			if mac, err := net.ParseMAC(fields[3]); err == nil {
				return mac, true
			}
		}
	}
	return nil, false
}
