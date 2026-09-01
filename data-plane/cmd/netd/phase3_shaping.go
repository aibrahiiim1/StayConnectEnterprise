package main

// PHASE-3 SHAPING RECONCILIATION — the ONLY writer of Phase-3 tc state (see ADR-0002).
//
// acctd measures and derives; netd applies. Nothing else writes these classes, so there is no schedule on
// which two daemons can interleave a delete with an add for the same session.
//
// The contract is a WHOLE DESIRED STATE, never a delta. A caller cannot say "remove this one" or "add that
// one", because incremental instructions are exactly how two sides end up disagreeing about what is installed.
// Reconciliation therefore has three parts, in this order:
//
//	1. TEAR DOWN what the plan says must not be forwarded.
//	2. REMOVE STRAYS — managed classes present on a managed bridge that the plan does not mention at all.
//	   This is the part a delta protocol can never do: after a crash, or a restart that lost the in-memory
//	   map, a class belonging to a session that ended is still forwarding traffic and NOTHING would ever
//	   mention it again. Enumerating the kernel is the only way to find it.
//	3. SHAPE what must be forwarded.
//
// Teardown and stray removal come first so there is no window in which access that has ended is still
// forwarded while capacity is being handed out.

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

// shaper is the tc surface netd uses. It is an interface so the ordering, stray-removal and idempotency
// properties can be proven without a kernel.
type shaper interface {
	EnsureBridgeInfra(ctx context.Context, bridge string) error
	// The staged, accountable-before-forwarding provisioning surface. netd never installs a forwarding filter
	// (ActivateSession) until the class has a durable generation and a registered accounting origin.
	PrepareSession(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int) error
	// PrepareSessionIn stages the class inside a SHARED group (empty groupCid == PER_DEVICE, and is exactly
	// PrepareSession). It is separate rather than a changed signature so every existing caller and every
	// existing test keeps its meaning.
	PrepareSessionIn(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int, groupCid string) error
	// EnsureGroupClass creates or re-rates the aggregate class a SHARED entitlement's sessions borrow from.
	EnsureGroupClass(ctx context.Context, ifc, groupCid string, kbps int) error
	ActivateSession(ctx context.Context, bridge string, ip net.IP) error
	AbortSession(ctx context.Context, bridge string, ip net.IP) error
	// RemoveClassification strips tc classification only. It does NOT deny access (see phase3_gate.go).
	RemoveClassification(ctx context.Context, bridge string, ip net.IP) error
	ReRateSession(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int) error
	SessionForwarding(ctx context.Context, bridge string, ip net.IP) (bool, error)
	DeleteSession(ctx context.Context, bridge string, ip net.IP) error
	// ReadClasses enumerates what is ACTUALLY installed on a device. Reconciliation cannot be honest without
	// it: netd's own memory only knows what this process did, and the dangerous leftovers are precisely the
	// ones it does not remember.
	ReadClasses(ctx context.Context, device string) (map[int]shape.ClassBytes, error)
	// DeleteSessionClass removes a managed class by minor, for strays whose owning IP is no longer known.
	DeleteSessionClass(ctx context.Context, bridge string, minor int) error
}

