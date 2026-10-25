package main

import (
	"net/http"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// A client network's name, domain and a reservation's hostname are written into the generated firewall, DHCP and DNS files
// (audit Phase 8, H1). netd refuses them again at apply time; refusing here tells the operator at the moment they type it.

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
