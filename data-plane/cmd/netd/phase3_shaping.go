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
	AddSession(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int) error
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

	lastApplied   time.Time
	lastDegrade   string
	lastAccepted  shapeplan.Accepted
	hasAccepted   bool
	lastRejection string
	rejections    int64
	// epochs is the TC owner's generation per managed class, keyed by bridge/session identity. netd is the
	// only process that creates or replaces these classes, so it is the only honest source of "this counter
	// series restarted". Without it, a counter that went backwards is ambiguous — a reset, a misread, or a
	// minor reused by a different guest — and accounting would have to guess.
	epochs map[string]int64
	// minorOwner remembers which session a managed minor was installed for, so a stray removal can end that
	// session's counter series precisely instead of guessing.
	minorOwner map[string]string
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

	res := p.reconcileLocked(ctx, env)
	res.Accepted = true
	res.PlanGeneration = env.PlanGeneration

	accepted := shapeplan.Accepted{
		Generation: env.PlanGeneration, Hash: env.DesiredStateHash,
		TenantID: env.TenantID, SiteID: env.SiteID,
		AcceptedAt: now.UTC(), ExpiresAt: env.ExpiresAt.UTC(),
	}
	p.lastAccepted, p.hasAccepted = accepted, true
	if p.store != nil {
		p.store.save(accepted)
	}
	return res, nil
}

func (p *phase3Shaping) noteRejection(reason string) {
	p.rejections++
	p.lastRejection = reason
}

// reconcileLocked drives the kernel to the envelope's desired state. The caller holds p.mu.
func (p *phase3Shaping) reconcileLocked(ctx context.Context, env shapeplan.Envelope) shapingPlanResponse {
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
		p.endSeries(s.Bridge, s.SessionID) // the series ends here; a future class for this session is a new one
		if err := p.shp.DeleteSession(ctx, s.Bridge, ip); err != nil {
			// A teardown failure is the serious one: it means traffic may still be forwarded for access that
			// has ended, so it is reported as degraded rather than swallowed.
			res.Failed++
			res.Problems = append(res.Problems, "tear "+s.SessionID+": "+err.Error())
			continue
		}
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

	// 3. SHAPE what must be forwarded.
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
			s := byMinor[minor]
			ip := net.ParseIP(s.IP)
			if err := p.shp.AddSession(ctx, bridge, ip, s.DownKbps, s.UpKbps); err != nil {
				res.Failed++
				res.Problems = append(res.Problems, "shape "+s.SessionID+": "+err.Error())
				continue
			}
			if p.epochs == nil {
				p.epochs = map[string]int64{}
			}
			created := false
			epoch, known := p.epochs[classKey(bridge, s.SessionID)]
			if !known {
				// A NEW class needs a generation from the durable allocator BEFORE it can carry traffic.
				// There is no local fallback: a class installed without an accountable generation is a class
				// whose bytes cannot be attributed to anyone, and manufacturing a value here is how a
				// recreated class ends up masquerading as the series a checkpoint still remembers.
				alloc, err := p.allocEpoch(ctx)
				if err != nil {
					res.Failed++
					res.Problems = append(res.Problems,
						"shape "+s.SessionID+": no class generation could be allocated; not made accountable")
					continue
				}
				epoch, created = alloc, true
				p.epochs[classKey(bridge, s.SessionID)] = epoch
			}
			// If this class slot was held by a DIFFERENT session, that session's series ends here. Leaving it
			// in the inventory would let the previous occupant's generation be carried forward on the next
			// restart, handing the new guest a checkpoint that describes someone else's counters.
			if prev, held := p.minorOwner[minorKey(bridge, minor)]; held && prev != s.SessionID {
				p.endSeries(bridge, prev)
			}
			p.minorOwner[minorKey(bridge, minor)] = s.SessionID
			if p.classes == nil {
				p.classes = map[string]managedClass{}
			}
			p.classes[classKey(bridge, s.SessionID)] = managedClass{
				SessionID: s.SessionID, DeviceID: s.DeviceID, Bridge: bridge, Minor: minor,
				Epoch: epoch, BootID: p.bootID}
			if created {
				// BEFORE the guest can use it: record what the counters actually read, so the first periodic
				// observation measures a difference rather than starting from nothing.
				if problem := p.registerOrigin(ctx, s, minor, epoch); problem != "" {
					res.Failed++
					res.Problems = append(res.Problems, problem)
				}
			}
			res.Shaped++
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
		}
	}

	res.Degraded = res.Failed > 0
	p.lastApplied = time.Now()
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
func (p *phase3Shaping) status() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]any{
		"active":                 p.mode.Active,
		"contract_version":       shapeplan.ContractVersion,
		"degraded":               p.lastDegrade != "",
		"producer_authenticated": p.authz.configured,
		"refused_total":          p.rejections,
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
		out["accepted_generation"] = p.lastAccepted.Generation
		out["accepted_at"] = p.lastAccepted.AcceptedAt.UTC().Format(time.RFC3339)
		out["plan_expires_at"] = p.lastAccepted.ExpiresAt.UTC().Format(time.RFC3339)
		// A plan that has expired without a replacement means the producer has gone quiet, and what is
		// installed is no longer known to be current. That is a health fact, not an internal detail.
		out["plan_stale"] = !p.lastAccepted.ExpiresAt.After(time.Now())
	} else if p.mode.Active {
		out["plan_stale"] = true
	}
	out["managed_classes"] = len(p.minorOwner)
	return out
}

// phase3EpochsHandler serves the current managed-class generations. Accounting reads them so a counter that
// went backwards can be judged: a new generation is a trustworthy reset, the same generation is a regression.
func (s *server) phase3EpochsHandler(w http.ResponseWriter, r *http.Request) {
	if !s.phase3.mode.Active {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "phase3_dark"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"epochs": s.phase3.Epochs()})
}
