// Package pmsd is the dedicated read-only PMS connector runtime (Phase 3, ADR-0001). It owns each PMS
// Interface connection under a DB advisory single-owner lock, runs one independent supervised worker per
// Interface, and persists the three interface-level freshness axes (transport / feed-continuity /
// complete-sync) to iam_v2.pms_interface_runtime. Occupancy freshness is Stay-specific and is NOT written
// here. It reuses the accepted FIAS protocol layer (data-plane/internal/pms) and enforces a hard outbound
// allowlist: no financial Posting record (PS), no Posting engine, no P# allocation.
//
// EVERYTHING is DARK-gated: when the connector flag is OFF the daemon constructs no DB connection, no
// repository, no worker, and no PMS socket. All external effects are injected via Deps so the contract can
// be proven with spies (no live PostgreSQL or PMS required for the unit/race gates).
package pmsd

import (
	"context"
	"errors"
	"hash/fnv"
	"log/slog"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// TransportStatus / ContinuityStatus / SyncStatus mirror the iam_v2.pms_interface_runtime enums.
type TransportStatus string
type ContinuityStatus string
type SyncStatus string

const (
	TransportUnknown      TransportStatus = "UNKNOWN"
	TransportConnecting   TransportStatus = "CONNECTING"
	TransportConnected    TransportStatus = "CONNECTED"
	TransportDisconnected TransportStatus = "DISCONNECTED"
	TransportError        TransportStatus = "ERROR"

	ContinuityUnknown       ContinuityStatus = "UNKNOWN"
	ContinuityContinuous    ContinuityStatus = "CONTINUOUS"
	ContinuityDiscontinuous ContinuityStatus = "DISCONTINUOUS"
	ContinuityGapDetected   ContinuityStatus = "GAP_DETECTED"

	SyncUnknown          SyncStatus = "UNKNOWN"
	SyncInSync           SyncStatus = "IN_SYNC"
	SyncResyncRequired   SyncStatus = "RESYNC_REQUIRED"
	SyncResyncInProgress SyncStatus = "RESYNC_IN_PROGRESS"
	SyncFailed           SyncStatus = "SYNC_FAILED"
)

// Interface is a PMS Interface as re-read after ownership is acquired.
type Interface struct {
	TenantID          string
	SiteID            string
	ID                string
	ConnectorKind     string
	LifecycleState    string // ACTIVE | AUTH_DISABLED | DRAINING | DECOMMISSIONED
	CurrentRevisionID string
}

// Revision carries the pinned connector configuration (timezone, endpoint, freshness thresholds).
type Revision struct {
	ID             string
	SourceTimezone string
	Endpoint       string
	ReadOnly       bool
}

// RuntimeState is one durable snapshot of the three interface-level axes for an optimistic upsert.
type RuntimeState struct {
	TenantID         string
	SiteID           string
	PMSInterfaceID   string
	PinnedRevisionID string
	Generation       int64
	UpdatedAt        time.Time

	Transport            TransportStatus
	LastConnectAttemptAt *time.Time
	LastConnectedAt      *time.Time
	LastHeartbeatAt      *time.Time
	DisconnectedSince    *time.Time
	TransportErrorCode   string

	Continuity         ContinuityStatus
	LastValidEventAt   *time.Time
	LastEventCursor    string
	DiscontinuityAt    *time.Time
	LastResyncMarkerAt *time.Time

	Sync                SyncStatus
	ResyncRequestedAt   *time.Time
	ResyncStartedAt     *time.Time
	LastCompleteSyncAt  *time.Time
	SyncCursor          string
	LastSyncFailureCode string
}

// Repo is the typed PMS repository. Nothing here is constructed while the connector is dark.
type Repo interface {
	ListActiveInterfaces(ctx context.Context) ([]Interface, error)
	// LoadInterface re-reads the Interface, its current Revision and read-only capability AFTER ownership
	// is acquired (guards against a lifecycle/revision change between discovery and dial).
	LoadInterface(ctx context.Context, tenantID, siteID, interfaceID string) (Interface, Revision, error)
	// UpsertRuntime persists a runtime snapshot with an optimistic generation check: a write whose
	// Generation is older than the currently stored generation MUST be rejected (ErrStaleGeneration).
	UpsertRuntime(ctx context.Context, st RuntimeState) error
}

// Locker is a session-level single-owner advisory lock bound to a dedicated DB connection.
type Locker interface {
	// TryLock is NON-blocking (pg_try_advisory_lock). Returns false if a competing owner holds the key.
	TryLock(ctx context.Context, key int64) (bool, error)
	// Lost fires when the underlying dedicated DB session dies (ownership is gone).
	Lost() <-chan struct{}
	// Close releases the lock and the dedicated connection.
	Close() error
}

// Conn is an owned PMS protocol connection (read-only). Events drive freshness-axis updates.
type Conn interface {
	// Serve runs the read-only protocol loop until ctx is cancelled or the link fails, invoking the axis
	// callbacks as real protocol evidence is observed. It NEVER sends a financial (PS) record.
	Serve(ctx context.Context, sink AxisSink) error
	Close() error
}

// AxisSink receives real protocol evidence; the worker translates it into RuntimeState upserts.
type AxisSink interface {
	OnConnected(at time.Time)
	OnHeartbeat(at time.Time)
	OnValidEvent(at time.Time, cursor string)
	OnResyncMarker(at time.Time)
	OnCompleteSync(at time.Time, cursor string)
	OnDisconnected(at time.Time, sanitizedCode string)
}

// Deps injects every external effect so flags-OFF and failure paths are provable with spies.
type Deps struct {
	// OpenRepo constructs the DB-backed repository. NOT called while the connector is dark.
	OpenRepo func(ctx context.Context) (Repo, error)
	// NewLocker creates a dedicated single-owner lock session. NOT called while dark / before dial.
	NewLocker func(ctx context.Context) (Locker, error)
	// Dial opens the owned read-only PMS connection AFTER the lock + re-read succeed. NOT called while dark.
	Dial func(ctx context.Context, iface Interface, rev Revision) (Conn, error)

	Now func() time.Time
	Log *slog.Logger

	// tuning (all bounded; zero -> sane defaults)
	ReconcileInterval time.Duration
	BackoffMin        time.Duration
	BackoffMax        time.Duration
	StableResetAfter  time.Duration // a connection must stay up this long before backoff resets
	StopGrace         time.Duration

	// jitter is an injectable randomness source for deterministic backoff tests (nil -> time-based).
	jitter func(int64) int64
}

var (
	ErrStaleGeneration = errors.New("pmsd: stale runtime generation (a newer owner exists)")
	ErrConnectorDark   = errors.New("pmsd: connector flag OFF")
)

func (d *Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}
func (d *Deps) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}
func (d *Deps) withDefaults() {
	if d.ReconcileInterval <= 0 {
		d.ReconcileInterval = 30 * time.Second
	}
	if d.BackoffMin <= 0 {
		d.BackoffMin = 500 * time.Millisecond
	}
	if d.BackoffMax <= 0 {
		d.BackoffMax = 30 * time.Second
	}
	if d.StableResetAfter <= 0 {
		d.StableResetAfter = 60 * time.Second
	}
	if d.StopGrace <= 0 {
		d.StopGrace = 10 * time.Second
	}
}

