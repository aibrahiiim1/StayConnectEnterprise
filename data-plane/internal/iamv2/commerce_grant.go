package iamv2

import (
	"encoding/json"
	"fmt"
	"strings"
)

// GrantSnapshotVersion is the canonical Phase-2 grant-snapshot schema version.
const GrantSnapshotVersion = 1

// Bounded operational limits for a Phase-2 grant (defensive caps; a hostile/typo config cannot request
// absurd values).
const (
	maxKbps            = 10_000_000 // 10 Gbps
	maxDevices         = 1_000
	maxIdleSeconds     = 30 * 24 * 3600
	maxSessionSeconds  = 365 * 24 * 3600
	maxTimeQuotaSecond = int64(10) * 365 * 24 * 3600
	maxDataQuotaBytes  = int64(1) << 50 // 1 PiB
)

var deviceLimitPolicies = map[string]bool{"REJECT_NEW_DEVICE": true, "DISCONNECT_OLDEST": true, "ADMIN_APPROVAL": true}

// knownGrantKeys are the only keys permitted in a tier grant_value (besides "match", the tier
// condition). Any other key at publication is rejected.
var knownGrantKeys = map[string]bool{
	"match": true, "down_kbps": true, "up_kbps": true, "max_concurrent_devices": true,
	"device_limit_policy": true, "idle_timeout_seconds": true, "max_continuous_session_seconds": true,
	"time_quota_seconds": true, "data_quota_bytes": true, "time_accounting_mode": true,
}

// GrantSnapshot is the typed, validated, canonical grant frozen into a quote. It is the ONLY grant data
// a purchase/entitlement trusts — never raw jsonb.
type GrantSnapshot struct {
	Version                     int    `json:"version"`
	ServicePlanRevisionID       string `json:"service_plan_revision_id"`
	PackageRevisionID           string `json:"package_revision_id"`
	GrantTierOrder              int    `json:"grant_tier_order"`
	DownKbps                    int    `json:"down_kbps"`
	UpKbps                      int    `json:"up_kbps"`
	MaxConcurrentDevices        int    `json:"max_concurrent_devices"`
	DeviceLimitPolicy           string `json:"device_limit_policy"`
	IdleTimeoutSeconds          int    `json:"idle_timeout_seconds"`
	MaxContinuousSessionSeconds int    `json:"max_continuous_session_seconds"`
	TimeQuotaSeconds            int64  `json:"time_quota_seconds"`
	DataQuotaBytes              int64  `json:"data_quota_bytes"`
	TimeAccountingMode          string `json:"time_accounting_mode"`
	// Duration / end policy (resolved once at quote time; never recomputed for historical rows).
	EndMode        string          `json:"end_mode"`
	WindowEndsAt   string          `json:"window_ends_at,omitempty"`  // RFC3339 UTC, "" = none
	DurationPolicy json.RawMessage `json:"duration_policy,omitempty"` // verbatim source policy (audit)
}

// asInt64 accepts only INTEGER numeric JSON (json.Number without a fractional part, or an int/int64).
// A JSON float (e.g. 5.5) or a whole-looking float64(5) that carries no integer guarantee is rejected;
// the repository decodes grant_value with UseNumber so tier numbers arrive as json.Number.
func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		s := n.String()
		if strings.ContainsAny(s, ".eE") {
			return 0, false
		}
		i, err := n.Int64()
		return i, err == nil
	case int:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}

