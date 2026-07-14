// Package transport abstracts how the control plane talks to an individual
// appliance's session controller (scd). Phase 3 ships a local Unix-socket
// implementation; Phase 5 will add a NATS-backed transport for multi-site
// deployments. Handlers only depend on this interface.
package transport

import (
	"context"
	"time"
)

// ApplianceTransport is the contract the control plane uses to drive actions
// on a specific appliance. Keep surfaces minimal — add methods only when a
// handler needs them.
type ApplianceTransport interface {
	// Revoke asks the appliance to tear down whichever session is currently
	// attached to ip. Idempotent: it's not an error if no such session exists.
	Revoke(ctx context.Context, applianceID, ip, reason string) error

	// PMSTest runs a one-shot connectivity probe against the named provider.
	PMSTest(ctx context.Context, applianceID, name string) (PMSTestResult, error)

	// PMSCache returns up to limit rows from the provider's in-memory cache.
	PMSCache(ctx context.Context, applianceID, name string, limit int) (PMSCacheResult, error)

	// PMSHealth returns a live snapshot of the provider's link state.
	PMSHealth(ctx context.Context, applianceID, name string) (PMSHealthResult, error)
}

// PMSTestResult mirrors scd's /v1/admin/pms/{name}/test response.
type PMSTestResult struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// PMSCacheRow mirrors one entry of a provider's in-memory cache.
type PMSCacheRow struct {
	RoomNumber        string    `json:"room_number"`
	FirstName         string    `json:"first_name"`
	LastName          string    `json:"last_name"`
	GuestDisplayName  string    `json:"guest_display_name,omitempty"`
	ReservationNumber string    `json:"reservation_number"`
	CheckIn           time.Time `json:"check_in,omitempty"`
	CheckOut          time.Time `json:"check_out,omitempty"`
	Email             string    `json:"email,omitempty"`
}

// PMSCacheResult mirrors scd's /v1/admin/pms/{name}/cache response.
type PMSCacheResult struct {
	Provider string        `json:"provider"`
	Kind     string        `json:"kind"`
	Count    int           `json:"count"`
	Rows     []PMSCacheRow `json:"rows"`
}

// PMSHealthSnapshot mirrors scd's in-process pms.Health. Unset times come
// back as zero values and should be treated as "never".
type PMSHealthSnapshot struct {
	Status         string    `json:"status"`
	ConnectedSince time.Time `json:"connected_since,omitempty"`
	LastRecordAt   time.Time `json:"last_record_at,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	LastErrorAt    time.Time `json:"last_error_at,omitempty"`
	CacheSize      int       `json:"cache_size"`
}

// PMSHealthResult mirrors scd's /v1/admin/pms/{name}/health response.
type PMSHealthResult struct {
	Provider string            `json:"provider"`
	Kind     string            `json:"kind"`
	Health   PMSHealthSnapshot `json:"health"`
}