// LockKey derives the deterministic single-owner advisory-lock key for a PMS Interface.
// Canonical representation: FNV-1a/64 of the UTF-8 bytes "tenant|site|interface", reinterpreted as a
// signed int64 (pg advisory locks take bigint). Documented + covered by TestLockKey_Deterministic.
func LockKey(tenantID, siteID, interfaceID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(tenantID))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(siteID))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(interfaceID))
	return int64(h.Sum64())
}

// Run is the daemon entry point. It is fail-closed and DARK-gated: with the connector flag OFF it
// constructs NO repository, NO lock, NO worker and NO PMS socket, and returns nil (clean exit). With the
// flag ON it builds the supervisor and serves until ctx is cancelled.
func Run(ctx context.Context, cfg iamv2.PMSConfig, deps Deps) error {
	if err := cfg.Validate(); err != nil { // malformed/incoherent flag set -> fail closed
		return err
	}
	if !cfg.ConnectorOn() {
		deps.log().Info("pmsd: connector flag OFF; no DB, repository, worker or PMS socket constructed",
			"flags", cfg.SafeFlagSummary())
		return nil
	}
	deps.withDefaults()
	repo, err := deps.OpenRepo(ctx)
	if err != nil {
		return err
	}
	sup := newSupervisor(cfg, repo, &deps)
	return sup.run(ctx)
}
