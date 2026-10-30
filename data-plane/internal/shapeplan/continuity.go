package shapeplan

// The continuity block: the bounded statement acctd makes, with every healthy plan, about how long netd may keep the
// SAME sessions authorized if plans stop arriving (docs/architecture/ONEGATE_OUTAGE_CONTINUITY.md).
//
// It is deliberately NOT part of DesiredStateHash. Its byte budgets change every tick; hashing them would make every plan
// "changed" and defeat the fast path (phase3_fastpath.go). It has its own hash, so a truncated or altered block is
// refused as a block, and the plan itself is still judged on its own hash.
//
// Nothing here grants anything. It only states upper bounds; netd derives a deadline from them on its own monotonic
// clock and keeps the shorter of every bound.

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MaxContinuityWindowSeconds is the hard ceiling netd enforces regardless of what a producer asks for. The settings
// bound (120 minutes) is the same number; a producer asking for more is refused, not clamped.
const MaxContinuityWindowSeconds = 120 * 60

// MaxBudgetAge is how old the byte budget may be when netd accepts it. The budget is a database read; older than this
// and the margin acctd subtracted no longer covers the bytes that flowed since.
const MaxBudgetAge = 2 * time.Minute

// ContinuitySession bounds one session.
type ContinuitySession struct {
	SessionID string `json:"session_id"`
	// HardEnd is the earliest wall-clock instant after which the session must not be forwarded whatever the window: the
	// entitlement window end, the plan's maximum session duration, the end of an online-time allowance.
	// Nil means no such instant is known (the window alone bounds it).
	HardEnd *time.Time `json:"hard_end,omitempty"`
}

// ContinuityBudget is one entitlement's remaining data allowance. Absent entitlement = no byte quota.
type ContinuityBudget struct {
	EntitlementID string `json:"entitlement_id"`
	// RemainingBytes is quota minus usage the database had counted at AsOf, minus a safety margin for the bytes that
	// could have flowed but not yet been counted. Never negative; zero means no continuity for this entitlement.
	RemainingBytes int64 `json:"remaining_bytes"`
}

// Continuity is the block.
type Continuity struct {
	// WindowSeconds is the configured outage window. Zero never appears: strict mode sends no block.
	WindowSeconds int `json:"window_seconds"`
	// AsOf is taken BEFORE the database read it describes, so any change committed after AsOf is newer than the block.
	AsOf         time.Time           `json:"as_of"`
	Sessions     []ContinuitySession `json:"sessions"`
	Budgets      []ContinuityBudget  `json:"budgets,omitempty"`
	ContentsHash string              `json:"contents_hash"`
}

// HashContinuity hashes everything but the hash itself, order-independent.
func HashContinuity(c Continuity) string {
	rows := make([]string, 0, len(c.Sessions)+len(c.Budgets)+1)
	rows = append(rows, strings.Join([]string{"W", strconv.Itoa(c.WindowSeconds), c.AsOf.UTC().Format(time.RFC3339Nano)}, "\x1f"))
	for _, s := range c.Sessions {
		end := ""
		if s.HardEnd != nil {
			end = s.HardEnd.UTC().Format(time.RFC3339Nano)
		}
		rows = append(rows, strings.Join([]string{"S", s.SessionID, end}, "\x1f"))
	}
	for _, b := range c.Budgets {
		rows = append(rows, strings.Join([]string{"B", b.EntitlementID, strconv.FormatInt(b.RemainingBytes, 10)}, "\x1f"))
	}
	sort.Strings(rows)
	sum := sha256.Sum256([]byte(strings.Join(rows, "\x1e")))
	return hex.EncodeToString(sum[:])
}

// Continuity refusal reasons. A refused block never refuses the plan: the plan is applied, the guests simply get no
// continuity (the previous, strict behaviour).
const (
	ReasonContinuityHash   = "continuity_hash_mismatch"
	ReasonContinuityWindow = "continuity_window_out_of_range"
	ReasonContinuityStale  = "continuity_budget_stale"
	ReasonContinuityFuture = "continuity_as_of_in_the_future"
)

// OffContinuity is the explicit statement "continuity is switched off" (setting 0): a block with no window and no content. It
// differs from NO block, which only means the producer is not vouching for anything new right now; netd keeps what it holds
// (never extended) in that case and drops it on an explicit off.
func OffContinuity() *Continuity {
	c := &Continuity{AsOf: time.Now().UTC()}
	c.ContentsHash = HashContinuity(*c)
	return c
}

// IsOff reports an authentic explicit-off block.
func (c Continuity) IsOff() bool {
	return c.WindowSeconds == 0 && len(c.Sessions) == 0 && len(c.Budgets) == 0 && c.ContentsHash == HashContinuity(c)
}

// ValidateContinuity checks the block against the clock netd sees. now is netd's wall clock.
func ValidateContinuity(c Continuity, now time.Time) (string, bool) {
	switch {
	case c.WindowSeconds <= 0 || c.WindowSeconds > MaxContinuityWindowSeconds:
		return ReasonContinuityWindow, false
	case c.ContentsHash != HashContinuity(c):
		return ReasonContinuityHash, false
	case c.AsOf.After(now.Add(30 * time.Second)):
		return ReasonContinuityFuture, false
	case now.Sub(c.AsOf) > MaxBudgetAge:
		return ReasonContinuityStale, false
	}
	return "", true
}
