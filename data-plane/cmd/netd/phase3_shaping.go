package main

// Phase-3 shaping reconciliation: the only writer of Phase-3 tc state, driven by a whole desired state, never a delta.
// Teardown and stray removal run before shaping so ended access is never forwarded while capacity is handed out.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

type shapingPlanResponse struct {
	Accepted       bool     `json:"accepted"`
	Reason         string   `json:"reason,omitempty"`
	PlanGeneration int64    `json:"plan_generation,omitempty"`
	TornDown       int      `json:"torn_down"`
	StraysRemoved  int      `json:"strays_removed"`
	Shaped         int      `json:"shaped"`
	Failed         int      `json:"failed"`
	Degraded       bool     `json:"degraded"`
	Problems       []string `json:"problems,omitempty"`
}

// shaper is the tc surface netd uses; an interface so ordering and idempotency are provable without a kernel.
type shaper interface {
	EnsureBridgeInfra(ctx context.Context, bridge string) error
	// netd never installs a forwarding filter (ActivateSession) before the class has a durable generation and a registered accounting origin.
	PrepareSession(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int) error
	PrepareSessionIn(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int, groupCid string) error
	EnsureGroupClass(ctx context.Context, ifc, groupCid string, kbps int) error
	ActivateSession(ctx context.Context, bridge string, ip net.IP) error
	AbortSession(ctx context.Context, bridge string, ip net.IP) error
	// RemoveClassification strips tc classification only; it does not deny access.
	RemoveClassification(ctx context.Context, bridge string, ip net.IP) error
	ReRateSession(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int) error
	SessionForwarding(ctx context.Context, bridge string, ip net.IP) (bool, error)
	DeleteSession(ctx context.Context, bridge string, ip net.IP) error
	// ReadClasses reads what is actually installed; netd's memory misses exactly the dangerous leftovers.
	ReadClasses(ctx context.Context, device string) (map[int]shape.ClassBytes, error)
	DeleteSessionClass(ctx context.Context, bridge string, minor int) error
}

type phase3Shaping struct {
	mu  sync.Mutex
	shp shaper

	// mode is netd's own view of whether Phase 3 is live; netd does not trust the submitter.
	mode  phase3Mode
	authz shapingAuthz
	// store persists the last accepted plan so a replayed older plan cannot reinstate removed access.
	store   *planStore
	origins originRegistrar
	// gate is the packet-authorization half of enforcement (see phase3_gate.go).
	gate gate
	// legacy, when wired, is drained to empty each pass; nothing writes auth_ipv4 any more.
	legacy      legacySet
	enforcement enforcementRecorder
	owner       addressOwner
	// unverified: sessions not renewed because ownership could not be checked; their bounded leases run down.
	unverified map[string]bool
	// leaseFiles is optional; nil means health reports the symptom without the on-disk cause.
	leaseFiles leaseFileInspector

	lastApplied time.Time
	lastDegrade string
	// lastAccepted and lastConverged are distinct: a plan can be admitted yet fail to be put in force.
	lastAccepted  shapeplan.Accepted
	hasAccepted   bool
	lastConverged shapeplan.Accepted
	hasConverged  bool
	lastRejection string
	rejections    int64
	// epochs is the TC owner's generation per managed class; netd alone creates these classes, so it alone can say a counter series restarted.
	epochs map[string]int64
	// pending holds generations for classes not yet fully provisioned so a retry reuses the generation instead of re-baselining the origin.
	pending    map[string]int64
	minorOwner map[string]string
	// quarantined tracks activations that could not be proven durable; without it, failing closed once would let the next pass re-admit the same guest.
	quarantined map[string]*quarantineState
	// journal is write-ahead: fsynced before the guest is provisionally authorized so a crash cannot lose the bound.
	journal *activationJournal
	// secClock is monotonic and boot-relative on purpose: a wall clock moving backwards would lengthen the activation bound.
	secClock securityClock
	// unprovenUnknown: the activation journal was unreadable at startup, so an unproven activation is treated as having spent its grace.
	unprovenUnknown bool
	// classes/epochCeiling/bootID are the durable half; without them a restart or reboot re-issues epoch 1 and accounting stalls or misattributes.
	classes     map[string]managedClass
	bootID      string
	classStore  *classStore
	generations generationAllocator
	restoreNote string
	// endedTorn remembers torn-down sessions the plan still lists so their teardown is not repeated each pass.
	endedTorn   map[string]bool
	passClasses map[string]map[int]shape.ClassBytes
}

