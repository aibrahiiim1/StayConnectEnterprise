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
func (s *server) reconcileNFTFromDB(ctx context.Context, siteID string) (int, error) {
	rows, err := s.db.Query(ctx, `
        SELECT host(ip), COALESCE(ingress_interface, ''),
               CASE
                 WHEN expires_at IS NULL THEN NULL
                 ELSE EXTRACT(epoch FROM (expires_at - now()))::int
               END AS ttl_seconds
          FROM sessions
         WHERE tenant_id = $1
           AND site_id = $2
           AND state = 'active'
           AND (expires_at IS NULL OR expires_at > now())
    `, s.tenID, siteID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	applied := 0
	for rows.Next() {
		var ipStr, iface string
		var ttlSec *int
		if err := rows.Scan(&ipStr, &iface, &ttlSec); err != nil {
			return applied, err
		}
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		// Sessions created before Phase 19 (or on the legacy network) have no
		// recorded ingress interface; fall back to the legacy bridge so the
		// concatenated auth set element is well-formed.
		if iface == "" {
			iface = s.legacyBridge
		}
		// 0 means "kernel default / no timeout" — used for unlimited
		// sessions. Otherwise floor at 60s so we don't add a row that's
		// about to expire within the same kernel tick.
		ttl := 0
		if ttlSec != nil {
			ttl = *ttlSec
			if ttl < 60 {
				ttl = 60
			}
		}
		s.nft.applyLocal(ctx, nftOp{Op: "add", Iface: iface, IP: ip.String(), TTLSeconds: ttl})
		applied++
	}
	return applied, rows.Err()
}

// reconcileShapingFromDB re-establishes the per-session tc shaping/accounting
// classes (download on the guest bridge, upload on its IFB) for every active
// session, using each session's recorded ingress bridge and its plan's rate
// caps. Kernel tc state does not survive a reboot (IFB devices are recreated
// empty) and is unknown to a freshly-promoted backup, so this is what keeps the
// accounting pipeline continuous across scd restarts and appliance reboots.
// Best-effort per row: one bad session never blocks the rest.
func (s *server) reconcileShapingFromDB(ctx context.Context, siteID string) (int, error) {
	rows, err := s.db.Query(ctx, `
        SELECT host(s.ip), COALESCE(s.ingress_interface, ''),
               COALESCE(t.down_kbps, 0), COALESCE(t.up_kbps, 0)
          FROM sessions s
          LEFT JOIN vouchers v ON v.id = s.voucher_id
          LEFT JOIN ticket_templates t ON t.id = v.template_id
         WHERE s.tenant_id = $1
           AND s.site_id = $2
           AND s.state = 'active'
           AND (s.expires_at IS NULL OR s.expires_at > now())
    `, s.tenID, siteID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type row struct {
		ip       net.IP
		bridge   string
		down, up int
	}
	var pending []row
	for rows.Next() {
		var ipStr, iface string
		var down, up int
		if err := rows.Scan(&ipStr, &iface, &down, &up); err != nil {
			return len(pending), err
		}
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if iface == "" {
			iface = s.legacyBridge
		}
		pending = append(pending, row{ip: ip, bridge: iface, down: down, up: up})
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	applied := 0
	for _, p := range pending {
		if err := s.shp.AddSession(ctx, p.bridge, p.ip, p.down, p.up); err != nil {
			slog.Warn("shaping reconcile: add session", "ip", p.ip.String(), "bridge", p.bridge, "err", err)
			continue
		}
		applied++
	}
	return applied, nil
}
