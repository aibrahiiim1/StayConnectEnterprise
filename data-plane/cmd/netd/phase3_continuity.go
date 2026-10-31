package main

// Outage continuity (docs/architecture/ONEGATE_OUTAGE_CONTINUITY.md).
//
// The kernel authorization set is renewed from acctd's plans. When plans stop (acctd hung or crashed, the database down), this
// governor keeps renewing exactly the authorizations that are STILL PRESENT, for exactly the sessions the last healthy plan
// bounded, until the earliest of:
//
//   - the window, measured on the boot-relative monotonic clock from the last accepted block (wall-clock changes cannot stretch it);
//   - the session's hard end (entitlement window, maximum session duration, online-time allowance);
//   - the entitlement's byte budget, metered here from the tc class counters (a budget spent ends every session of it);
//   - a revocation: pushed directly (POST /v1/flows/end, /v1/phase3/continuity/revoke), journalled before it is acknowledged,
//     or found by the database probe (a session the database no longer calls entitled).
//
// What it never does: create an authorization that is absent (so a reboot, a flushed set or an administrator's removal is never
// undone), extend one past its deadline, renew without DHCP ownership evidence, renew a budgeted session it cannot meter, or
// act on a record from another boot, scope or assignment.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// contTakeoverAge: plans have been silent this long (monotonic), so the governor renews. Below the 90 s lease with margin: the
// element renewed last by the plan path still has more than this left to run. A variable only so the real-kernel test can run in seconds.
var contTakeoverAge = 60 * time.Second

const (
	// contSampleEvery is the spacing of the counter samples a budget baseline is taken from (the producer's margin assumes this).
	contSampleEvery = 30 * time.Second
	contSampleKeep  = 12
	// contSaveEvery throttles the routine state write; every drop is written at once.
	contSaveEvery = 15 * time.Second
	// contBaselineSlack: a baseline sample taken this long AFTER the block's AsOf is still accepted. Later and bytes between AsOf
	// and the sample would go uncounted.
	contBaselineSlack = 5 * time.Second
	contRevokeKeep    = 4 * time.Hour
	contRevokeMax     = 4096
	contStateVersion  = 1
	contProbeTimeout  = 3 * time.Second
	contProbeChunk    = 400
)

// contSess is one session the governor may keep.
type contSess struct {
	SessionID     string `json:"session_id"`
	Bridge        string `json:"bridge"`
	IP            string `json:"ip"`
	MAC           string `json:"mac,omitempty"`
	EntitlementID string `json:"entitlement_id"`
	Minor         int    `json:"minor"`
	Epoch         int64  `json:"epoch"`
	// DeadlineBoot is the monotonic (boot-relative, ms) instant after which the session is no longer renewed.
	DeadlineBoot int64 `json:"deadline_boot_ms"`
	// Base is the class counter (down+up) the budget is measured from; HasBase is false for a session with no byte budget.
	Base    uint64 `json:"base_bytes"`
	HasBase bool   `json:"has_base"`
	// LastUsed is the bytes metered for this session since Base at the last metering; it is folded into the entitlement's
	// Gone when the session is dropped, so a shared allowance remembers what a departed device used.
	LastUsed uint64 `json:"last_used_bytes"`
}

type contBudget struct {
	// Remaining is the producer's figure at the block's AsOf, margin already taken off.
	Remaining int64 `json:"remaining_bytes"`
	// Used is what netd has metered since the baselines (refreshed each outage tick; not authoritative on disk).
	Used uint64 `json:"used_bytes"`
	// Gone is what sessions that have been dropped (expired, revoked, refused by the database) used before they left. Never decreases.
	Gone      uint64 `json:"gone_bytes"`
	Exhausted bool   `json:"exhausted"`
}

type contState struct {
	Version     int    `json:"version"`
	TenantID    string `json:"tenant_id"`
	SiteID      string `json:"site_id"`
	ApplianceID string `json:"appliance_id"`
	AssignGen   int64  `json:"assignment_generation"`
	BootID      string `json:"boot_id"`
	PlanGen     int64  `json:"plan_generation"`
	// LastPlanBoot is when (boot ms) the last plan was accepted; the outage clock runs from it.
	LastPlanBoot int64                  `json:"last_plan_boot_ms"`
	WindowSec    int                    `json:"window_seconds"`
	AsOf         time.Time              `json:"as_of"`
	Sessions     map[string]*contSess   `json:"sessions"`
	Budgets      map[string]*contBudget `json:"budgets"`
	Sum          string                 `json:"sum"`
}

