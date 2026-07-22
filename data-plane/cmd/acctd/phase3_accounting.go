package main

// PHASE-3 ACCOUNTING PASS.
//
// This exists because the previous wiring was wrong in a way the tests could not see: the Phase-3 ingest was
// called from the LEGACY loop, using session ids from the legacy `sessions` table, while the controlled
// operation resolves sessions in `iam_v2.sessions`. Every call would have been refused as out-of-scope. The
// tests passed because they invoked the operation directly with iam_v2 ids — they proved the operation, not
// the composition. So the pass now reads its OWN session domain, and a composition-root test drives THIS
// function rather than the operation beneath it.
//
// Two further properties matter more than they look:
//
//   - SAMPLE IDENTITY IS DERIVED FROM THE SAMPLE, NOT FROM MEMORY. An in-memory per-session counter resets on
//     restart, so the next sample would reuse an identity that is already stored, be classified DUPLICATE and
//     have its bytes silently dropped — real usage lost, and nothing anywhere would say so. The identity is
//     the sample's own tick instant (epoch seconds), which is stable across restarts, identical for a genuine
//     retry of the same tick, and monotonic.
//   - A FIRST OBSERVATION IS A BASELINE, NEVER A SAMPLE. After a start, a restart or a re-created tc class
//     there is no previous reading to subtract, so the pass adopts the current counter and writes nothing.
//     Writing the absolute counter would bill the guest for everything since the class was created.

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
)

// phase3Session is one live Phase-3 session as the accounting pass sees it.
type phase3Session struct {
	ID     string
	IP     net.IP
	Bridge string
}

// counterReader reads the tc classes of one interface. It is an interface so the pass can be driven with
// synthetic deltas in a composition-root test.
type counterReader interface {
	ReadClasses(ctx context.Context, iface string) (map[int]shape.ClassBytes, error)
}

// sampleSeqFor derives the durable identity of a sample from WHEN it was taken. Seconds are the right
// granularity: the tick is seconds-scale, so two readings that land in the same second ARE the same tick and
// collapsing them is correct, while a restart mid-tick re-derives exactly the same identity instead of
// inventing a fresh one.
func sampleSeqFor(at time.Time) int64 { return at.Unix() }

// livePhase3Sessions returns the site's active Phase-3 sessions with the addressing the counters are keyed by.
func (p *phase3) livePhase3Sessions(ctx context.Context) ([]phase3Session, error) {
	rows, err := p.enf.Pool().Query(ctx, `SELECT s.id::text, COALESCE(host(s.ip),''), COALESCE(s.ingress_interface,'')
		FROM iam_v2.sessions s
		WHERE s.tenant_id=$1 AND s.site_id=$2 AND s.state='active' AND s.ended IS NULL
		ORDER BY s.started`, p.tenant, p.site)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []phase3Session
	for rows.Next() {
		var id, ip, bridge string
		if err := rows.Scan(&id, &ip, &bridge); err != nil {
			return nil, err
		}
		parsed := net.ParseIP(ip)
		if parsed == nil {
			// A session with no address cannot be measured. Skipping it is right; guessing an address would
			// attribute somebody else's traffic to this guest.
			continue
		}
		out = append(out, phase3Session{ID: id, IP: parsed, Bridge: bridge})
	}
	return out, rows.Err()
}

// accountingPass reads each live Phase-3 session's counters once, computes the delta against the previous
// observation, and ingests it through the controlled operation. It returns the number of samples accepted,
// which the tick logs — a pass that accepts nothing for a busy site is a symptom worth seeing.
func (p *phase3) accountingPass(ctx context.Context, rd counterReader, fallbackBridge string, now time.Time) int {
	if p == nil || !p.ownsAccounting() {
		return 0
	}
	sessions, err := p.livePhase3Sessions(ctx)
	if err != nil {
		slog.Error("phase3: could not load sessions for accounting", "err", err)
		return 0
	}
	if len(sessions) == 0 {
		// No live sessions: drop the baselines so a returning session re-baselines rather than measuring a
		// delta against a counter that belonged to a different class.
		p.baseline = nil
		return 0
	}
	if p.baseline == nil {
		p.baseline = map[string]snapEntry{}
	}

	downCache := map[string]map[int]shape.ClassBytes{}
	upCache := map[string]map[int]shape.ClassBytes{}
	readDown := func(bridge string) map[int]shape.ClassBytes {
		if m, ok := downCache[bridge]; ok {
			return m
		}
		m, _ := rd.ReadClasses(ctx, bridge)
		downCache[bridge] = m
		return m
	}
	readUp := func(bridge string) map[int]shape.ClassBytes {
		ifb := shape.IFBName(bridge)
		if m, ok := upCache[ifb]; ok {
			return m
		}
		m, _ := rd.ReadClasses(ctx, ifb)
		upCache[ifb] = m
		return m
	}

	next := map[string]snapEntry{}
	accepted := 0
	for _, s := range sessions {
		bridge := s.Bridge
		if bridge == "" {
			bridge = fallbackBridge
		}
		minor, ok := shape.MinorForIP(s.IP)
		if !ok {
			continue
		}
		curUp := readUp(bridge)[minor].Bytes
		curDown := readDown(bridge)[minor].Bytes
		next[s.ID] = snapEntry{BytesUp: curUp, BytesDown: curDown}

		prev, seen := p.baseline[s.ID]
		if !seen {
			// First observation (fresh session, acctd restart, or a rebuilt class): adopt the baseline and
			// write nothing, so already-persisted usage is never counted twice.
			continue
		}
		dUp := int64(curUp) - int64(prev.BytesUp)
		dDown := int64(curDown) - int64(prev.BytesDown)
		if dUp < 0 { // the class was re-created; count from zero rather than storing a negative delta
			dUp = int64(curUp)
		}
		if dDown < 0 {
			dDown = int64(curDown)
		}
		if dUp == 0 && dDown == 0 {
			continue
		}
		class, err := p.ingestSample(ctx, sampleIdentity{SessionID: s.ID, Seq: sampleSeqFor(now)}, dUp, dDown, now)
		if err != nil {
			// A refused sample is NOT progress: keep the previous baseline so this delta is re-measured and
			// re-offered next tick instead of being lost.
			slog.Warn("phase3: accounting sample refused", "session", s.ID, "err", err)
			next[s.ID] = prev
			continue
		}
		switch class {
		case "ACCEPTED":
			accepted++
		case "DELAYED":
			accepted++
			slog.Info("phase3: sample belongs to a frozen period; recorded as delayed", "session", s.ID)
		case "DUPLICATE":
			// The same tick was already ingested (a retry, or two passes inside one second). The bytes are
			// already stored, so advancing the baseline is correct — this is not lost usage.
		}
	}
	p.baseline = next
	return accepted
}
