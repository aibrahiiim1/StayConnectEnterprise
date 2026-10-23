package main

import (
	"context"
	"net"
)

// netContext is the guest network a source IP belongs to; netd enforces non-overlapping enabled subnets, so IP alone determines the ingress bridge.
type netContext struct {
	NetworkID string
	// Name is the operator's label for the network, so a sign-in attempt can be recorded against something a human recognises.
	Name      string
	VLANID    *int
	Bridge    string
	GatewayIP string
}

// resolveNetwork finds the guest network whose subnet contains ip, falling back to the legacy bridge (default br-lan) with
// no network id when nothing matches, so the guest path never breaks.
func (s *server) resolveNetwork(ctx context.Context, ip net.IP) netContext {
	fallback := netContext{Bridge: s.legacyBridge}
	if s.db == nil || ip == nil {
		return fallback
	}
	var netID, bridge, gw, name string
	var vlan *int
	err := s.db.QueryRow(ctx, `
        SELECT id::text, bridge_name, host(gateway_ip), vlan_id, COALESCE(name,'')
          FROM guest_networks
         WHERE enabled AND subnet_cidr >>= $1::inet
         ORDER BY masklen(subnet_cidr) DESC
         LIMIT 1
    `, ip.String()).Scan(&netID, &bridge, &gw, &vlan, &name)
	if err != nil || bridge == "" {
		return fallback
	}
	return netContext{NetworkID: netID, Name: name, VLANID: vlan, Bridge: bridge, GatewayIP: gw}
}
