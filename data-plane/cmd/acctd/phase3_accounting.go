package main

// PHASE-3 ACCOUNTING PASS.
//
// The runtime submits what it can actually see — the ABSOLUTE tc counters — and the database decides what that
// means. acctd keeps NO baseline of its own: the durable checkpoint is the baseline, which is the only way a
// restart can be safe. A process that came back and adopted the current counter as "zero" would silently lose
// every byte the guest used while it was down, and nothing anywhere would say so.
//
// The trusted reset signal comes from netd, the only process that creates or replaces these managed classes.
// A counter that went backwards is otherwise ambiguous — a recreated class, a misread, or a minor reused by a
// different guest — and accounting must never guess between those.

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
)

// phase3Session is one live Phase-3 session as the accounting pass sees it.
type phase3Session struct {
	ID       string
	DeviceID string
	IP       net.IP
	Bridge   string
}

// counterReader reads the tc classes of one interface. It is an interface so the pass can be driven with
// synthetic counters in a composition-root test.
type counterReader interface {
	ReadClasses(ctx context.Context, iface string) (map[int]shape.ClassBytes, error)
}

// epochSource supplies the TC owner's generation per managed class. acctd never invents one: if netd cannot be
// asked, the affected sessions are skipped rather than accounted against an unknown generation.
type epochSource interface {
	ClassEpochs(ctx context.Context) (map[string]int64, error)
}

// livePhase3Sessions returns the site's active Phase-3 sessions with the addressing the counters are keyed by.
func (p *phase3) livePhase3Sessions(ctx context.Context) ([]phase3Session, error) {
	rows, err := p.enf.Pool().Query(ctx, `SELECT s.id::text, s.device_id::text, COALESCE(host(s.ip),''),
			COALESCE(s.ingress_interface,'')
		FROM iam_v2.sessions s
		WHERE s.tenant_id=$1 AND s.site_id=$2 AND s.state='active' AND s.ended IS NULL
		ORDER BY s.started`, p.tenant, p.site)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []phase3Session
	for rows.Next() {
		var id, dev, ip, bridge string
		if err := rows.Scan(&id, &dev, &ip, &bridge); err != nil {
			return nil, err
		}
		parsed := net.ParseIP(ip)
		if parsed == nil {
			// A session with no address cannot be measured. Skipping it is right; guessing an address would
			// attribute somebody else's traffic to this guest.
			continue
		}
		out = append(out, phase3Session{ID: id, DeviceID: dev, IP: parsed, Bridge: bridge})
	}
	return out, rows.Err()
}

// accountingPass reads each live Phase-3 session's absolute counters once and submits them to the controlled
// operation. It returns how many observations the database ACCEPTED as usage (baselines and duplicates are not
// usage). A tc read failure for an interface skips every session on it — never a zero substitution, which
// would look like "the guest stopped using the network" and, worse, would make the next real reading look like
// a huge burst.
func (p *phase3) accountingPass(ctx context.Context, rd counterReader, ep epochSource, fallbackBridge string, now time.Time) int {
	if p == nil || !p.ownsAccounting() {
		return 0
	}
	sessions, err := p.livePhase3Sessions(ctx)
	if err != nil {
		p.acctDegraded = "cannot load Phase-3 sessions: " + err.Error()
		slog.Error("phase3: could not load sessions for accounting", "err", err)
		return 0
	}
	if len(sessions) == 0 {
		p.acctDegraded = ""
		return 0
	}
	epochs, err := ep.ClassEpochs(ctx)
	if err != nil {
		// Without the TC owner's generation a backwards counter cannot be told from a reset. Skipping the pass
		// preserves every checkpoint; the next tick tries again.
		p.acctDegraded = "class generations unavailable: " + err.Error()
		slog.Warn("phase3: class generations unavailable; accounting deferred", "err", err)
		return 0
	}

	// counters are read once per interface, and a failed read poisons only that interface
	type reading struct {
		classes map[int]shape.ClassBytes
		err     error
	}
	cache := map[string]reading{}
	read := func(iface string) reading {
		if r, ok := cache[iface]; ok {
			return r
		}
		m, err := rd.ReadClasses(ctx, iface)
		r := reading{classes: m, err: err}
		cache[iface] = r
		return r
	}

	accepted, degraded := 0, ""
	for _, s := range sessions {
		bridge := s.Bridge
		if bridge == "" {
			bridge = fallbackBridge
		}
		minor, ok := shape.MinorForIP(s.IP)
		if !ok {
			continue
		}
		down := read(bridge)
		up := read(shape.IFBName(bridge))
		if down.err != nil || up.err != nil {
			// preserve the checkpoint and this session's history; retry next tick
			degraded = "tc counters unreadable on " + bridge
			continue
		}
		epoch, known := epochs[bridge+"|"+s.ID]
		if !known {
			// netd is not managing a class for this session yet (it has not been shaped, or the plan has not
			// been applied). There is no trustworthy generation, so the observation is not submitted.
			continue
		}
		class, err := p.ingestAbsolute(ctx, s, bridge, minor, epoch,
			int64(up.classes[minor].Bytes), int64(down.classes[minor].Bytes), now)
		if err != nil {
			degraded = "accounting refused for a session: " + err.Error()
			slog.Warn("phase3: absolute counter observation refused", "session", s.ID, "err", err)
			continue
		}
		switch class {
		case "ACCEPTED", "DELAYED":
			accepted++
		}
	}
	p.acctDegraded = degraded
	return accepted
}

// ingestAbsolute submits one session's absolute counters through the controlled operation.
func (p *phase3) ingestAbsolute(ctx context.Context, s phase3Session, bridge string, minor int, epoch, absUp, absDown int64, at time.Time) (string, error) {
	var class string
	err := p.enf.Pool().QueryRow(ctx, `SELECT iam_v2.ingest_absolute_counters($1,$2,$3::uuid,$4::uuid,$5,$6,$7,$8,$9,$10)`,
		p.tenant, p.site, s.ID, s.DeviceID, bridge, minor, epoch, absUp, absDown, at).Scan(&class)
	if err != nil {
		return "", err
	}
	return class, nil
}

// AccountingDegraded reports the truthful accounting state for health: empty when the last pass completed
// cleanly, otherwise why it did not.
func (p *phase3) AccountingDegraded() string {
	if p == nil {
		return ""
	}
	return p.acctDegraded
}
