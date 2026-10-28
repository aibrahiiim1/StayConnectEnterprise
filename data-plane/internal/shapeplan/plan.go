// Package shapeplan is the wire contract between acctd (derives shaping) and netd (the only applier). It
// is one shared definition so both ends hash a desired state identically. The envelope carries scope
// (tenant/site/appliance/assignment), currency (generation + expiry, so a replay cannot reinstate removed
// access) and completeness (hash, so a truncated body is not applied as a mass revocation).
package shapeplan

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ContractVersion mismatches are refused, not interpreted. /2 added AccessEndsAt; a /1 producer states no
// boundary, and reading that as "none exists" would grant a full-length lease to a guest whose access ends soon.
const ContractVersion = "phase3-shaping/2"

// Session is one session's desired treatment. Entitled=false means "must not be forwarded".
type Session struct {
	SessionID string `json:"session_id"`
	// DeviceID is needed because the applier registers the accounting origin, keyed by the full source
	// tuple (session, device, bridge, class); inferring it later loses the first tick's traffic.
	DeviceID string `json:"device_id"`
	IP       string `json:"ip"`
	Bridge   string `json:"bridge"`
	DownKbps int    `json:"down_kbps"`
	UpKbps   int    `json:"up_kbps"`
	Entitled bool   `json:"entitled"`
	// AccessEndsAt is the earliest hard boundary after which the session must not be forwarded (the
	// entitlement's window end). It travels in the plan because the applier holds the kernel lease, which
	// must never outlive the access, and netd has no view of entitlements. Nil means no wall-clock end is
	// known, NOT unbounded: the lease is still bounded by the producer's cadence.
	AccessEndsAt *time.Time `json:"access_ends_at,omitempty"`
	// EndReason is a bounded machine code for why an un-entitled session is torn down; meaningful only when
	// Entitled is false, empty means its access ended. SUPERSEDED_ON_ADDRESS (a newer session took the
	// address) must not be recorded as a teardown failure.
	EndReason string `json:"end_reason,omitempty"`
	// MAC lets the applier ask DHCP whether the address still belongs to this device; without it a
	// reassigned address stayed authorized under another guest's entitlement.
	MAC string `json:"mac,omitempty"`
	// EntitlementID groups the sessions sharing one account, so the applier can build the SHARED ceiling.
	EntitlementID string `json:"entitlement_id,omitempty"`
	// SpeedAllocation is PER_DEVICE or SHARED; empty means PER_DEVICE.
	SpeedAllocation string `json:"speed_allocation,omitempty"`
}

// Envelope is a complete, scoped, versioned statement of the managed state.
type Envelope struct {
	ContractVersion string `json:"contract_version"`

	TenantID     string `json:"tenant_id"`
	SiteID       string `json:"site_id"`
	ApplianceID  string `json:"appliance_id"`
	AssignmentID string `json:"assignment_id"`
	// AssignmentGen older than the appliance's held assignment describes a changed tenancy and is refused.
	AssignmentGen int64 `json:"assignment_generation"`
	// ProducerRuntimeGen is informational: it distinguishes one producer process from the next; nothing gates on it.
	ProducerRuntimeGen int64 `json:"producer_runtime_generation"`
	// PlanGeneration is monotonic and durable across producer restarts.
	PlanGeneration int64     `json:"plan_generation"`
	GeneratedAt    time.Time `json:"generated_at"`
	// ExpiresAt bounds how long the plan is current; an expired plan with no replacement means the producer
	// went quiet and the installed state is no longer known to be correct.
	ExpiresAt time.Time `json:"expires_at"`

	// ManagedBridges are all bridges this plan speaks for, not just those with sessions. Otherwise a bridge
	// whose last session ended would never appear again and a class left on it could never be found.
	ManagedBridges []string `json:"managed_bridges"`

	Sessions         []Session `json:"sessions"`
	DesiredStateHash string    `json:"desired_state_hash"`
}

// Scope is the applier's own identity from its enrollment and signed assignment, never from the envelope.
type Scope struct {
	TenantID    string
	SiteID      string
	ApplianceID string
	AssignGen   int64
}

// Accepted is the durable record of the last plan in force, so a restart cannot accept an older plan.
type Accepted struct {
	Generation int64     `json:"generation"`
	Hash       string    `json:"hash"`
	TenantID   string    `json:"tenant_id"`
	SiteID     string    `json:"site_id"`
	AcceptedAt time.Time `json:"accepted_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// HashDesiredState hashes the managed bridges and sessions, order-independent and separated by control
// characters that cannot occur in fields. The bridge list is inside the hash because it decides where
// unclaimed classes are removed; a trimmed list would silently stop reconciling those bridges.
func HashDesiredState(bridges []string, sessions []Session) string {
	rows := make([]string, 0, len(sessions)+len(bridges))
	for _, b := range bridges {
		rows = append(rows, "B\x1f"+b)
	}
	for _, s := range sessions {
		// The access boundary and end reason are hashed too: altered in transit they would still look like a
		// valid plan while granting a longer lease or a different audit trail.
		ends := ""
		if s.AccessEndsAt != nil {
			ends = s.AccessEndsAt.UTC().Format(time.RFC3339Nano)
		}
		rows = append(rows, strings.Join([]string{
			"S", s.SessionID, s.DeviceID, s.IP, s.Bridge,
			strconv.Itoa(s.DownKbps), strconv.Itoa(s.UpKbps), strconv.FormatBool(s.Entitled), ends,
			s.EndReason, s.MAC, s.EntitlementID, s.SpeedAllocation,
		}, "\x1f"))
	}
	sort.Strings(rows)
	sum := sha256.Sum256([]byte(strings.Join(rows, "\x1e")))
	return hex.EncodeToString(sum[:])
}

// Refusal reasons are a closed set so the applier can report why without logging the plan.
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
	// ReasonBadBridgeName: a managed bridge name is not a plain Linux interface name. The names reach tc and ip
	// argument lists, where one starting with '-' would be read as an option (audit Phase 6 L12).
	ReasonBadBridgeName = "bad_bridge_name"
)

// validBridgeName is the closed interface-name set (letters, digits, '.', '_', '-'; not starting with '-' or '.';
// at most IFNAMSIZ-1 bytes).
func validBridgeName(s string) bool {
	if len(s) == 0 || len(s) > 15 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case (c == '.' || c == '_' || c == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

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
		// Accepting it would silently turn full reconciliation into add-only.
		return ReasonNoManagedBridges, false
	case env.DesiredStateHash != HashDesiredState(env.ManagedBridges, env.Sessions):
		return ReasonHashMismatch, false
	}
	for _, b := range env.ManagedBridges {
		// An empty entry is skipped by the applier; anything else must be a plain interface name. Session bridges
		// must be declared managed, which the applier checks per session.
		if b != "" && !validBridgeName(b) {
			return ReasonBadBridgeName, false
		}
	}
	if hasLast {
		if env.PlanGeneration < last.Generation {
			return ReasonStaleGeneration, false
		}
		if env.PlanGeneration == last.Generation && env.DesiredStateHash != last.Hash {
			// Two payloads under one generation means the producer's numbering is broken.
			return ReasonGenerationConflict, false
		}
	}
	return "", true
}
