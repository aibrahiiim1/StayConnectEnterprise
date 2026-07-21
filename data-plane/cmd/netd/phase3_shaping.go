package main

// PHASE-3 SHAPING RECONCILIATION — the ONLY writer of Phase-3 tc state (see ADR-0002).
//
// acctd measures and derives; netd applies. Nothing else writes these classes, so there is no schedule on
// which two daemons can interleave a delete with an add for the same session.
//
// The contract is a WHOLE PLAN, never a delta. A caller cannot say "remove this one" or "add that one",
// because incremental instructions are exactly how two sides end up disagreeing about what is installed.
// Every submission is a complete statement of what should be in force, and applying it is idempotent: the
// same plan applied twice leaves the kernel in the same state, which is what makes restart and reboot
// recovery ordinary rather than special.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// shapingSession is one session's desired treatment. It is deliberately flat and addressing-only: netd is not
// told WHY a session is entitled, because it has no business making that judgement.
type shapingSession struct {
	SessionID string `json:"session_id"`
	IP        string `json:"ip"`
	Bridge    string `json:"bridge"`
	DownKbps  int    `json:"down_kbps"`
	UpKbps    int    `json:"up_kbps"`
}

// shapingPlanRequest is a COMPLETE statement of Phase-3 shaping for a site.
type shapingPlanRequest struct {
	// Tear lists sessions that must not be forwarded. They are torn down BEFORE anything is shaped.
	Tear []shapingSession `json:"tear"`
	// Shape lists sessions that must be shaped, at the rates their entitlement's pinned plan specifies.
	Shape []shapingSession `json:"shape"`
}

type shapingPlanResponse struct {
	TornDown int      `json:"torn_down"`
	Shaped   int      `json:"shaped"`
	Failed   int      `json:"failed"`
	Degraded bool     `json:"degraded"`
	Problems []string `json:"problems,omitempty"`
}

// shaper is the tc surface netd uses. It is an interface so the ordering and idempotency properties can be
// proven without a kernel.
type shaper interface {
	EnsureBridgeInfra(ctx context.Context, bridge string) error
	AddSession(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int) error
	DeleteSession(ctx context.Context, bridge string, ip net.IP) error
}

// phase3Shaping holds the single writer's state. It deliberately remembers NOTHING about policy: the only
// field is a mutex, because two concurrent submissions must not interleave, and lastApplied exists purely so
// health can report whether the appliance is currently degraded.
type phase3Shaping struct {
	mu          sync.Mutex
	shp         shaper
	lastApplied time.Time
	lastDegrade string
}

// applyPhase3Shaping reconciles the kernel to the submitted plan. Teardown first, then shaping: the other
// order leaves a window in which access that has ended is still forwarded while capacity is handed out.
func (p *phase3Shaping) apply(ctx context.Context, plan shapingPlanRequest) shapingPlanResponse {
	p.mu.Lock()
	defer p.mu.Unlock()

	var res shapingPlanResponse
	for _, s := range plan.Tear {
		ip := net.ParseIP(s.IP)
		if ip == nil || s.Bridge == "" {
			res.Failed++
			res.Problems = append(res.Problems, "tear: unusable addressing for session "+s.SessionID)
			continue
		}
		if err := p.shp.DeleteSession(ctx, s.Bridge, ip); err != nil {
			// A teardown failure is the serious one: it means traffic may still be forwarded for access that
			// has ended, so it is reported as degraded rather than swallowed.
			res.Failed++
			res.Problems = append(res.Problems, "tear "+s.SessionID+": "+err.Error())
			continue
		}
		res.TornDown++
	}
	for _, s := range plan.Shape {
		ip := net.ParseIP(s.IP)
		if ip == nil || s.Bridge == "" || s.DownKbps <= 0 || s.UpKbps <= 0 {
			// A session with no addressing or no rates is left UNSHAPED. Installing a zero rate would be a
			// silent full-speed pass — the opposite of what an unratable session should get.
			res.Failed++
			res.Problems = append(res.Problems, "shape: unusable plan entry for session "+s.SessionID)
			continue
		}
		if err := p.shp.EnsureBridgeInfra(ctx, s.Bridge); err != nil {
			res.Failed++
			res.Problems = append(res.Problems, "bridge "+s.Bridge+": "+err.Error())
			continue
		}
		if err := p.shp.AddSession(ctx, s.Bridge, ip, s.DownKbps, s.UpKbps); err != nil {
			res.Failed++
			res.Problems = append(res.Problems, "shape "+s.SessionID+": "+err.Error())
			continue
		}
		res.Shaped++
	}
	res.Degraded = res.Failed > 0
	p.lastApplied = time.Now()
	p.lastDegrade = ""
	if res.Degraded {
		p.lastDegrade = res.Problems[0]
	}
	return res
}

// phase3ShapingHandler serves POST /v1/phase3/shaping on netd's protected local socket. The socket's existing
// peer restriction is the authentication: only local, privileged callers can reach it.
func (s *server) phase3ShapingHandler(w http.ResponseWriter, r *http.Request) {
	var plan shapingPlanRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request", "message": "malformed shaping plan"})
		return
	}
	res := s.phase3.apply(r.Context(), plan)
	if res.Degraded {
		slog.Warn("phase3 shaping applied with problems", "failed", res.Failed, "first", res.Problems[0])
	}
	writeJSON(w, http.StatusOK, res)
}