func (s contState) checksum() string {
	s.Sum = ""
	raw, _ := json.Marshal(s)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// contRevocation is one durable "this must not come back".
type contRevocation struct {
	SessionID     string    `json:"session_id,omitempty"`
	EntitlementID string    `json:"entitlement_id,omitempty"`
	Bridge        string    `json:"bridge,omitempty"`
	IP            string    `json:"ip,omitempty"`
	Reason        string    `json:"reason"`
	At            time.Time `json:"at"`
}

type contJournal struct {
	Version int              `json:"version"`
	Entries []contRevocation `json:"entries"`
	Sum     string           `json:"sum"`
}

func (j contJournal) checksum() string {
	j.Sum = ""
	raw, _ := json.Marshal(j)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

type contSample struct {
	at    time.Time
	bytes map[string]contCount // classKey -> counter
	// classes are the managed classes that existed when the sample was taken (classKey -> epoch), read or not. A class absent
	// from it was created AFTER the sample, so it counts from zero (a new tc class starts at 0 bytes), which can only overcount.
	classes map[string]int64
}

type contCount struct {
	epoch int64
	bytes uint64
}

// entitlementProbe asks the database which of these sessions are no longer entitled (ended, entitlement not ACTIVE, window past,
// or unknown). Read-only. An error means "could not ask", never "entitled".
type entitlementProbe interface {
	NotEntitled(ctx context.Context, tenant, site string, sessionIDs []string) (map[string]bool, error)
}

// pgEntitlementProbe is the production probe: iam_v2.continuity_not_entitled (migration 0199), a read-only SECURITY DEFINER
// function granted to svc_netd. It mirrors the "entitled" predicate of the plan derivation (acctd PlanForSite).
type pgEntitlementProbe struct {
	pool interface {
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	}
}

// NotEntitled asks for the scope the caller names (the CURRENT Phase-3 scope), never one fixed at startup.
func (e *pgEntitlementProbe) NotEntitled(ctx context.Context, tenant, site string, ids []string) (map[string]bool, error) {
	var out []string
	err := e.pool.QueryRow(ctx,
		`SELECT COALESCE(array_agg(x::text), ARRAY[]::text[]) FROM iam_v2.continuity_not_entitled($1::uuid,$2::uuid,$3::uuid[]) x`,
		tenant, site, ids).Scan(&out)
	if err != nil {
		return nil, err
	}
	m := make(map[string]bool, len(out))
	for _, id := range out {
		m[id] = true
	}
	return m, nil
}

// continuity is the governor's state. All fields are guarded by phase3Shaping.mu except where noted.
type continuity struct {
	statePath   string
	journalPath string
	probe       entitlementProbe

	st      *contState
	journal []contRevocation
	samples []contSample

	lastSample  time.Time
	lastSaved   time.Time
	dirty       bool
	outageSince time.Time
	// view of the last tick, for the status route and tests.
	lastTick      time.Time
	probeOK       bool
	probeFailures int
	refused       string
	scopeLogged   bool
	renewed       int64
	dropped       map[string]string // sessionID -> reason, bounded
	// notRenewed is why sessions held in the record were not renewed at the last outage tick (reason -> count). It is the evidence
	// for a record that holds more sessions than the kernel set (lab finding F5): which step skipped them, never a guess.
	notRenewed map[string]int
}

func (c *continuity) enabled() bool { return c != nil && c.statePath != "" }

// ---- persistence ------------------------------------------------------------------------------------------------------

func (c *continuity) saveState() error {
	if c.statePath == "" || c.st == nil {
		return nil
	}
	c.st.Version = contStateVersion
	c.st.Sum = c.st.checksum()
	raw, err := json.Marshal(c.st)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(c.statePath, raw, 0o600); err != nil {
		return err
	}
	c.lastSaved, c.dirty = time.Now(), false
	return nil
}

func (c *continuity) removeState() {
	c.st = nil
	if c.statePath != "" {
		_ = os.Remove(c.statePath)
	}
}

func (c *continuity) saveJournal() error {
	if c.journalPath == "" {
		return nil
	}
	j := contJournal{Version: contStateVersion, Entries: c.journal}
	j.Sum = j.checksum()
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return writeFileAtomic(c.journalPath, raw, 0o600)
}

// loadJournal returns the journal; an unreadable or tampered file is reported and treated as EMPTY-BUT-FLAGGED by the caller
// (continuity stays off until a fresh block is accepted, because a revocation could be hiding in it).
func (c *continuity) loadJournal() error {
	if c.journalPath == "" {
		return nil
	}
	raw, err := os.ReadFile(c.journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("revocation journal unreadable: %w", err)
	}
	var j contJournal
	if json.Unmarshal(raw, &j) != nil || j.Sum != j.checksum() {
		return errors.New("revocation journal is corrupt")
	}
	c.journal = j.Entries
	return nil
}

// restore loads the persisted state after a restart. It returns a reason when the record is refused; the caller logs it. A
// refused record is deleted: strict behaviour, never a guess.
func (p *phase3Shaping) contRestore() string {
	c := p.cont
	if !c.enabled() {
		return ""
	}
	if err := c.loadJournal(); err != nil {
		// A journal that cannot be trusted may be hiding a revocation: no continuity until a fresh block replaces the record.
		c.removeState()
		c.refused = err.Error()
		return c.refused
	}
	raw, err := os.ReadFile(c.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		c.refused = "continuity record unreadable"
		return c.refused
	}
	var st contState
	if json.Unmarshal(raw, &st) != nil || st.Version != contStateVersion || st.Sum != st.checksum() {
		c.removeState()
		c.refused = "continuity record is corrupt or altered"
		return c.refused
	}
	boot, err := p.currentBootID()
	nowBoot, err2 := p.bootNow()
	switch {
	case err != nil || err2 != nil:
		c.refused = "boot identity or monotonic clock unavailable"
	case !p.mode.Active:
		c.refused = "Phase 3 is not active"
	case st.BootID != boot:
		c.refused = "continuity record is from another boot (the kernel authorization set did not survive it)"
	case st.TenantID != p.mode.TenantID || st.SiteID != p.mode.SiteID || st.ApplianceID != p.mode.ApplianceID:
		c.refused = "continuity record belongs to another tenant, site or appliance"
	case st.AssignGen < p.mode.AssignGen:
		c.refused = "continuity record predates the assignment in force"
	case st.LastPlanBoot > nowBoot:
		c.refused = "continuity record claims a plan from the future of this boot"
	}
	if c.refused != "" {
		c.removeState()
		return c.refused
	}
	if st.Sessions == nil {
		st.Sessions = map[string]*contSess{}
	}
	if st.Budgets == nil {
		st.Budgets = map[string]*contBudget{}
	}
	c.st = &st
	return ""
}

// ---- revocation -------------------------------------------------------------------------------------------------------

func (c *continuity) pruneJournal(now time.Time, asOf time.Time) (changed bool) {
	keep := c.journal[:0:0]
	for _, e := range c.journal {
		if now.Sub(e.At) > contRevokeKeep {
			continue
		}
		// A block read from the database AFTER the revocation already reflects it; the entry has done its job.
		if !asOf.IsZero() && e.At.Before(asOf) {
			continue
		}
		keep = append(keep, e)
	}
	if len(keep) > contRevokeMax {
		keep = keep[len(keep)-contRevokeMax:]
	}
	changed = len(keep) != len(c.journal)
	c.journal = keep
	return changed
}

func (c *continuity) revoked(s *contSess) (string, bool) {
	for _, e := range c.journal {
		switch {
		case e.SessionID != "" && e.SessionID == s.SessionID:
			return e.Reason, true
		case e.EntitlementID != "" && e.EntitlementID == s.EntitlementID:
			return e.Reason, true
		case e.IP != "" && e.IP == s.IP && (e.Bridge == "" || e.Bridge == s.Bridge):
			return e.Reason, true
		}
	}
	return "", false
}

// contRevoke records a revocation durably and drops what it names. The journal write comes first: a revocation that is not
// on disk is not acknowledged, so a restart cannot lose it. Caller holds p.mu. It returns an error when the journal could
// not be written; the in-memory effect has happened regardless (fail closed).
func (p *phase3Shaping) contRevoke(ctx context.Context, e contRevocation) error {
	c := p.cont
	if c == nil {
		return nil
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	c.journal = append(c.journal, e)
	if len(c.journal) > contRevokeMax {
		c.journal = c.journal[len(c.journal)-contRevokeMax:]
	}
	jerr := c.saveJournal()
	if c.st != nil {
		p.contRefreshUsage(ctx) // the revoked session's last bytes stay on the entitlement's account
		for k, s := range c.st.Sessions {
			if reason, hit := c.revoked(s); hit {
				c.drop(k, "revoked: "+reason)
				if ip := net.ParseIP(s.IP); ip != nil {
					if derr := p.denyAccess(ctx, s.Bridge, ip); derr != nil {
						slog.Warn("netd: continuity could not prove a revoked authorization gone", "session", s.SessionID, "err", derr)
					}
				}
			}
		}
	}
	return jerr
}

func (c *continuity) drop(key, reason string) {
	if c.st == nil {
		return
	}
	if s, ok := c.st.Sessions[key]; ok {
		if c.dropped == nil {
			c.dropped = map[string]string{}
		}
		if len(c.dropped) > 512 {
			c.dropped = map[string]string{}
		}
		c.dropped[s.SessionID] = reason
		if s.HasBase {
			if b := c.st.Budgets[s.EntitlementID]; b != nil {
				b.Gone += s.LastUsed
			}
		}
		delete(c.st.Sessions, key)
		c.dirty = true
	}
}

// ---- acceptance -------------------------------------------------------------------------------------------------------

// contNotePlan is called by submit after a plan was admitted (converged or not). Caller holds p.mu.
func (p *phase3Shaping) contNotePlan(env shapeplan.Envelope, now time.Time) {
	c := p.cont
	if !c.enabled() {
		return
	}
	nowBoot, err := p.bootNow()
	if err != nil {
		c.removeStateLocked()
		return
	}
	// A plan has arrived: the outage clock restarts, whether or not it carries a block.
	if c.st != nil {
		c.st.LastPlanBoot = nowBoot
		c.dirty = true
	}
	c.outageSince = time.Time{}
	if env.Continuity == nil {
		// The producer is not vouching for anything new (setting not yet read, accounting stalled). What is held is kept and
		// never extended: every deadline in it was fixed when it was accepted.
		return
	}
	if env.Continuity.IsOff() {
		if c.st != nil {
			slog.Info("netd: the producer switched outage continuity off; continuity is cleared")
		}
		c.removeStateLocked()
		c.refused = ""
		return
	}
	if reason, ok := shapeplan.ValidateContinuity(*env.Continuity, now); !ok {
		slog.Warn("netd: continuity block refused; strict behaviour applies", "reason", reason)
		c.removeStateLocked()
		c.refused = reason
		return
	}
	c.refused = ""
	boot, berr := p.currentBootID()
	if berr != nil {
		c.removeStateLocked()
		return
	}
	asOf := env.Continuity.AsOf
	if c.pruneJournal(now, asOf) {
		// The file must mirror the memory. An entry a database read has already absorbed is matched by ADDRESS, so left on disk it
		// would be reloaded by the next netd start (an outage restart, an upgrade) and applied to whoever holds that address by then:
		// seen in the lab as five healthy guests cut at a netd restart by staff disconnects made hours earlier.
		if err := c.saveJournal(); err != nil {
			slog.Warn("netd: the pruned revocation journal could not be saved", "err", err)
		}
	}
	hard := map[string]*time.Time{}
	for _, s := range env.Continuity.Sessions {
		hard[s.SessionID] = s.HardEnd
	}
	budget := map[string]int64{}
	for _, b := range env.Continuity.Budgets {
		budget[b.EntitlementID] = b.RemainingBytes
	}
	base, sampled := c.baselineAt(asOf)
	window := time.Duration(env.Continuity.WindowSeconds) * time.Second

	ns := &contState{Version: contStateVersion, TenantID: env.TenantID, SiteID: env.SiteID, ApplianceID: env.ApplianceID,
		AssignGen: env.AssignmentGen, BootID: boot, PlanGen: env.PlanGeneration, LastPlanBoot: nowBoot,
		WindowSec: env.Continuity.WindowSeconds, AsOf: asOf,
		Sessions: map[string]*contSess{}, Budgets: map[string]*contBudget{}}
	for _, s := range env.Sessions {
		if !s.Entitled || s.IP == "" || s.EntitlementID == "" {
			continue
		}
		he, listed := hard[s.SessionID]
		if !listed {
			continue
		}
		key := classKey(s.Bridge, s.SessionID)
		if _, renewable := p.memo.renewable[key]; !renewable {
			continue // the last walk did not prove this session in force
		}
		mc, ok := p.classes[key]
		if !ok {
			continue
		}
		cs := &contSess{SessionID: s.SessionID, Bridge: s.Bridge, IP: s.IP, MAC: s.MAC, EntitlementID: s.EntitlementID,
			Minor: mc.Minor, Epoch: mc.Epoch}
		if _, revokedNow := c.revoked(cs); revokedNow {
			continue
		}
		// The shortest of: the window, the producer's hard end, the plan's own access boundary.
		rem := window
		for _, end := range []*time.Time{he, s.AccessEndsAt} {
			if end != nil {
				if left := end.Sub(now); left < rem {
					rem = left
				}
			}
		}
		if rem < time.Second {
			continue
		}
		cs.DeadlineBoot = nowBoot + rem.Milliseconds()
		if rb, budgeted := budget[s.EntitlementID]; budgeted {
			b, have := base[key]
			if !have {
				// A session admitted after the baseline sample: its class did not exist when the sample was taken, so it is
				// measured from zero (everything it ever carried counts, some of it twice: never under). A class that existed but
				// could not be read, or no sample at all, still gets no continuity.
				if _, existed := sampled[key]; sampled == nil || existed {
					continue
				}
				b = contCount{epoch: mc.Epoch}
			}
			if b.epoch != mc.Epoch {
				b.bytes = 0 // a class created after the sample counts from zero
			}
			cs.Base, cs.HasBase = b.bytes, true
			if ns.Budgets[s.EntitlementID] == nil {
				ns.Budgets[s.EntitlementID] = &contBudget{Remaining: rb}
			}
		}
		ns.Sessions[key] = cs
	}
	// A budget carried by the previous record that the new block repeats keeps its exhausted flag only if it is the same
	// AsOf; a newer AsOf came from the database and supersedes it.
	if c.st != nil && c.st.AsOf.Equal(asOf) {
		for id, b := range ns.Budgets {
			if old := c.st.Budgets[id]; old != nil {
				b.Gone = old.Gone
				if old.Exhausted {
					b.Exhausted = true
				}
			}
		}
		for k, s := range ns.Sessions {
			if b := ns.Budgets[s.EntitlementID]; b != nil && b.Exhausted {
				delete(ns.Sessions, k)
			}
		}
	}
	c.st = ns
	c.scopeLogged = false
	c.dirty = true
	if time.Since(c.lastSaved) >= contSaveEvery {
		if err := c.saveState(); err != nil {
			slog.Warn("netd: the continuity record could not be saved", "err", err)
		}
	}
	// Leaving an outage: the plan path is back in charge.
	c.outageSince = time.Time{}
}

func (c *continuity) removeStateLocked() { c.removeState(); c.outageSince = time.Time{} }

// baselineAt returns, per class, the counter of the latest sample taken at or before asOf (plus a small slack), and the classes
// that existed when it was taken (nil when there is no such sample, or it predates the record of existing classes).
func (c *continuity) baselineAt(asOf time.Time) (map[string]contCount, map[string]int64) {
	var pick *contSample
	for i := range c.samples {
		if !c.samples[i].at.After(asOf.Add(contBaselineSlack)) {
			pick = &c.samples[i]
		}
	}
	if pick == nil {
		return map[string]contCount{}, nil
	}
	return pick.bytes, pick.classes
}

// ---- sampling and the outage tick ---------------------------------------------------------------------------------------

// classCounters reads down+up counters for every managed class on the given bridges, keyed like p.classes.
func (p *phase3Shaping) classCounters(ctx context.Context, bridges map[string]bool) (map[string]contCount, error) {
	out := map[string]contCount{}
	for b := range bridges {
		down, err := p.shp.ReadClasses(ctx, b)
		if err != nil {
			return nil, err
		}
		up, err := p.shp.ReadClasses(ctx, shape.IFBName(b))
		if err != nil {
			return nil, err
		}
		for key, mc := range p.classes {
			if mc.Bridge != b {
				continue
			}
			d, okd := down[mc.Minor]
			u, oku := up[mc.Minor]
			if !okd || !oku {
				continue
			}
			out[key] = contCount{epoch: mc.Epoch, bytes: d.Bytes + u.Bytes}
		}
	}
	return out, nil
}

func (p *phase3Shaping) managedBridgeSet() map[string]bool {
	out := map[string]bool{}
	for _, mc := range p.classes {
		out[mc.Bridge] = true
	}
	return out
}

// contScopeOK reports whether the held record still belongs to the Phase-3 scope in force. Caller holds p.mu. When it does not
// (Phase 3 went inactive, or another tenant, site, appliance or an older assignment was adopted) the record is cleared and nothing
// is renewed any more: the leases run down, fail closed. Logged once per record.
func (p *phase3Shaping) contScopeOK() bool {
	c := p.cont
	st := c.st
	if st == nil {
		return true
	}
	if p.mode.Active && st.TenantID == p.mode.TenantID && st.SiteID == p.mode.SiteID &&
		st.ApplianceID == p.mode.ApplianceID && st.AssignGen >= p.mode.AssignGen {
		return true
	}
	if !c.scopeLogged {
		c.scopeLogged = true
		slog.Warn("netd: the Phase-3 scope changed or went inactive; outage continuity is cleared and nothing is renewed",
			"active", p.mode.Active, "record_tenant", st.TenantID, "tenant", p.mode.TenantID,
			"record_site", st.SiteID, "site", p.mode.SiteID, "record_assignment_gen", st.AssignGen, "assignment_gen", p.mode.AssignGen)
	}
	c.removeStateLocked()
	c.refused = "the Phase-3 scope changed or went inactive"
	return false
}

// contRefreshUsage re-reads the class counters and refreshes every budgeted session's LastUsed. Best effort: a failed read leaves
// the earlier figures. Caller holds p.mu. It reports whether the counters were read, and returns them.
func (p *phase3Shaping) contRefreshUsage(ctx context.Context) (map[string]contCount, bool) {
	c := p.cont
	if c == nil || c.st == nil || !hasBudgets(c.st) {
		return nil, false
	}
	cnt, err := p.classCounters(ctx, p.managedBridgeSet())
	if err != nil {
		return nil, false
	}
	for k, s := range c.st.Sessions {
		if !s.HasBase {
			continue
		}
		cur, ok := cnt[k]
		if !ok || cur.epoch != s.Epoch {
			continue
		}
		u := cur.bytes
		if cur.bytes >= s.Base {
			u = cur.bytes - s.Base
		} // a counter that went backwards counts in full
		if u > s.LastUsed {
			s.LastUsed = u
		}
	}
	return cnt, true
}

// contTick is the governor. It is called every phase3RenewEvery. It reports how many authorizations it renewed.
func (p *phase3Shaping) contTick(ctx context.Context, now time.Time) int {
	c := p.cont
	if !c.enabled() || p.gate == nil || p.shp == nil {
		return 0
	}
	nowBoot, err := p.bootNow()
	if err != nil {
		return 0 // unmeasurable time: nothing is extended
	}

	// Phase 1 (locked): healthy sampling, outage detection, the sessions to probe.
	p.mu.Lock()
	c.lastTick = now
	if !p.contScopeOK() || c.st == nil {
		p.mu.Unlock()
		return 0
	}
	tenant, site := p.mode.TenantID, p.mode.SiteID
	age := time.Duration(nowBoot-c.st.LastPlanBoot) * time.Millisecond
	if age < contTakeoverAge {
		// Plans are arriving. Keep the samples a future budget baseline will be taken from.
		if now.Sub(c.lastSample) >= contSampleEvery && p.hasConverged {
			if cnt, err := p.classCounters(ctx, p.managedBridgeSet()); err == nil {
				known := make(map[string]int64, len(p.classes))
				for k, mc := range p.classes {
					known[k] = mc.Epoch
				}
				c.samples = append(c.samples, contSample{at: now, bytes: cnt, classes: known})
				if len(c.samples) > contSampleKeep {
					c.samples = c.samples[len(c.samples)-contSampleKeep:]
				}
				c.lastSample = now
			}
		}
		if c.dirty && time.Since(c.lastSaved) >= contSaveEvery {
			_ = c.saveState()
		}
		p.mu.Unlock()
		return 0
	}
	if c.outageSince.IsZero() {
		c.outageSince = now
		slog.Warn("netd: plans have stopped; outage continuity is bounding the sessions the last plan covered",
			"plan_age_s", int(age/time.Second), "sessions", len(c.st.Sessions), "window_s", c.st.WindowSec)
	}
	ids := make([]string, 0, len(c.st.Sessions))
	for _, s := range c.st.Sessions {
		ids = append(ids, s.SessionID)
	}
	probe := c.probe
	p.mu.Unlock()

	// Phase 2 (unlocked): ask the database. A hung query must not hold revocations behind it.
	var notEntitled map[string]bool
	probeOK := false
	if probe != nil && len(ids) > 0 {
		pctx, cancel := context.WithTimeout(ctx, contProbeTimeout)
		notEntitled = map[string]bool{}
		probeOK = true
		sort.Strings(ids)
		for i := 0; i < len(ids) && probeOK; i += contProbeChunk {
			j := i + contProbeChunk
			if j > len(ids) {
				j = len(ids)
			}
			m, perr := probe.NotEntitled(pctx, tenant, site, ids[i:j])
			if perr != nil {
				probeOK = false
				break
			}
			for k, v := range m {
				if v {
					notEntitled[k] = true
				}
			}
		}
		cancel()
	}

	// Phase 3 (locked): apply.
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.contScopeOK() || c.st == nil {
		return 0
	}
	if p.mode.TenantID != tenant || p.mode.SiteID != site {
		return 0 // the scope moved while the database was being asked: the answer belongs to another scope
	}
	// A plan may have arrived while the database was being asked: the plan path is back in charge, and this tick must not overlay it.
	if nb, berr := p.bootNow(); berr != nil || time.Duration(nb-c.st.LastPlanBoot)*time.Millisecond < contTakeoverAge {
		return 0
	}
	c.probeOK = probeOK
	if probe != nil && !probeOK {
		c.probeFailures++
	} else {
		c.probeFailures = 0
	}
	ownerSaved := p.owner
	if p.owner.src != nil {
		p.owner.src = &passLeases{src: ownerSaved.src}
		defer func() { p.owner = ownerSaved }()
	}

	// 3-pre. meter first, so that every session dropped below leaves its final usage behind in the entitlement's account.
	meterOK := true
	var counters map[string]contCount
	if hasBudgets(c.st) {
		cnt, ok := p.contRefreshUsage(ctx)
		if !ok {
			meterOK = false
			slog.Warn("netd: continuity cannot read the class counters; budgeted sessions are not renewed")
		} else {
			counters = cnt
		}
	}
	var deny []contSess
	if meterOK && counters != nil {
		for k, s := range c.st.Sessions {
			if !s.HasBase {
				continue
			}
			if cur, ok := counters[k]; !ok || cur.epoch != s.Epoch {
				c.drop(k, "the traffic class is gone or restarted; it can no longer be metered")
				deny = append(deny, *s)
			}
		}
	}

	// 3a. who is out: deadline, revocation, the database's word.
	for k, s := range c.st.Sessions {
		switch {
		case nowBoot >= s.DeadlineBoot:
			c.drop(k, "continuity window or hard end reached")
			deny = append(deny, *s)
		case notEntitled[s.SessionID]:
			c.drop(k, "the database no longer lists the session as entitled")
			deny = append(deny, *s)
		default:
			if reason, hit := c.revoked(s); hit {
				c.drop(k, "revoked: "+reason)
				deny = append(deny, *s)
			}
		}
	}

	// 3b. budgets: what the live sessions used plus what departed sessions used. Never decreases.
	if meterOK {
		for id, b := range c.st.Budgets {
			total := b.Gone
			for _, s := range c.st.Sessions {
				if s.HasBase && s.EntitlementID == id {
					total += s.LastUsed
				}
			}
			if total > b.Used {
				b.Used = total
			}
			if !b.Exhausted && (b.Remaining <= 0 || b.Used >= uint64(b.Remaining)) {
				b.Exhausted = true
				slog.Warn("netd: an entitlement's byte budget is spent during the outage; its sessions are being removed", "entitlement", id,
					"used_bytes", b.Used, "budget_bytes", b.Remaining)
			}
			if b.Exhausted {
				for k, s := range c.st.Sessions {
					if s.EntitlementID == id {
						c.drop(k, "data allowance spent during the outage")
						deny = append(deny, *s)
					}
				}
			}
		}
	}

	// 3c. explicit removal, so the end is exact and does not wait for the element to run down.
	for _, s := range deny {
		if ip := net.ParseIP(s.IP); ip != nil {
			if derr := p.denyAccess(ctx, s.Bridge, ip); derr != nil {
				slog.Warn("netd: continuity could not prove an authorization removed", "session", s.SessionID, "err", derr)
			}
		}
	}

	// 3d. renew what is left, only where the element is present, ownership is proven, and the session is metered if budgeted.
	view := p.snapshotGate(ctx)
	var reqs []gateLease
	skipped := map[string]int{}
	for k, s := range c.st.Sessions {
		ip := net.ParseIP(s.IP)
		if ip == nil {
			c.drop(k, "bad address")
			continue
		}
		if s.HasBase && !meterOK {
			skipped["counters_unreadable"]++
			continue
		}
		want := time.Duration(s.DeadlineBoot-nowBoot) * time.Millisecond
		if want > phase3LeaseTTL {
			want = phase3LeaseTTL
		}
		if s.MAC != "" {
			ev := p.owner.Evidence(ctx, ip, s.MAC)
			switch ev.State {
			case addressForeign:
				c.drop(k, "the address now belongs to another device")
				_ = p.denyAccess(ctx, s.Bridge, ip)
				continue
			case addressUnknown:
				skipped["dhcp_ownership_unproven"]++
				continue // no evidence, no renewal
			}
			// Ownership is proven: restore the gate's MAC binding from the record. After a netd-only restart the gate's map is empty, and a
			// renewal would otherwise refresh the IP element alone and let the MAC-bound element expire.
			if mb, ok := p.gate.(macBinder); ok {
				mb.BindMAC(s.Bridge, ip, s.MAC)
			}
			if !ev.Until.IsZero() {
				if left := ev.Until.Sub(now); left < want {
					want = left
				}
			}
		}
		want = want.Truncate(time.Second)
		if want < time.Second {
			skipped["deadline_reached"]++
			continue
		}
		el, present, known := view.lookup(s.Bridge, ip)
		if !known {
			skipped["gate_unreadable"]++
			continue
		}
		if !present {
			skipped["element_absent"]++
			continue // never create an authorization; an absent one was removed on purpose or by a reboot
		}
		if !needsRenewal(el.Expires, want) {
			continue
		}
		reqs = append(reqs, gateLease{Bridge: s.Bridge, IP: ip, TTL: want})
	}
	renewed := 0
	for i, rerr := range p.authorizeBatch(ctx, reqs) {
		if rerr != nil {
			slog.Warn("netd: continuity could not renew an authorization", "bridge", reqs[i].Bridge, "ip", reqs[i].IP.String(), "err", rerr)
			continue
		}
		renewed++
	}
	c.renewed += int64(renewed)
	if len(skipped) > 0 && !sameCounts(skipped, c.notRenewed) {
		slog.Warn("netd: continuity holds sessions it did not renew this tick", "by_reason", skipped,
			"held", len(c.st.Sessions), "renewed_now", renewed)
	}
	c.notRenewed = skipped
	if c.dirty {
		_ = c.saveState()
	}
	return renewed
}

func sameCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func hasBudgets(st *contState) bool {
	for _, b := range st.Budgets {
		if !b.Exhausted {
			return true
		}
	}
	return false
}

// ---- status -------------------------------------------------------------------------------------------------------------

type contStatus struct {
	Enabled      bool              `json:"enabled"`
	Active       bool              `json:"active"`
	WindowSec    int               `json:"window_seconds"`
	Sessions     int               `json:"sessions"`
	OutageSince  *time.Time        `json:"outage_since,omitempty"`
	ProbeOK      bool              `json:"database_probe_ok"`
	ProbeFails   int               `json:"database_probe_failures"`
	Renewed      int64             `json:"renewals"`
	Revocations  int               `json:"journalled_revocations"`
	Refused      string            `json:"refused,omitempty"`
	Budgets      map[string]uint64 `json:"budget_used_bytes,omitempty"`
	BudgetsLimit map[string]int64  `json:"budget_limit_bytes,omitempty"`
	Dropped      map[string]string `json:"dropped,omitempty"`
	// NotRenewed: sessions held but not renewed at the last outage tick, by reason (element_absent, dhcp_ownership_unproven, ...).
	NotRenewed map[string]int `json:"not_renewed,omitempty"`
}

func (p *phase3Shaping) contStatus() contStatus {
	c := p.cont
	if !c.enabled() {
		return contStatus{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := contStatus{Enabled: true, ProbeOK: c.probeOK, ProbeFails: c.probeFailures, Renewed: c.renewed,
		Revocations: len(c.journal), Refused: c.refused, Dropped: map[string]string{}}
	for k, v := range c.dropped {
		out.Dropped[k] = v
	}
	if len(c.notRenewed) > 0 {
		out.NotRenewed = map[string]int{}
		for k, v := range c.notRenewed {
			out.NotRenewed[k] = v
		}
	}
	if c.st != nil {
		out.WindowSec, out.Sessions = c.st.WindowSec, len(c.st.Sessions)
		out.Active = !c.outageSince.IsZero()
		if out.Active {
			t := c.outageSince
			out.OutageSince = &t
		}
		out.Budgets, out.BudgetsLimit = map[string]uint64{}, map[string]int64{}
		for id, b := range c.st.Budgets {
			out.Budgets[id], out.BudgetsLimit[id] = b.Used, b.Remaining
		}
	}
	return out
}

// continuityLoop runs contTick until ctx ends.
func (p *phase3Shaping) continuityLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			p.contTick(ctx, now)
		}
	}
}
