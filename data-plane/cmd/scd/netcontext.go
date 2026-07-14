package main

import (
	"context"
	"net"
)

// netContext is the guest network a source IP belongs to. Because netd enforces
// non-overlapping enabled subnets on one appliance, a source IP maps to at most
// one guest network, so IP alone unambiguously determines the ingress bridge.
type netContext struct {
	NetworkID string
	VLANID    *int
	Bridge    string
	GatewayIP string
}

// resolveNetwork finds the guest network whose subnet contains ip. Falls back
// to the legacy bridge (default br-lan) with no network id when the networking
// tables are empty or the IP matches nothing — preserving pre-Phase-19
// behaviour so the guest path never breaks during rollout.
func (s *server) resolveNetwork(ctx context.Context, ip net.IP) netContext {
	fallback := netContext{Bridge: s.legacyBridge}
	if s.db == nil || ip == nil {
		return fallback
	}
	var netID, bridge, gw string
	var vlan *int
	err := s.db.QueryRow(ctx, `
        SELECT id::text, bridge_name, host(gateway_ip), vlan_id
          FROM guest_networks
         WHERE enabled AND subnet_cidr >>= $1::inet
         ORDER BY masklen(subnet_cidr) DESC
         LIMIT 1
    `, ip.String()).Scan(&netID, &bridge, &gw, &vlan)
	if err != nil || bridge == "" {
		return fallback
	}
	return netContext{NetworkID: netID, VLANID: vlan, Bridge: bridge, GatewayIP: gw}
}

// recordSessionNetwork stamps the session row with its network context after
// creation. Best-effort: a failure here never fails the authorization.
func (s *server) recordSessionNetwork(ctx context.Context, sessionID string, nc netContext) {
	if sessionID == "" || nc.NetworkID == "" {
		return
	}
	var vlanArg any
	if nc.VLANID != nil {
		vlanArg = *nc.VLANID
	}
	var gwArg any
	if nc.GatewayIP != "" {
		gwArg = nc.GatewayIP
	}
	_, _ = s.db.Exec(ctx, `
        UPDATE sessions
           SET guest_network_id = $2::uuid,
               vlan_id = $3,
               ingress_interface = NULLIF($4,''),
               gateway_ip = $5::inet
         WHERE id = $1
    `, sessionID, nc.NetworkID, vlanArg, nc.Bridge, gwArg)
}
