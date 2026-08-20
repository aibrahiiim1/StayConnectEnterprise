package main

// NFT set replication for HA pairs (phase 5.5).
//
// When two scd instances run at the same site (active + backup, tracked by
// keepalived), both must hold the same auth_ipv4 nft set so a VRRP
// failover doesn't drop live sessions.
//
// Approach: the live scd publishes every set mutation on nft.{siteID};
// peer(s) subscribe and mirror into their own local nft. Self-echo is
// suppressed via a sender stamp.
//
// This wraps nft.Client without changing its API — every existing
// Allow/Deny call site picks up replication for free.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/stayconnect/enterprise/data-plane/internal/metrics"
	"github.com/stayconnect/enterprise/data-plane/internal/nft"
)

type nftSync struct {
	client      *nft.Client
	nc          *nats.Conn // may be nil → publish is a no-op (dev / standalone)
	applianceID string
	siteID      string
	met         *metrics.Registry // may be nil during early boot
}

func newNFTSync(client *nft.Client, nc *nats.Conn, applianceID, siteID string) *nftSync {
	return &nftSync{client: client, nc: nc, applianceID: applianceID, siteID: siteID}
}

// SetMetrics wires the metrics registry. Called from main once it's built;
// nft mutations before this are simply not counted.
func (n *nftSync) SetMetrics(m *metrics.Registry) { n.met = m }

// Allow mirrors nft.Client.Allow and, on success, publishes the op. iface is
// the ingress guest bridge (Phase 19); it is ignored on a legacy IP-only set.
func (n *nftSync) Allow(ctx context.Context, iface string, ip net.IP, ttl time.Duration) error {
	if err := n.client.Allow(ctx, iface, ip, ttl); err != nil {
		return err
	}
	n.publishOp(nftOp{Op: "add", Iface: iface, IP: ip.String(), TTLSeconds: int(ttl.Seconds())})
	if n.met != nil {
		n.met.NFTOps.WithLabelValues("add", "local").Inc()
	}
	return nil
}

// Deny mirrors nft.Client.Deny and, on success, publishes the op.
func (n *nftSync) Deny(ctx context.Context, iface string, ip net.IP) error {
	if err := n.client.Deny(ctx, iface, ip); err != nil {
		return err
	}
	n.publishOp(nftOp{Op: "del", Iface: iface, IP: ip.String()})
	if n.met != nil {
		n.met.NFTOps.WithLabelValues("del", "local").Inc()
	}
	return nil
}

// applyLocal applies a peer's op to our local nft set WITHOUT re-publishing.
// Only the NATS subscriber calls this.
func (n *nftSync) applyLocal(ctx context.Context, op nftOp) {
	ip := net.ParseIP(op.IP)
	if ip == nil {
		return
	}
	switch op.Op {
	case "add":
		ttl := time.Duration(op.TTLSeconds) * time.Second
		if err := n.client.Allow(ctx, op.Iface, ip, ttl); err != nil {
			slog.Warn("nft mirror add failed", "ip", op.IP, "err", err)
			return
		}
		if n.met != nil {
			n.met.NFTOps.WithLabelValues("add", "peer").Inc()
		}
	case "del":
		if err := n.client.Deny(ctx, op.Iface, ip); err != nil {
			slog.Warn("nft mirror del failed", "ip", op.IP, "err", err)
			return
		}
		if n.met != nil {
			n.met.NFTOps.WithLabelValues("del", "peer").Inc()
		}
	}
}

type nftOp struct {
	Op         string `json:"op"` // "add" | "del"
	Iface      string `json:"iface,omitempty"`
	IP         string `json:"ip"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
	Sender     string `json:"sender"` // publisher's applianceID; self-filter
}

func (n *nftSync) publishOp(op nftOp) {
	if n.nc == nil || n.siteID == "" {
		return
	}
	op.Sender = n.applianceID
	body, _ := json.Marshal(op)
	if err := n.nc.Publish("nft."+n.siteID, body); err != nil {
		slog.Warn("nft publish failed", "err", err)
	}
}

// startNFTSyncSubscriber wires the mirror path. Must be called after
// s.nft has been set and s.applianceID / s.tenID / siteID are known.
func startNFTSyncSubscriber(ctx context.Context, s *server, nc *nats.Conn, siteID string) error {
	if siteID == "" {
		return nil // single-site dev deployment
	}
	subj := "nft." + siteID
	_, err := nc.Subscribe(subj, func(m *nats.Msg) {
		var op nftOp
		if err := json.Unmarshal(m.Data, &op); err != nil {
			return
		}
		if op.Sender == s.applID {
			return // our own echo; already applied locally
		}
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		s.nft.applyLocal(rctx, op)
	})
	if err != nil {
		return err
	}
	slog.Info("nft sync subscribed", "subject", subj)
	return nil
}

// reconcileNFTFromDB rebuilds the local auth_ipv4 nft set from the active
// sessions rows for this site. Runs once on boot: handles the case where
// scd restarts and loses its (kernel-held) nft set, AND it's the primary
// path by which a brand-new backup scd bootstraps before any NATS ops arrive.
//
// Per-row TTL = max(60s, expires_at - now). Rows already past expiry are
// skipped — the reaper will close them shortly. Rows with NULL expires_at
// (unlimited tenants) get a long TTL via the kernel default and survive
// until explicitly revoked.

// THE LEGACY RECONCILERS ARE GONE.
//
// reconcileNFTFromDB and reconcileShapingFromDB rebuilt the firewall set and the shaping classes from
// public.sessions. That table is the superseded session domain and no longer exists: the current authority
// is iam_v2.sessions, authorized through netd (cmd/netd/phase3_enforcement.go) and accounted by acctd
// (cmd/acctd/phase3_accounting.go), which own their own reconciliation.
//
// Two reconcilers writing one nft set is the collision internal/nft/nft.go warns about, so removing the
// legacy one is not merely cleanup -- it removes the possibility of the two disagreeing about who is
// allowed on the network.
