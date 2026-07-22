// Package shapeplan is the WIRE CONTRACT between the process that derives Phase-3 shaping (acctd) and the
// only process allowed to apply it (netd, ADR-0002).
//
// It lives in one place on purpose. The two ends must agree byte-for-byte on what a desired state hashes to,
// and two "obviously identical" implementations of a canonical encoding are how that stops being true — the
// drift is invisible until a plan is silently refused in production. One definition, used by both binaries,
// makes disagreement impossible rather than detectable.
//
// The envelope answers three questions a bare list of sessions cannot:
//
//	WHOSE state is this?      tenant/site/appliance/assignment — so a plan derived for one site can never be
//	                          applied on another, even by mistake.
//	IS IT STILL CURRENT?      plan generation + expiry — so a delayed or replayed plan cannot reinstate access
//	                          that a newer plan removed.
//	IS IT COMPLETE?           desired-state hash — so a truncated body is refused instead of being applied as
//	                          a smaller desired state, which would look exactly like a mass revocation.
package shapeplan

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ContractVersion is the wire contract. A producer speaking a different version is refused rather than
// interpreted: a half-understood desired state is worse than none.
const ContractVersion = "phase3-shaping/1"

// Session is one session's desired treatment. Entitled=false means "must not be forwarded".
type Session struct {
	SessionID string `json:"session_id"`
	// DeviceID travels with the session because the APPLIER registers the accounting origin for a class it
	// creates, and a checkpoint is keyed by its whole source tuple — session, device, bridge, class. Without
	// it the applier could not name the series it just created, and the origin would have to be inferred
	// later by the producer, which is the gap that loses the first tick's traffic.
	DeviceID string `json:"device_id"`
	IP       string `json:"ip"`
	Bridge   string `json:"bridge"`
	DownKbps int    `json:"down_kbps"`
	UpKbps   int    `json:"up_kbps"`
	Entitled bool   `json:"entitled"`
}

// Envelope is a COMPLETE, scoped, versioned statement of the Phase-3 managed state.
type Envelope struct {
	ContractVersion string `json:"contract_version"`

	TenantID     string `json:"tenant_id"`
	SiteID       string `json:"site_id"`
	ApplianceID  string `json:"appliance_id"`
	AssignmentID string `json:"assignment_id"`
	// AssignmentGen is the signed assignment's version. A plan derived under an older assignment than the one
	// the appliance now holds describes a tenancy that has since changed, and is refused.
	AssignmentGen int64 `json:"assignment_generation"`
	// ProducerRuntimeGen distinguishes one producer process from the next. Nothing gates on it; it is there so
	// an operator reading two plans can tell "the same process re-derived" from "a new process took over".
	ProducerRuntimeGen int64 `json:"producer_runtime_generation"`
	// PlanGeneration is monotonic and DURABLE across producer restarts.
	PlanGeneration int64     `json:"plan_generation"`
	GeneratedAt    time.Time `json:"generated_at"`
	// ExpiresAt bounds how long this plan may be considered current. Its real job is on the applier's health:
	// a plan that expired without a replacement means the producer went quiet and what is installed is no
	// longer known to be correct.
	ExpiresAt time.Time `json:"expires_at"`

	// ManagedBridges is the set of guest bridges this plan speaks for — NOT merely the ones a session
	// happens to be on. Without it, a bridge whose last session has already ended and been forgotten would
	// never appear in any plan again, so a class left behind on it could never be found: the applier would
	// have nothing telling it to look there. Declaring the bridges separately from the sessions is what makes
	// "remove everything not claimed" a statement the applier can actually act on.
	ManagedBridges []string `json:"managed_bridges"`

	Sessions         []Session `json:"sessions"`
	DesiredStateHash string    `json:"desired_state_hash"`
}

// Scope is the applier's OWN authoritative identity, resolved from its enrollment and signed assignment — never
// from the envelope. An envelope can only be checked against it.
type Scope struct {
	TenantID    string
	SiteID      string
	ApplianceID string
	AssignGen   int64
}