// phase3Shaping holds the single writer's state.
type phase3Shaping struct {
	mu  sync.Mutex
	shp shaper

	// mode is netd's OWN view of whether Phase 3 is live and for whom. netd validates this itself rather than
	// trusting the submitter, so a dark appliance cannot be talked into mutating tc by anything upstream.
	mode phase3Mode
	// authz names the single local process allowed to submit plans.
	authz shapingAuthz
	// store remembers the last accepted plan across a restart, so a replayed or delayed older plan cannot
	// reinstate access that a newer plan removed.
	store *planStore
	// origins records a newly created class's accounting starting point, through the controlled operation,
	// before the guest can push traffic through it (see phase3_origin.go).
	origins originRegistrar
	// gate is the PACKET-AUTHORIZATION half of enforcement (see phase3_gate.go). netd owns both halves, so
	// there is no schedule on which one daemon admits a guest while another is still preparing to meter it.
	gate gate
	// enforcement records the confirmed kernel result durably, so a Session is only ever reported ACTIVE
	// after both halves are actually in force (see phase3_enforcement.go).
	enforcement enforcementRecorder
	// owner answers "does this address still belong to this session's device" from DHCP (phase3_address_owner.go).
	// A zero value answers UNKNOWN for everything, which is the behaviour every existing test expects.
	owner addressOwner
	// unverified is the set of sessions this writer is currently declining to renew because ownership could
	// not be checked. Their existing bounded leases are running down; confirmation before expiry resumes
	// normal renewal, and silence lets the kernel drop them.
	unverified map[string]bool
	// leaseFiles reports where Kea keeps its lease database, so a failed lease query can be explained by the
	// artifact that caused it instead of only by its error text (phase3_ownership_evidence.go). Optional: a
	// nil inspector simply means health reports the symptom without the on-disk cause.
	leaseFiles leaseFileInspector

	lastApplied time.Time
	lastDegrade string
	// ADMITTED and CONVERGED are different facts and must not share a field. A plan can be admitted (it is
	// current, in scope, correctly hashed) and still fail to be put in force. Recording only one of them
	// means either the anti-replay guard forgets a generation it accepted, or health claims a kernel state
	// that was never reached.
	lastAccepted  shapeplan.Accepted // admitted: what the anti-replay guard compares against
	hasAccepted   bool
	lastConverged shapeplan.Accepted // converged: what the kernel was actually driven to, cleanly
	hasConverged  bool
	lastRejection string
	rejections    int64
	// epochs is the TC owner's generation per managed class, keyed by bridge/session identity. netd is the
	// only process that creates or replaces these classes, so it is the only honest source of "this counter
	// series restarted". Without it, a counter that went backwards is ambiguous — a reset, a misread, or a
	// minor reused by a different guest — and accounting would have to guess.
	epochs map[string]int64
	// pending holds a class generation allocated for a class that has NOT yet fully provisioned (prepared and
	// registered but not yet activated+verified, or a provisioning that failed after allocation). It exists so
	// a retry of the same plan reuses the same generation instead of allocating a fresh one and re-baselining
	// the origin — which would be the very counter loss the origin exists to prevent. A generation only leaves
	// pending when its class is fully in force (moved to epochs) or its session is torn down.
	pending map[string]int64
	// minorOwner remembers which session a managed minor was installed for, so a stray removal can end that
	// session's counter series precisely instead of guessing.
	minorOwner map[string]string
	// quarantined tracks, per managed class, an activation that could not be PROVEN durable. It carries the
	// grace clock (a short provisional lease is tolerated while the promotion is retried), the strike count,
	// and the instant before which the session must not be re-admitted at all. Without it, failing closed once
	// would simply hand the next pass a clean slate to admit the same guest again, forever (see proveActive
	// and the quarantine check in provisionSession).
	quarantined map[string]*quarantineState
	// journal is the WRITE-AHEAD durability boundary for those records (phase3_journal.go). An attempt is
	// fsynced to disk BEFORE the guest is provisionally authorized, so a crash in the window between
	// admission and the end-of-pass inventory write cannot lose the bound.
	journal *activationJournal
	// secClock is the MONOTONIC, boot-relative security clock the activation bound is measured against
	// (phase3_securitytime.go). It is not the wall clock, deliberately: a wall clock that moves backwards
	// lengthens the bound.
	secClock securityClock
	// unprovenUnknown records that the durable activation journal could not be READ at startup. It is not a
	// substitute for the durable record — a write failure denies admission outright rather than being
	// remembered in memory — but an unreadable journal means the history of every session is unknown, and an
	// activation that cannot be proven must then be treated as having already spent its grace.
	unprovenUnknown bool
	// classes/epochCeiling/bootID are the DURABLE half of the same state (see phase3_classstate.go). Without
	// them a restart re-issues epoch 1 and accounting stalls; a reboot re-issues epoch 1 and a recreated
	// class is mistaken for the series a checkpoint still remembers.
	classes     map[string]managedClass
	bootID      string
	classStore  *classStore
	generations generationAllocator
	// restoreNote records why durable class state was not carried forward, so an operator can tell
	// "nothing was running" from "we could not prove what was running".
	restoreNote string
}