type passLeases struct {
	src    leaseSource
	done   bool
	leases []KeaLease
	err    error
}

// EmptyAnswerTrusted forwards the stability question to the real source: this wrapper only caches one lease list per pass, and hiding the
// source's judgement behind it would make every empty table read as trustworthy. A source that cannot judge keeps the established meaning.
func (l *passLeases) EmptyAnswerTrusted(ip net.IP) (bool, string) {
	if st, ok := l.src.(leaseStability); ok {
		return st.EmptyAnswerTrusted(ip)
	}
	return true, ""
}

func (l *passLeases) Leases() ([]KeaLease, error) {
	if !l.done {
		l.leases, l.err = l.src.Leases()
		l.done = true
	}
	return l.leases, l.err
}

// Any other mode value, including the empty string from older revisions, is PER_DEVICE.
const speedAllocationShared = "SHARED"

func classKey(bridge, sessionID string) string { return bridge + "|" + sessionID }

func minorKey(bridge string, minor int) string { return fmt.Sprintf("%s|%d", bridge, minor) }

// endSeries forgets a class's generation; a successor allocates a strictly greater one, so it never inherits the replaced class's checkpoint.
func (p *phase3Shaping) endSeries(bridge, sessionID string) {
	delete(p.epochs, classKey(bridge, sessionID))
	delete(p.classes, classKey(bridge, sessionID))
	// A half-allocated generation is void once the series ends; a returning session is a new class.
	delete(p.pending, classKey(bridge, sessionID))
	// The quarantine deliberately outlives the series: clearing it would let the next pass re-admit the guest.
}

// Epochs returns generations of currently managed classes only; torn-down classes must not look accountable to acctd.
func (p *phase3Shaping) Epochs() map[string]int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]int64, len(p.classes))
	for _, c := range p.classes {
		out[classKey(c.Bridge, c.SessionID)] = c.Epoch
	}
	return out
}

type errPlanRefused struct{ reason string }

func (e errPlanRefused) Error() string { return "plan refused: " + e.reason }

// submit is the whole admission path; it returns a refusal reason rather than applying a plan it only partly understands.
func (p *phase3Shaping) submit(ctx context.Context, env shapeplan.Envelope, now time.Time) (shapingPlanResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.mode.Active {
		// DARK: netd refuses on its own authority so the kill switch does not depend on another process.
		p.noteRejection("phase3_dark")
		return shapingPlanResponse{Accepted: false, Reason: "phase3_dark"}, errPlanRefused{"phase3_dark"}
	}
	last, hasLast := p.lastAccepted, p.hasAccepted
	if !hasLast && p.store != nil {
		// After a restart the in-memory history is empty but the durable one is not.
		last, hasLast = p.store.load()
	}
	scope := shapeplan.Scope{TenantID: p.mode.TenantID, SiteID: p.mode.SiteID,
		ApplianceID: p.mode.ApplianceID, AssignGen: p.mode.AssignGen}
	if reason, ok := shapeplan.Validate(env, scope, last, hasLast, now); !ok {
		p.noteRejection(reason)
		return shapingPlanResponse{Accepted: false, Reason: reason}, errPlanRefused{reason}
	}

	res := p.reconcileLocked(ctx, env, now)
	res.Accepted = true
	res.PlanGeneration = env.PlanGeneration

	accepted := shapeplan.Accepted{
		Generation: env.PlanGeneration, Hash: env.DesiredStateHash,
		TenantID: env.TenantID, SiteID: env.SiteID,
		AcceptedAt: now.UTC(), ExpiresAt: env.ExpiresAt.UTC(),
	}
	// Admission advances even on partial application so a replay of an older generation cannot reinstate revoked access.
	// Resubmitting the same generation and hash is allowed so the producer can retry until the kernel converges.
	p.lastAccepted, p.hasAccepted = accepted, true
	if p.store != nil {
		p.store.save(accepted)
	}
	// Convergence is recorded only when nothing failed.
	if !res.Degraded {
		p.lastConverged, p.hasConverged = accepted, true
	}
	return res, nil
}