// BuildGrantSnapshot merges the pinned plan revision with the matched tier's typed overrides, validated
// against the Phase-2 bounds. Returns a deterministic error on any malformed/out-of-range/unknown-key
// input; tier overrides may not violate required plan constraints.
func BuildGrantSnapshot(tier GrantTier, plan PlanRevisionRow, pkg PackageRevisionRow) (GrantSnapshot, error) {
	// unknown tier keys are rejected (publication-grade validation applied at runtime too)
	for k := range tier.Value {
		if !knownGrantKeys[k] {
			return GrantSnapshot{}, &Error{Code: ErrInvalidInput, Msg: "unknown grant key: " + k}
		}
	}
	g := GrantSnapshot{
		Version:               GrantSnapshotVersion,
		ServicePlanRevisionID: plan.ID,
		PackageRevisionID:     pkg.ID,
		GrantTierOrder:        tier.Order,
		DownKbps:              plan.DownKbps,
		UpKbps:                plan.UpKbps,
		MaxConcurrentDevices:  plan.MaxConcurrentDevices,
		DeviceLimitPolicy:     "REJECT_NEW_DEVICE",
		TimeQuotaSeconds:      plan.TimeQuotaSeconds,
		DataQuotaBytes:        plan.DataQuotaBytes,
		TimeAccountingMode:    plan.TimeAccountingMode,
	}
	// integer overrides from the tier
	intOverride := func(key string, dst *int, max int) error {
		if v, ok := tier.Value[key]; ok {
			iv, ok := asInt64(v)
			if !ok || iv < 0 || iv > int64(max) {
				return &Error{Code: ErrInvalidInput, Msg: "grant " + key + " must be a non-negative integer within bounds"}
			}
			*dst = int(iv)
		}
		return nil
	}
	int64Override := func(key string, dst *int64, max int64) error {
		if v, ok := tier.Value[key]; ok {
			iv, ok := asInt64(v)
			if !ok || iv < 0 || iv > max {
				return &Error{Code: ErrInvalidInput, Msg: "grant " + key + " must be a non-negative integer within bounds"}
			}
			*dst = iv
		}
		return nil
	}
	for _, e := range []error{
		intOverride("down_kbps", &g.DownKbps, maxKbps),
		intOverride("up_kbps", &g.UpKbps, maxKbps),
		intOverride("max_concurrent_devices", &g.MaxConcurrentDevices, maxDevices),
		intOverride("idle_timeout_seconds", &g.IdleTimeoutSeconds, maxIdleSeconds),
		intOverride("max_continuous_session_seconds", &g.MaxContinuousSessionSeconds, maxSessionSeconds),
		int64Override("time_quota_seconds", &g.TimeQuotaSeconds, maxTimeQuotaSecond),
		int64Override("data_quota_bytes", &g.DataQuotaBytes, maxDataQuotaBytes),
	} {
		if e != nil {
			return GrantSnapshot{}, e
		}
	}
	// enum overrides
	if v, ok := tier.Value["device_limit_policy"]; ok {
		s, ok := v.(string)
		if !ok || !deviceLimitPolicies[strings.ToUpper(strings.TrimSpace(s))] {
			return GrantSnapshot{}, &Error{Code: ErrInvalidInput, Msg: "unknown device_limit_policy"}
		}
		g.DeviceLimitPolicy = strings.ToUpper(strings.TrimSpace(s))
	}
	if v, ok := tier.Value["time_accounting_mode"]; ok {
		s, _ := v.(string)
		g.TimeAccountingMode = strings.ToUpper(strings.TrimSpace(s))
	}
	// AGGREGATE_ONLINE_TIME is capability-disabled in Phase 2 (fail closed).
	if strings.ToUpper(g.TimeAccountingMode) == "AGGREGATE_ONLINE_TIME" {
		return GrantSnapshot{}, &Error{Code: ErrInvalidInput, Msg: "AGGREGATE_ONLINE_TIME accounting is capability-disabled in Phase 2"}
	}
	if g.TimeAccountingMode == "" {
		g.TimeAccountingMode = "VALIDITY_WINDOW"
	}
	if g.TimeAccountingMode != "VALIDITY_WINDOW" {
		return GrantSnapshot{}, &Error{Code: ErrInvalidInput, Msg: "unsupported time_accounting_mode " + g.TimeAccountingMode}
	}
	// required plan constraints a tier may never violate
	if g.MaxConcurrentDevices < 1 {
		return GrantSnapshot{}, &Error{Code: ErrInvalidInput, Msg: "max_concurrent_devices must be >= 1"}
	}
	return g, nil
}

// Canonical returns the deterministic JSON encoding of the snapshot (stable field order via struct
// tags) for storage in offer_quotes.grant_snapshot.
func (g GrantSnapshot) Canonical() []byte {
	b, _ := json.Marshal(g)
	return b
}

// ParseGrantSnapshot decodes a stored canonical snapshot.
func ParseGrantSnapshot(b []byte) (GrantSnapshot, error) {
	var g GrantSnapshot
	if err := json.Unmarshal(b, &g); err != nil {
		return GrantSnapshot{}, &Error{Code: ErrInvalidInput, Msg: "malformed grant snapshot"}
	}
	if g.Version != GrantSnapshotVersion {
		return GrantSnapshot{}, &Error{Code: ErrInvalidInput, Msg: fmt.Sprintf("unsupported grant snapshot version %d", g.Version)}
	}
	return g, nil
}