// speedAllocationShared is the plan-revision mode in which every session under one Entitlement borrows from a
// single aggregate ceiling. Any other value — including the empty string a pre-0059 revision produces — is
// PER_DEVICE, which is what those revisions were sold as.
const speedAllocationShared = "SHARED"

// classKey identifies one managed class for epoch purposes.
func classKey(bridge, sessionID string) string { return bridge + "|" + sessionID }

// minorKey identifies one installed class slot on one bridge.
func minorKey(bridge string, minor int) string { return fmt.Sprintf("%s|%d", bridge, minor) }

// bumpEpoch records that a managed class was (re)created or destroyed, so the next counter reading is judged
// against a new generation rather than compared with a series that no longer exists.
// endSeries forgets a class's generation. The series is over; whatever replaces it will allocate a NEW
// generation from the durable allocator, which is strictly greater than anything issued before — so a
// successor can never inherit the checkpoint of the class it replaced.
func (p *phase3Shaping) endSeries(bridge, sessionID string) {
	delete(p.epochs, classKey(bridge, sessionID))
	delete(p.classes, classKey(bridge, sessionID))
	// A generation half-allocated for a class that never came into force is void once the series ends: a
	// session that returns later is a genuinely new class and must allocate a fresh generation.
	delete(p.pending, classKey(bridge, sessionID))
	// NOTE: the quarantine deliberately OUTLIVES the series. It is a fact about a session whose durable
	// activation could not be proven, not about the tc class that was torn down because of it, and clearing it
	// here would let the next pass re-admit the guest as though nothing had happened.
}

// Epochs returns the generation of every class this appliance CURRENTLY manages.
//
// It is derived from the managed inventory, not from the raw epoch map, because the map also holds the
// generations of classes that have since been torn down. Reporting those would tell acctd it may account
// against a class the kernel no longer has — and the answer would look authoritative. A session whose class
// was removed and later returns gets a fresh generation from the monotonic ceiling, which is strictly higher
// than the one it had, so nothing is lost by forgetting it here.
func (p *phase3Shaping) Epochs() map[string]int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]int64, len(p.classes))
	for _, c := range p.classes {
		out[classKey(c.Bridge, c.SessionID)] = c.Epoch
	}
	return out
}

// errPlanRefused is returned when netd will not act on an envelope at all.
type errPlanRefused struct{ reason string }

func (e errPlanRefused) Error() string { return "plan refused: " + e.reason }