func (p *phase3Shaping) noteRejection(reason string) {
	p.rejections++
	p.lastRejection = reason
}

func (p *phase3Shaping) reconcileLocked(ctx context.Context, env shapeplan.Envelope, now time.Time) shapingPlanResponse {
	var res shapingPlanResponse
	if p.minorOwner == nil {
		p.minorOwner = map[string]string{}
	}
	if p.endedTorn == nil {
		p.endedTorn = map[string]bool{}
	}
	// One DHCP lease list per pass, not per session per tick.
	if p.owner.src != nil {
		saved := p.owner
		p.owner.src = &passLeases{src: saved.src}
		defer func() { p.owner = saved }()
	}
	p.passClasses = nil
	defer func() { p.passClasses = nil }()

	desired := map[string]map[int]shapeplan.Session{}
	// Bridges come from the plan's declaration: a bridge with no sessions is the one most likely to hold a forgotten class.
	bridges := map[string]bool{}
	for _, b := range env.ManagedBridges {
		if b != "" {
			bridges[b] = true
		}
	}
	var tear []shapeplan.Session

	for _, s := range env.Sessions {
		if s.Bridge == "" {
			res.Failed++
			res.Problems = append(res.Problems, "session "+s.SessionID+": no bridge")
			continue
		}
		if !bridges[s.Bridge] {
			// A session on an undeclared bridge is a producer defect: its class would never be removed.
			res.Failed++
			res.Problems = append(res.Problems, "session "+s.SessionID+": bridge "+s.Bridge+" is not declared managed")
			continue
		}
		if !s.Entitled {
			tear = append(tear, s)
			continue
		}
		ip := net.ParseIP(s.IP)
		if ip == nil || s.DownKbps <= 0 || s.UpKbps <= 0 {
			// A session with no addressing or rates stays unshaped; a zero rate would be a silent full-speed pass.
			res.Failed++
			res.Problems = append(res.Problems, "shape: unusable plan entry for session "+s.SessionID)
			continue
		}
		minor, ok := shape.MinorForIP(ip)
		if !ok {
			res.Failed++
			res.Problems = append(res.Problems, "shape: unshapeable address for session "+s.SessionID)
			continue
		}
		if desired[s.Bridge] == nil {
			desired[s.Bridge] = map[int]shapeplan.Session{}
		}
		if other, clash := desired[s.Bridge][minor]; clash && other.SessionID != s.SessionID {
			// Two live sessions claiming one class is a producer defect; installing either would misattribute traffic, so neither is installed.
			res.Failed++
			res.Problems = append(res.Problems,
				fmt.Sprintf("class conflict on %s: sessions %s and %s both map to minor %d", s.Bridge, other.SessionID, s.SessionID, minor))
			delete(desired[s.Bridge], minor)
			continue
		}
		desired[s.Bridge][minor] = s
	}

	stillTorn := map[string]bool{}
	for _, s := range tear {
		ip := net.ParseIP(s.IP)
		if ip == nil {
			res.Failed++
			res.Problems = append(res.Problems, "tear: unusable addressing for session "+s.SessionID)
			continue
		}
		// A handover is not a teardown: when the plan hands the address to a newer session, kernel teardown would take the new guest offline and reset its accounting series every pass.
		// Only the durable half runs; the kernel half is left to the new owner.
		if minor, ok := shape.MinorForIP(ip); ok {
			if next, taken := desired[s.Bridge][minor]; taken && next.SessionID != s.SessionID {
				p.endSeries(s.Bridge, s.SessionID)
				p.markEnded(ctx, s.SessionID, s.EndReason)
				delete(p.classes, classKey(s.Bridge, s.SessionID))
				res.TornDown++
				continue
			}
		}
		// Already ended: the producer keeps listing it, and the stray sweeps catch anything still installed.
		if p.endedTorn[classKey(s.Bridge, s.SessionID)] {
			stillTorn[classKey(s.Bridge, s.SessionID)] = true
			continue
		}
		p.endSeries(s.Bridge, s.SessionID)
		// Deny packet authorization before tc teardown so a failed class delete cannot leave a guest online.
		if err := p.denyAccess(ctx, s.Bridge, ip); err != nil {
			res.Failed++
			res.Problems = append(res.Problems,
				"tear "+s.SessionID+": PACKET AUTHORIZATION NOT PROVEN REMOVED: "+err.Error())
			continue
		}
		if err := p.shp.DeleteSession(ctx, s.Bridge, ip); err != nil {
			// Access is already denied; this is degraded cleanup, still reported because a leftover class distorts accounting.
			res.Failed++
			res.Problems = append(res.Problems, "tear "+s.SessionID+": access denied but tc cleanup failed: "+err.Error())
			continue
		}
		if p.markEnded(ctx, s.SessionID, s.EndReason) {
			stillTorn[classKey(s.Bridge, s.SessionID)] = true
		}
		if minor, ok := shape.MinorForIP(ip); ok {
			delete(p.minorOwner, minorKey(s.Bridge, minor))
		}
		delete(p.classes, classKey(s.Bridge, s.SessionID))
		res.TornDown++
	}
	p.endedTorn = stillTorn

	// 2a. Remove stray nft elements the plan does not claim, before the tc stray sweep: deleting a class first would leave its authorized address forwarding unshaped and uncounted.
	// Only the Phase-3 set is enumerated (once per pass, reused for renewal); legacy auth_ipv4 is never touched here.
	view := p.snapshotGate(ctx)
	if p.gate != nil {
		if !view.ok {
			res.Failed++
			res.Problems = append(res.Problems, "phase-3 authorization scan failed")
		} else {
			authorized := make([]gateElem, 0, len(view.byKey))
			for _, el := range view.byKey {
				authorized = append(authorized, el)
			}
			sort.Slice(authorized, func(i, j int) bool {
				return gateKey(authorized[i].Bridge, authorized[i].IP) < gateKey(authorized[j].Bridge, authorized[j].IP)
			})
			for _, el := range authorized {
				if el.IP == nil {
					continue
				}
				minor, ok := shape.MinorForIP(el.IP)
				if !ok {
					continue
				}
				if _, want := desired[el.Bridge][minor]; want {
					continue
				}
				if err := p.denyAccess(ctx, el.Bridge, el.IP); err != nil {
					res.Failed++
					res.Problems = append(res.Problems,
						"stray authorization "+el.IP.String()+" on "+el.Bridge+" NOT removed: "+err.Error())
					continue
				}
				delete(view.byKey, gateKey(el.Bridge, el.IP))
				res.StraysRemoved++
			}
		}
	}

	// 2b. REMOVE STRAY CLASSES — everything installed on a managed bridge that the plan does not claim.
	for _, bridge := range sortedKeys(bridges) {
		installed, err := p.shp.ReadClasses(ctx, bridge)
		if err != nil {
			// Unknown installed state means unknown strays; say so rather than report an exact-looking clean apply.
			res.Failed++
			res.Problems = append(res.Problems, "stray scan failed on "+bridge+": "+err.Error())
			continue
		}
		for _, minor := range sortedInts(installed) {
			if minor < shape.GuestMinorBase || minor > shape.GuestMinorMax {
				continue
			}
			if _, want := desired[bridge][minor]; want {
				continue
			}
			owner := p.minorOwner[minorKey(bridge, minor)]
			if err := p.shp.DeleteSessionClass(ctx, bridge, minor); err != nil {
				res.Failed++
				res.Problems = append(res.Problems, fmt.Sprintf("stray %d on %s: %s", minor, bridge, err.Error()))
				continue
			}
			if owner != "" {
				p.endSeries(bridge, owner)
			}
			delete(p.minorOwner, minorKey(bridge, minor))
			res.StraysRemoved++
		}
	}

	// 2c. Reconcile the legacy set to empty: nothing writes auth_ipv4 any more, so every element is an ungoverned grant.
	if p.legacy != nil {
		n, err := p.legacy.Drain(ctx)
		if n > 0 {
			res.StraysRemoved += n
			slog.Warn("phase3: removed authorizations from the legacy auth_ipv4 set; nothing should write it", "count", n)
		}
		if err != nil {
			res.Failed++
			res.Problems = append(res.Problems, "legacy authorization set not drained: "+err.Error())
		}
	}

	// 3. Shape what must be forwarded, staged and fail-closed (see provisionSession); tc class inventories are read once per device per pass.
	p.passClasses = map[string]map[int]shape.ClassBytes{}
	for _, bridge := range sortedKeys(bridges) {
		byMinor := desired[bridge]
		if len(byMinor) == 0 {
			continue
		}
		if err := p.shp.EnsureBridgeInfra(ctx, bridge); err != nil {
			res.Failed++
			res.Problems = append(res.Problems, "bridge "+bridge+": "+err.Error())
			continue
		}
		for _, minor := range sortedIntKeys(byMinor) {
			p.provisionSession(ctx, bridge, minor, byMinor[minor], view, now, &res)
		}
	}

	// Write the durable inventory after convergence so a restart resumes from what is installed; a failed write is reported because the next start re-generates every class and loses accounting continuity.
	if p.classStore != nil {
		if err := p.classStore.save(p.snapshot()); err != nil {
			res.Failed++
			res.Problems = append(res.Problems, "durable class state not written: "+err.Error())
			// The activation clock is in that file; if it cannot be written, an unprovable activation gets no fresh grace after restart.
			p.unprovenUnknown = true
		}
	}

	res.Degraded = res.Failed > 0
	p.lastApplied = now
	p.lastDegrade = ""
	if res.Degraded {
		p.lastDegrade = res.Problems[0]
	}
	return res
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedInts(m map[int]shape.ClassBytes) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func sortedIntKeys(m map[int]shapeplan.Session) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// phase3ShapingHandler serves POST /v1/phase3/shaping; identity is SO_PEERCRED, since a header is a claim, not an identity.
func (s *server) phase3ShapingHandler(w http.ResponseWriter, r *http.Request) {
	pc, ok := r.Context().Value(peerConnKey{}).(producerIdentity)
	var credErr error
	if !ok {
		credErr = errors.New("connection carried no peer credentials")
	}
	if err := s.phase3.authz.authorize(pc, credErr); err != nil {
		s.phase3.mu.Lock()
		s.phase3.noteRejection("unauthorized_producer")
		s.phase3.mu.Unlock()
		slog.Warn("phase3 shaping submission refused", "reason", "unauthorized_producer", "uid", pc.UID, "err", err)
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden", "message": "caller is not the authorized shaping producer"})
		return
	}

	var env shapeplan.Envelope
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request", "message": "malformed shaping plan"})
		return
	}

	res, err := s.phase3.submit(r.Context(), env, time.Now())
	if err != nil {
		var refused errPlanRefused
		code := http.StatusConflict
		if errors.As(err, &refused) && refused.reason == shapeplan.ReasonUnsupportedContract {
			code = http.StatusBadRequest
		}
		slog.Warn("phase3 shaping plan refused", "reason", res.Reason, "generation", env.PlanGeneration)
		writeJSON(w, code, res)
		return
	}
	if res.Degraded {
		slog.Warn("phase3 shaping applied with problems", "failed", res.Failed, "first", res.Problems[0])
	}
	writeJSON(w, http.StatusOK, res)
}