// Accepted is the minimum record of the last plan put in force, kept durably so a restart cannot be talked
// into accepting a plan older than one already applied.
type Accepted struct {
	Generation int64     `json:"generation"`
	Hash       string    `json:"hash"`
	TenantID   string    `json:"tenant_id"`
	SiteID     string    `json:"site_id"`
	AcceptedAt time.Time `json:"accepted_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// HashDesiredState is the canonical hash of a desired state: the managed bridges AND the sessions.
// Order-independent (the producer's SQL ordering is not part of the meaning) and field-separated with control
// characters that cannot occur in any of the fields, so "a|b" and "a" + "|b" cannot collide.
//
// The bridge list is inside the hash deliberately. It decides WHERE the applier will remove unclaimed
// classes, so a body that lost it — or had it trimmed in transit — would silently stop reconciling those
// bridges while still looking like a complete, valid plan.
func HashDesiredState(bridges []string, sessions []Session) string {
	rows := make([]string, 0, len(sessions)+len(bridges))
	for _, b := range bridges {
		rows = append(rows, "B\x1f"+b)
	}
	for _, s := range sessions {
		rows = append(rows, strings.Join([]string{
			"S", s.SessionID, s.DeviceID, s.IP, s.Bridge,
			strconv.Itoa(s.DownKbps), strconv.Itoa(s.UpKbps), strconv.FormatBool(s.Entitled),
		}, "\x1f"))
	}
	sort.Strings(rows)
	sum := sha256.Sum256([]byte(strings.Join(rows, "\x1e")))
	return hex.EncodeToString(sum[:])
}

// Refusal reasons. They are a closed set so the applier can report exactly why a plan was not put in force
// without logging the plan itself.
const (
	ReasonUnsupportedContract = "unsupported_contract"
	ReasonWrongTenant         = "wrong_tenant"
	ReasonWrongSite           = "wrong_site"
	ReasonWrongAppliance      = "wrong_appliance"
	ReasonStaleAssignment     = "stale_assignment"
	ReasonInvalidGeneration   = "invalid_generation"
	ReasonExpiredPlan         = "expired_plan"
	ReasonHashMismatch        = "hash_mismatch"
	ReasonNoManagedBridges    = "no_managed_bridges"
	ReasonStaleGeneration     = "stale_generation"
	ReasonGenerationConflict  = "duplicate_generation_conflict"
)

// Validate checks an envelope against the applier's own scope and the last plan it accepted. It returns a
// bounded reason and false when the plan must not be applied.
func Validate(env Envelope, scope Scope, last Accepted, hasLast bool, now time.Time) (string, bool) {
	switch {
	case env.ContractVersion != ContractVersion:
		return ReasonUnsupportedContract, false
	case env.TenantID == "" || env.TenantID != scope.TenantID:
		return ReasonWrongTenant, false
	case env.SiteID == "" || env.SiteID != scope.SiteID:
		return ReasonWrongSite, false
	case env.ApplianceID == "" || env.ApplianceID != scope.ApplianceID:
		return ReasonWrongAppliance, false
	case env.AssignmentGen < scope.AssignGen:
		return ReasonStaleAssignment, false
	case env.PlanGeneration <= 0:
		return ReasonInvalidGeneration, false
	case env.ExpiresAt.IsZero() || !env.ExpiresAt.After(now):
		return ReasonExpiredPlan, false
	case len(env.ManagedBridges) == 0:
		// A plan that names no bridges cannot express "remove what is not claimed" anywhere, so accepting it
		// would silently turn full reconciliation into add-only.
		return ReasonNoManagedBridges, false
	case env.DesiredStateHash != HashDesiredState(env.ManagedBridges, env.Sessions):
		return ReasonHashMismatch, false
	}
	if hasLast {
		if env.PlanGeneration < last.Generation {
			return ReasonStaleGeneration, false
		}
		if env.PlanGeneration == last.Generation && env.DesiredStateHash != last.Hash {
			// The same generation must mean the same desired state. Two different payloads under one
			// generation means the producer's numbering is broken, and applying either would be a guess.
			return ReasonGenerationConflict, false
		}
	}
	return "", true
}
