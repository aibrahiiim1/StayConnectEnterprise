package main

// scd's handle on this appliance's nftables sets.
//
// It is a thin, local-only wrapper: an operator revocation denies the address on THIS appliance and counts the
// mutation. (It used to also publish every mutation to a peer appliance over NATS; that transport is removed
// with the rest of the cloud telemetry subsystem, and nothing ever consumed the replication.) The Phase-3
// authorization set itself is owned and reconciled by netd from iam_v2.sessions; the walled-garden set is
// synced through client directly.

import (
	"context"
	"net"

	"github.com/stayconnect/enterprise/data-plane/internal/metrics"
	"github.com/stayconnect/enterprise/data-plane/internal/nft"
)

type localNFT struct {
	client *nft.Client
	met    *metrics.Registry // may be nil during early boot
}

func newLocalNFT(client *nft.Client) *localNFT { return &localNFT{client: client} }

// SetMetrics wires the metrics registry once it is built; mutations before this are simply not counted.
func (n *localNFT) SetMetrics(m *metrics.Registry) { n.met = m }

// Deny removes an address from the guest authorization set on its ingress bridge.
func (n *localNFT) Deny(ctx context.Context, iface string, ip net.IP) error {
	if err := n.client.Deny(ctx, iface, ip); err != nil {
		return err
	}
	if n.met != nil {
		n.met.NFTOps.WithLabelValues("del", "local").Inc()
	}
	return nil
}