// status reports the truthful enforcement state in five distinct states: "degraded=false, plan_stale=true" reads as healthy while nothing is known enforced.
const (
	shapingDark          = "DARK"
	shapingNoPlan        = "ACTIVE_NO_PLAN"
	shapingConverged     = "ACTIVE_FRESH_CONVERGED"
	shapingStale         = "ACTIVE_STALE"
	shapingDegradedState = "ACTIVE_DEGRADED"
)

// ACTIVE_FRESH_CONVERGED requires both halves verified; a session metered but not authorized (or vice versa) leaves res.Failed non-zero.

// enforcementStage is per-session progress, so an operator can tell which half has not converged.
const (
	stageRequested    = "REQUESTED"
	stageTCPrepared   = "TC_PREPARED"
	stageOriginPinned = "ORIGIN_PINNED"
	stageTCActive     = "TC_ACTIVE"
	stageAuthorized   = "NFT_AUTHORIZED"
	stageEnforced     = "ENFORCED"
	stageDeniedFailed = "DENIED_FAILED"
)

func (p *phase3Shaping) shapingState(now time.Time) (string, bool) {
	if !p.mode.Active {
		return shapingDark, false
	}
	switch {
	case p.lastDegrade != "":
		return shapingDegradedState, true
	case !p.hasConverged:
		// Live with nothing proven in force: either nothing was submitted or every submission failed.
		return shapingNoPlan, true
	case !p.lastConverged.ExpiresAt.After(now):
		// The producer has gone quiet: what is installed may be right, but nothing confirms it.
		return shapingStale, true
	default:
		return shapingConverged, false
	}
}

