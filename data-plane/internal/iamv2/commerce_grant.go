package iamv2

import (
	"encoding/json"
	"fmt"
	"strings"
)

const GrantSnapshotVersion = 1

// Defensive caps so a hostile or typo config cannot request absurd values.
const (
	maxKbps            = 10_000_000 // 10 Gbps
	maxDevices         = 1_000
	maxIdleSeconds     = 30 * 24 * 3600
	maxSessionSeconds  = 365 * 24 * 3600
	maxTimeQuotaSecond = int64(10) * 365 * 24 * 3600
	maxDataQuotaBytes  = int64(1) << 50 // 1 PiB
)

var deviceLimitPolicies = map[string]bool{"REJECT_NEW_DEVICE": true, "DISCONNECT_OLDEST": true, "ADMIN_APPROVAL": true}

// knownGrantKeys are the only keys permitted in a tier grant_value; any other is rejected.
var knownGrantKeys = map[string]bool{
	"match": true, "down_kbps": true, "up_kbps": true, "max_concurrent_devices": true,
	"device_limit_policy": true, "idle_timeout_seconds": true, "max_continuous_session_seconds": true,
	"time_quota_seconds": true, "data_quota_bytes": true, "time_accounting_mode": true,
}

// GrantSnapshot is the only grant data a purchase or entitlement trusts, never raw jsonb.
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
	// End policy is resolved once at quote time and never recomputed.
	EndMode        string          `json:"end_mode"`
	WindowEndsAt   string          `json:"window_ends_at,omitempty"`  // RFC3339 UTC, "" = none
	DurationPolicy json.RawMessage `json:"duration_policy,omitempty"` // verbatim source policy (audit)
	// AcquisitionMethod is pinned at quote time and re-validated at confirm; "" means Free.
	AcquisitionMethod string `json:"acquisition_method,omitempty"`
	// FrozenDataQuotaBytes is a PER_STAY_NIGHT allowance resolved once from the pinned stay; 0 means the
	// entitlement reads its plan revision.
	FrozenDataQuotaBytes int64 `json:"frozen_data_quota_bytes,omitempty"`
}

// asInt64 accepts only integer JSON; floats (even whole-looking float64) are rejected. The repository decodes
// grant_value with UseNumber so tier numbers arrive as json.Number.
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

// BuildGrantSnapshot merges the pinned plan revision with the matched tier's overrides, failing on any
// malformed, out-of-range or unknown-key input.
func BuildGrantSnapshot(tier GrantTier, plan PlanRevisionRow, pkg PackageRevisionRow) (GrantSnapshot, error) {
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
	if v, ok := tier.Value["device_limit_policy"]; ok {
		s, ok := v.(string)
		if !ok || !deviceLimitPolicies[strings.ToUpper(strings.TrimSpace(s))] {
			return GrantSnapshot{}, &Error{Code: ErrInvalidInput, Msg: "unknown device_limit_policy"}
		}
		g.DeviceLimitPolicy = strings.ToUpper(strings.TrimSpace(s))
	}
	// A tier may not choose the accounting mode: it belongs to the immutable plan revision, and an override
	// would grant one revision under two accounting rules.
	if _, ok := tier.Value["time_accounting_mode"]; ok {
		return GrantSnapshot{}, &Error{Code: ErrInvalidInput,
			Msg: "time_accounting_mode belongs to the plan revision and cannot be overridden by a tier"}
	}
	if g.TimeAccountingMode == "" {
		// An omitted mode is VALIDITY_WINDOW.
		g.TimeAccountingMode = "VALIDITY_WINDOW"
	}
	switch strings.ToUpper(g.TimeAccountingMode) {
	case "VALIDITY_WINDOW", "AGGREGATE_ONLINE_TIME":
	default:
		return GrantSnapshot{}, &Error{Code: ErrInvalidInput, Msg: "unsupported time_accounting_mode " + g.TimeAccountingMode}
	}
	if g.MaxConcurrentDevices < 1 {
		return GrantSnapshot{}, &Error{Code: ErrInvalidInput, Msg: "max_concurrent_devices must be >= 1"}
	}
	return g, nil
}

// Canonical is the deterministic JSON stored in offer_quotes.grant_snapshot.
func (g GrantSnapshot) Canonical() []byte {
	b, _ := json.Marshal(g)
	return b
}

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
