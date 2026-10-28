package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// A client network's name, domain and a reservation's hostname are written into the generated firewall, DHCP and DNS files
// (audit Phase 8, H1). netd refuses them again at apply time; refusing here tells the operator at the moment they type it.

// reservedIPRefusal checks a reservation's address against its client network's subnet when it is saved, so the
// operator hears about it at once instead of as an apply failure from netd (audit P9 M5). netd checks it again.
// It returns the error code and message, or "" when the address is usable. subnet is the network's CIDR text.
func reservedIPRefusal(ip, subnet string) (string, string) {
	addr := net.ParseIP(strings.TrimSpace(ip))
	if addr == nil || addr.To4() == nil {
		return "bad_ip", "reserved_ip is not a valid IPv4 address"
	}
	_, ipnet, err := net.ParseCIDR(strings.TrimSpace(subnet))
	if err != nil {
		// No readable subnet: netd's apply-time check still stands; nothing here to compare against.
		return "", ""
	}
	if !ipnet.Contains(addr) {
		return "reservation_outside_subnet", fmt.Sprintf("reserved_ip %s is outside the client network's subnet %s", addr, ipnet)
	}
	return "", ""
}

// writeNetworkTextRefusal answers 400 with the field's own code when text is unusable and reports whether it did.
func writeNetworkTextRefusal(w http.ResponseWriter, name, domain string) bool {
	if name != "" {
		if why := netcfg.CheckNetworkName(name); why != "" {
			jsonErr(w, http.StatusBadRequest, "invalid_name", why)
			return true
		}
	}
	if why := netcfg.CheckDomainName(domain); why != "" {
		jsonErr(w, http.StatusBadRequest, "invalid_domain", why)
		return true
	}
	return false
}

func writeHostnameRefusal(w http.ResponseWriter, hostname string) bool {
	if why := netcfg.CheckDomainName(hostname); why != "" {
		jsonErr(w, http.StatusBadRequest, "invalid_hostname", why)
		return true
	}
	return false
}
