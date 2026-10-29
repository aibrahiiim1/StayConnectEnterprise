package main

// acctd submits absolute tc counters and keeps no baseline of its own: the durable checkpoint is the baseline,
// otherwise a restart would adopt the current counter as zero and silently lose the bytes used meanwhile.
// Reset detection comes from netd, which owns the managed classes; a backwards counter is otherwise ambiguous
// (recreated class, misread, reused minor) and must never be guessed.

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
)

// Degradation reasons are fixed phrases so no error text, SQL or identifier can reach a health reader.
const (
	reasonSessionsUnreadable = "phase3 session inventory unreadable"
	reasonNoClassGenerations = "managed-class generations unavailable"
	reasonCountersUnreadable = "tc counters unreadable on a managed bridge"
	reasonSourceIncoherent   = "a live session's counter source is incoherent"
	reasonObservationRefused = "an observation was refused by the accounting boundary"
	reasonNoRecentPass       = "no accounting pass has completed within the freshness window"
	reasonExpiryStuck        = "an entitlement's expiry has failed in several consecutive sweeps"
	reasonSubmitterStalled   = "the shaping plan has not been delivered to netd within the plan's validity"
)

type phase3Session struct {
	ID       string
	DeviceID string
	IP       net.IP
	Bridge   string
}

type counterReader interface {
	ReadClasses(ctx context.Context, iface string) (map[int]shape.ClassBytes, error)
}

// epochSource supplies netd's generation per managed class; acctd never invents one.
type epochSource interface {
	ClassEpochs(ctx context.Context) (map[string]int64, error)
}

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
			// Guessing an address would attribute somebody else's traffic to this guest.
			continue
		}
		out = append(out, phase3Session{ID: id, DeviceID: dev, IP: parsed, Bridge: bridge})
	}
	return out, rows.Err()
}

// accountingPass submits each live session's absolute counters and returns how many observations the database
// accepted as usage. fallbackBridge only picks interfaces to read; it is never put into a session's identity
// (the database validates the bridge against the session's row). A tc read failure skips every session on
// that interface rather than substituting zero, which would make the next real reading look like a huge burst.
func (p *phase3) accountingPass(ctx context.Context, rd counterReader, ep epochSource, fallbackBridge string, now time.Time) int {
	if p == nil || !p.ownsAccounting() {
		return 0
	}
	sessions, err := p.livePhase3Sessions(ctx)
	if err != nil {
		p.acctDegraded = reasonSessionsUnreadable
		slog.Error("phase3: could not load sessions for accounting", "err", err)
		return 0
	}
	if len(sessions) == 0 {
		// Nothing to measure is a clean pass, so an appliance with no guests online does not go stale.
		p.acctDegraded = ""
		p.lastPassOK = now
		return 0
	}
	epochs, err := ep.ClassEpochs(ctx)
	if err != nil {
		// Without netd's generation a backwards counter cannot be told from a reset; skip and keep checkpoints.
		p.acctDegraded = reasonNoClassGenerations
		slog.Warn("phase3: class generations unavailable; accounting deferred", "err", err)
		return 0
	}

	// Counters are read once per interface; a failed read poisons only that interface.
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
		if s.Bridge == "" {
			// Substituting a default would let the daemon decide where counters came from; the controlled
			// operation re-derives that from the session's row precisely so it cannot.
			degraded = reasonSourceIncoherent
			slog.Warn("phase3: session has no ingress interface; not accounted", "session", s.ID)
			continue
		}
		bridge := s.Bridge
		minor, ok := shape.MinorForIP(s.IP)
		if !ok {
			degraded = reasonSourceIncoherent
			continue
		}
		down := read(bridge)
		up := read(shape.IFBName(bridge))
		if down.err != nil || up.err != nil {
			degraded = reasonCountersUnreadable
			continue
		}
		epoch, known := epochs[bridge+"|"+s.ID]
		if !known {
			// netd has no class for this session yet, so there is no trustworthy generation.
			continue
		}
		upBytes, upOK := up.classes[minor]
		downBytes, downOK := down.classes[minor]
		if !upOK || !downOK {
			// Audit P6 L13: a class absent from the counter dump is an unread meter, not a reading of 0 bytes.
			degraded = reasonCountersUnreadable
			slog.Warn("phase3: session's class is absent from the counters; not accounted", "session", s.ID)
			continue
		}
		class, err := p.ingestAbsolute(ctx, s, bridge, minor, epoch,
			int64(upBytes.Bytes), int64(downBytes.Bytes), now)
		if err != nil {
			degraded = reasonObservationRefused
			slog.Warn("phase3: absolute counter observation refused", "session", s.ID, "err", err)
			continue
		}
		switch class {
		case "ACCEPTED", "DELAYED":
			// A REPLAY:* classification stored nothing further; counting it would make retries look like traffic.
			accepted++
		}
	}
	p.acctDegraded = degraded
	if degraded == "" {
		p.lastPassOK = now
	}
	return accepted
}

func (p *phase3) ingestAbsolute(ctx context.Context, s phase3Session, bridge string, minor int, epoch, absUp, absDown int64, at time.Time) (string, error) {
	var class string
	err := p.enf.Pool().QueryRow(ctx, `SELECT iam_v2.ingest_absolute_counters($1,$2,$3::uuid,$4::uuid,$5,$6,$7,$8,$9,$10)`,
		p.tenant, p.site, s.ID, s.DeviceID, bridge, minor, epoch, absUp, absDown, at).Scan(&class)
	if err != nil {
		return "", err
	}
	return class, nil
}

// AccountingDegraded returns a fixed reason phrase, never raw SQL or a guest/session identifier: health
// readers are not authorised to see who is staying at the property.
func (p *phase3) AccountingDegraded() string {
	if p == nil {
		return ""
	}
	return p.acctDegraded
}

// A pass older than this is degraded even if nothing errored: a silently stopped loop looks like success.
const accountingFreshness = 5 * time.Minute

func (p *phase3) accountingHealth(now time.Time) map[string]any {
	if p == nil {
		return map[string]any{"active": false}
	}
	out := map[string]any{"active": true}
	stale := p.lastPassOK.IsZero() || now.Sub(p.lastPassOK) > accountingFreshness
	if !p.lastPassOK.IsZero() {
		out["last_clean_pass_at"] = p.lastPassOK.UTC().Format(time.RFC3339)
	}
	out["stale"] = stale
	reason := p.acctDegraded
	if reason == "" && stale {
		reason = reasonNoRecentPass
	}
	out["degraded"] = reason != ""
	if reason != "" {
		out["reason"] = reason
	}
	return out
}