// submit is the whole admission path: DARK check, scope/version/freshness validation, then reconciliation.
// It returns a refusal reason rather than applying a plan it only partly understands.
func (p *phase3Shaping) submit(ctx context.Context, env shapeplan.Envelope, now time.Time) (shapingPlanResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.mode.Active {
		// DARK. netd refuses on its own authority. Trusting "acctd would never submit while dark" would make
		// the kill switch depend on a different process staying correct.
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
	// The ADMISSION record advances even when application was partial, so a replay of an older generation
	// still cannot reinstate revoked access. Re-submitting this SAME generation and hash is explicitly
	// allowed (shapeplan.Validate permits an equal generation with an equal hash), which is what lets the
	// producer retry until the kernel actually converges.
	p.lastAccepted, p.hasAccepted = accepted, true
	if p.store != nil {
		p.store.save(accepted)
	}
	// CONVERGENCE is recorded only when the kernel was driven to this state with nothing failing. Anything
	// else and health must keep saying so, however many times the same plan is admitted.
	if !res.Degraded {
		p.lastConverged, p.hasConverged = accepted, true
	}
	return res, nil
}

func (p *phase3Shaping) noteRejection(reason string) {
	p.rejections++
	p.lastRejection = reason
}

// reconcileLocked drives the kernel to the envelope's desired state. The caller holds p.mu.
func (p *phase3Shaping) reconcileLocked(ctx context.Context, env shapeplan.Envelope, now time.Time) shapingPlanResponse {
	var res shapingPlanResponse
	if p.minorOwner == nil {
		p.minorOwner = map[string]string{}
	}

	// desired[bridge][minor] = the session that must occupy that class.
	desired := map[string]map[int]shapeplan.Session{}
	// The bridges to reconcile come from the plan's DECLARATION, not from wherever its sessions happen to be.
	// A bridge with no sessions left is exactly the one most likely to be holding a class nobody remembers.
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
			// A session on a bridge the plan does not claim to manage is a producer defect: the applier would
			// install a class it has not been told to reconcile, and would never remove it again.
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
			// A session with no addressing or no rates is left UNSHAPED. Installing a zero rate would be a
			// silent full-speed pass — the opposite of what an unratable session should get.
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
			// Two live sessions claiming one class is a producer defect. Installing either would attribute
			// the other's traffic to it, so neither is installed.
			res.Failed++
			res.Problems = append(res.Problems,
				fmt.Sprintf("class conflict on %s: sessions %s and %s both map to minor %d", s.Bridge, other.SessionID, s.SessionID, minor))
			delete(desired[s.Bridge], minor)
			continue
		}
		desired[s.Bridge][minor] = s
	}

	// 1. TEAR DOWN — explicit, precise, by address.
	for _, s := range tear {
		ip := net.ParseIP(s.IP)
		if ip == nil {
			res.Failed++
			res.Problems = append(res.Problems, "tear: unusable addressing for session "+s.SessionID)
			continue
		}
		// A HANDOVER IS NOT A TEARDOWN.
		//
		// Teardown is BY ADDRESS — deny the element, delete the class — because that is the only precise way
		// to stop forwarding. But when the same plan hands that address to a newer session, the kernel state
		// at that address now belongs to the session taking it over, and removing it would take the NEW guest
		// offline and destroy the accounting series that was just created for them.
		//
		// It is not a hypothetical race. On the appliance, a device that authenticated as two rooms produced
		// exactly this plan every second: the superseded session's teardown revoked the surviving session's
		// authorization and deleted its class, the shaping step re-created both a moment later with a FRESH
		// generation, and the accounting origin was reset once per pass — a guest flickering offline and a
		// counter series that restarted every tick.
		//
		// So the durable half still runs (this session is genuinely over, and the record must say so with its
		// own reason), and the kernel half is left to its new owner.
		if minor, ok := shape.MinorForIP(ip); ok {
			if next, taken := desired[s.Bridge][minor]; taken && next.SessionID != s.SessionID {
				p.endSeries(s.Bridge, s.SessionID)
				p.markEnded(ctx, s.SessionID, s.EndReason)
				delete(p.classes, classKey(s.Bridge, s.SessionID))
				res.TornDown++
				continue
			}
		}
		p.endSeries(s.Bridge, s.SessionID) // the series ends here; a future class for this session is a new one
		// PACKET AUTHORIZATION FIRST. An entitlement that expired must stop reaching the internet even if tc
		// teardown then fails; the reverse order would leave a guest online because a class delete failed.
		if err := p.denyAccess(ctx, s.Bridge, ip); err != nil {
			res.Failed++
			res.Problems = append(res.Problems,
				"tear "+s.SessionID+": PACKET AUTHORIZATION NOT PROVEN REMOVED: "+err.Error())
			continue
		}
		if err := p.shp.DeleteSession(ctx, s.Bridge, ip); err != nil {
			// Access is already denied, so this is degraded CLEANUP, not continued guest access. It is still
			// reported: a leftover class distorts later accounting and must be seen.
			res.Failed++
			res.Problems = append(res.Problems, "tear "+s.SessionID+": access denied but tc cleanup failed: "+err.Error())
			continue
		}
		p.markEnded(ctx, s.SessionID, s.EndReason)
		if minor, ok := shape.MinorForIP(ip); ok {
			delete(p.minorOwner, minorKey(s.Bridge, minor))
		}
		delete(p.classes, classKey(s.Bridge, s.SessionID))
		res.TornDown++
	}

	// 2. REMOVE STRAYS — everything installed on a managed bridge that the plan does not claim.
	for _, bridge := range sortedKeys(bridges) {
		installed, err := p.shp.ReadClasses(ctx, bridge)
		if err != nil {
			// Unknown installed state means unknown strays. Say so rather than silently reconciling against
			// an assumption: an operator seeing "applied cleanly" would believe enforcement is exact.
			res.Failed++
			res.Problems = append(res.Problems, "stray scan failed on "+bridge+": "+err.Error())
			continue
		}
		for _, minor := range sortedInts(installed) {
			if minor < shape.GuestMinorBase || minor > shape.GuestMinorMax {
				continue // appliance infrastructure, never per-session state
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

	// 2b. REMOVE STRAY PACKET AUTHORIZATIONS — Phase-3 nft elements the plan does not claim.
	//
	// This is the nft half of the same argument that justifies tc stray removal, and it matters more: a stray
	// tc class meters traffic for a session that ended, but a stray nft element KEEPS THAT GUEST ONLINE. After
	// a crash, or a restart that lost the in-memory map, nothing else would ever mention it again.
	//
	// It enumerates ONLY the Phase-3 set. A legacy authorization lives in auth_ipv4, is never returned here,
	// and therefore cannot be removed by this loop no matter what the plan says.
	//
	// The set is enumerated ONCE per pass and the snapshot is reused for renewal decisions in step 3, so the
	// two halves judge the same installed state and the steady cost is one nft transaction per pass rather
	// than one per guest per tick.
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
				// Its tc class, if any, is removed by the tc stray loop above/below on the same pass.
				delete(view.byKey, gateKey(el.Bridge, el.IP))
				res.StraysRemoved++
			}
		}
	}

	// 3. SHAPE what must be forwarded — STAGED and FAIL-CLOSED (see provisionSession).
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

	// The durable inventory is written once the kernel has been driven to this desired state, so a restart
	// resumes from what is actually installed rather than from what was merely intended. A failed write is
	// reported: the next start would silently re-generate every class, which is safe but loses accounting
	// continuity, and an operator should know it happened.
	if p.classStore != nil {
		if err := p.classStore.save(p.snapshot()); err != nil {
			res.Failed++
			res.Problems = append(res.Problems, "durable class state not written: "+err.Error())
			// The activation clock is part of that file. If it cannot be written, a restart would come back
			// with a stale or absent countdown, so from here on an unprovable activation gets no fresh grace.
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

// phase3ShapingHandler serves POST /v1/phase3/shaping. Authentication is SO_PEERCRED on the accepted unix
// connection: the kernel states which uid is calling. A header would be a claim, not an identity.
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

// status reports the truthful current enforcement state. An operator (and the health supervisor) must be able
// to see that the kernel is NOT enforcing what durable state says it should — a plan that failed to apply is
// invisible otherwise, and "shaping looks fine" is the most expensive kind of wrong.
// The five states an operator actually needs to tell apart. Reporting "degraded=false, plan_stale=true" — as
// an earlier version did — is the worst of both: it reads as healthy at a glance while saying, in a field
// nobody aggregates, that nothing is known to be enforced.
const (
	shapingDark          = "DARK"                   // Phase 3 is off here; nothing is enforced and nothing should be
	shapingNoPlan        = "ACTIVE_NO_PLAN"         // live, but no plan has ever been put in force
	shapingConverged     = "ACTIVE_FRESH_CONVERGED" // live, current plan, BOTH halves driven to it cleanly
	shapingStale         = "ACTIVE_STALE"           // live, but the plan in force has expired without a replacement
	shapingDegradedState = "ACTIVE_DEGRADED"        // live, but the kernel is not known to match the plan
)

// ACTIVE_FRESH_CONVERGED means BOTH ENFORCEMENT HALVES are proven, never just one.
//
// It is reached only when a reconciliation completed with nothing failing, and provisionSession does not
// count a session as shaped until its accountable tc class is verified AND its nft authorization is verified.
// A session that is metered but not authorized (or authorized but not metered) leaves res.Failed non-zero, so
// this state is unreachable while either half is missing. That is deliberate: "converged" is the one word an
// operator reads as "enforcement matches what we granted", and it must never be true of half the system.

// enforcementStage is the per-session progress an operator needs to tell apart when a plan has NOT converged.
// Reporting only "degraded" answers "something is wrong" but not "which half", and the two halves have
// completely different causes and remedies.
const (
	stageRequested    = "REQUESTED"      // durable grant exists; nothing enforced yet
	stageTCPrepared   = "TC_PREPARED"    // classes exist, not classifying, guest NOT authorized
	stageOriginPinned = "ORIGIN_PINNED"  // accounting origin registered; still not classifying or authorized
	stageTCActive     = "TC_ACTIVE"      // classifying and verified; guest still NOT authorized
	stageAuthorized   = "NFT_AUTHORIZED" // packet authorization in force and verified
	stageEnforced     = "ENFORCED"       // both halves proven and the Session promoted to active
	stageDeniedFailed = "DENIED_FAILED"  // provisioning failed; access denied and cleanup attempted
)

// shapingState derives the single truthful state. Caller holds p.mu.
func (p *phase3Shaping) shapingState(now time.Time) (string, bool) {
	if !p.mode.Active {
		return shapingDark, false
	}
	switch {
	case p.lastDegrade != "":
		return shapingDegradedState, true
	case !p.hasConverged:
		// Live with nothing proven in force. Whether that is because nothing was ever submitted or because
		// every submission failed, the appliance is not enforcing what durable state says it should.
		return shapingNoPlan, true
	case !p.lastConverged.ExpiresAt.After(now):
		// The producer has gone quiet. What is installed may still be right, but nothing is confirming it,
		// and "probably still right" is not a state to page nobody about.
		return shapingStale, true
	default:
		return shapingConverged, false
	}
}

func (p *phase3Shaping) status() map[string]any {
	return p.statusWithEvidence(evidenceHealth{Available: true, CheckedAt: time.Now()}, false)
}

// statusWithEvidence is status() with the ownership authority's own health folded in.
//
// The plane cannot be called healthy while the authority that decides who owns an address is unreachable: it
// is still installing kernel state, but it can no longer tell whether that state belongs to the guest it says
// it does, and it is withholding every renewal in the meantime. Reporting CONVERGED there was how an appliance
// with no lease manager for two days looked perfectly well.
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
		// How many sessions this pass declined to renew for want of evidence. Zero is the normal state; a
		// number here means guests are running down bounded authorizations that nothing is confirming.
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
	// LAST, so it cannot be overwritten by the ordinary degrade text. An evidence outage is not one problem
	// among several: while it lasts, nothing this plane reports about ownership can be relied on, so it is the
	// problem an operator must see first.
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

// phase3EpochsHandler serves the current managed-class generations.
//
// This is CONTROL-PLANE state, not a public status page. It enumerates which sessions are shaped, on which
// bridge, and how many times each class has been replaced — enough to profile guest activity and to time an
// attack against a reset. The socket is group-readable by scd, edged, portald and pmsd, none of which have
// any business reading it, so it requires the SAME authenticated producer identity as a submission.
//
// While dark it refuses without disclosing anything, including whether any classes exist.
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
