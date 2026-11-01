package main

import (
	"os"
	"strings"
)

// sysParentMAC is the MAC address the system reports for a network interface ("" when it cannot be read). The netplan
// renderer gives the bridge of a tagged client network this address (netcfg.RenderNetplanWith).
func sysParentMAC(iface string) string {
	if iface == "" || strings.ContainsAny(iface, "/\\") || strings.Contains(iface, "..") {
		return ""
	}
	b, err := os.ReadFile("/sys/class/net/" + iface + "/address")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