func (p *phase3Shaping) status() map[string]any {
	return p.statusWithEvidence(evidenceHealth{Available: true, CheckedAt: time.Now()}, false)
}

// statusWithEvidence folds in the ownership authority's health: with no lease manager the plane cannot claim CONVERGED, since it cannot tell whom state belongs to.
func (p *phase3Shaping) statusWithEvidence(ev evidenceHealth, probed bool) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	state, degraded := p.shapingState(now)
	if p.mode.Active && probed && !ev.Available {
		state, degraded = shapingDegradedState, true
	}
	out := map[string]any{
		"active":                 p.mode.Active,
		"state":                  state,
		"contract_version":       shapeplan.ContractVersion,
		"degraded":               degraded,
		"producer_authenticated": p.authz.configured,
		"refused_total":          p.rejections,
		// Sessions not renewed this pass for want of evidence; non-zero means guests are running down unconfirmed bounded authorizations.
		"ownership_unverified": len(p.unverified),
	}
	if !p.lastApplied.IsZero() {
		out["last_applied_at"] = p.lastApplied.UTC().Format(time.RFC3339)
	}
	if p.lastDegrade != "" {
		out["problem"] = p.lastDegrade
	}
	if p.lastRejection != "" {
		out["last_refusal"] = p.lastRejection
	}
	if p.hasAccepted {
		out["admitted_generation"] = p.lastAccepted.Generation
		out["admitted_at"] = p.lastAccepted.AcceptedAt.UTC().Format(time.RFC3339)
	}
	if p.hasConverged {
		out["converged_generation"] = p.lastConverged.Generation
		out["converged_at"] = p.lastConverged.AcceptedAt.UTC().Format(time.RFC3339)
		out["plan_expires_at"] = p.lastConverged.ExpiresAt.UTC().Format(time.RFC3339)
		out["plan_stale"] = !p.lastConverged.ExpiresAt.After(now)
	} else if p.mode.Active {
		out["plan_stale"] = true
	}
	out["managed_classes"] = len(p.classes)
	if p.restoreNote != "" {
		out["restore_note"] = p.restoreNote
	}
	// Last, so ordinary degrade text cannot overwrite it: while the evidence outage lasts nothing about ownership can be relied on.
	if probed {
		oe := map[string]any{"available": ev.Available, "leases": ev.Leases}
		if ev.Fault != "" {
			oe["fault"] = ev.Fault
			oe["detail"] = ev.Detail
		}
		out["ownership_evidence"] = oe
		if p.mode.Active && !ev.Available {
			out["problem"] = "DHCP ownership evidence unavailable (" + ev.Fault + "): " + ev.Detail
		}
	}
	return out
}

// phase3EpochsHandler serves current managed-class generations. This is control-plane state that profiles guest activity, so it needs the same authenticated producer identity as a submission and, while dark, refuses without disclosing anything.
func (s *server) phase3EpochsHandler(w http.ResponseWriter, r *http.Request) {
	pc, ok := r.Context().Value(peerConnKey{}).(producerIdentity)
	var credErr error
	if !ok {
		credErr = errors.New("connection carried no peer credentials")
	}
	if err := s.phase3.authz.authorize(pc, credErr); err != nil {
		s.phase3.mu.Lock()
		s.phase3.noteRejection("unauthorized_epoch_read")
		s.phase3.mu.Unlock()
		slog.Warn("phase3 class-generation read refused", "uid", pc.UID, "err", err)
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}
	if !s.phase3.mode.Active {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "phase3_dark"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"epochs": s.phase3.Epochs()})
}
