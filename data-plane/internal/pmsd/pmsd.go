// Package pmsd is the dedicated read-only PMS connector runtime (Phase 3, ADR-0001). It owns each PMS
// Interface connection under a DB advisory single-owner lock, runs one independent supervised worker per
// Interface, and persists the interface-level freshness axes (transport / feed-continuity / complete-sync)
// to iam_v2.pms_interface_runtime via INDEPENDENT compare-and-set updates. It reuses the accepted FIAS
// protocol layer (data-plane/internal/pms) and enforces a hard outbound allowlist at the socket write
// chokepoint: no financial Posting record (PS/PA), no Posting engine, no P# allocation.
//
// Identity is assignment-scoped: Tenant/Site derive ONLY from the verified signed appliance assignment.
// EVERYTHING is DARK-gated: with the connector flag OFF the daemon loads no assignment, constructs no DB
// connection, reads no secret, starts no worker and opens no PMS socket. All external effects are injected
// via Deps so the contract is provable with spies (no live PostgreSQL or PMS required for the unit/race
// gates).
package pmsd

import (
	"context"
	"errors"
	"log/slog"
	"sync"
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

// Assignment is the verified signed appliance assignment scope. It is the ONLY source of Tenant/Site
// identity — never environment or client input.
type Assignment struct {
	ApplianceID string
	TenantID    string
	SiteID      string
}

// Interface is a PMS Interface as discovered/re-read within the assigned scope.
type Interface struct {
	TenantID          string
	SiteID            string
	ID                string
	ConnectorKind     string
	LifecycleState    string // ACTIVE | AUTH_DISABLED | DRAINING | DECOMMISSIONED
	CurrentRevisionID string
}

// Revision carries the fully-typed pinned connector configuration. Nothing here defaults implicitly;
// Validate() fails closed on any missing/incoherent field.
type Revision struct {
	ID                       string
	ConnectorKind            string
	Endpoint                 string
	SourceTimezone           string
	ReadOnly                 bool // must be EXPLICITLY true; never defaulted
	NormalizationVersion     int
	DialTimeout              time.Duration
	ReadTimeout              time.Duration
	WriteTimeout             time.Duration
	HeartbeatInterval        time.Duration
	HeartbeatTimeout         time.Duration
	FeedFreshnessBound       time.Duration
	CompleteSyncBound        time.Duration
	ResyncSupported          bool
	Published                bool
	CredentialMode           string // "NONE" (no-auth transport, e.g. Protel FIAS) | "AUTH_KEY"
	ActiveSecretGenerationID string
}

// Credential modes. Protel FIAS is a no-auth transport → NONE (no Secret Generation is fabricated,
// required, decrypted or pinned). AUTH_KEY connectors MUST carry a Secret Generation.
const (
	CredentialNone    = "NONE"
	CredentialAuthKey = "AUTH_KEY"
)

var supportedConnectorKinds = map[string]struct{}{"protel-fias": {}}
var supportedCredentialModes = map[string]struct{}{CredentialNone: {}, CredentialAuthKey: {}}

// RequiresSecret reports whether this revision's credential mode needs a Secret Generation.
func (r Revision) RequiresSecret() bool { return r.CredentialMode == CredentialAuthKey }

// Validate enforces the typed connector configuration. Missing read-only capability FAILS — it is never
// treated as true. Every timeout/threshold must be a positive duration.
func (r Revision) Validate() error {
	switch {
	case !r.Published:
		return coded(CodeRevisionInvalid, errors.New("revision not published"))
	case r.ID == "":
		return coded(CodeRevisionInvalid, errors.New("missing revision id"))
	case r.ConnectorKind == "":
		return coded(CodeRevisionInvalid, errors.New("missing connector kind"))
	case !r.ReadOnly:
		return coded(CodeRevisionInvalid, errors.New("read-only capability absent or false"))
	case r.Endpoint == "":
		return coded(CodeRevisionInvalid, errors.New("missing endpoint"))
	case r.SourceTimezone == "":
		return coded(CodeRevisionInvalid, errors.New("missing source timezone"))
	case r.NormalizationVersion <= 0:
		return coded(CodeRevisionInvalid, errors.New("normalization version must be > 0"))
	}
	if _, ok := supportedCredentialModes[r.CredentialMode]; !ok {
		return coded(CodeRevisionInvalid, errors.New("credential mode must be explicit (NONE or AUTH_KEY)"))
	}
	// a Secret Generation is REQUIRED only for AUTH_KEY; a NONE connector must NOT carry one (no fabricated
	// secret for a no-auth transport).
	if r.RequiresSecret() && r.ActiveSecretGenerationID == "" {
		return coded(CodeSecretMissing, errors.New("AUTH_KEY connector requires an active secret generation"))
	}
	if !r.RequiresSecret() && r.ActiveSecretGenerationID != "" {
		return coded(CodeRevisionInvalid, errors.New("NONE connector must not carry a secret generation"))
	}
	if _, ok := supportedConnectorKinds[r.ConnectorKind]; !ok {
		return coded(CodeRevisionInvalid, errors.New("unsupported connector kind"))
	}
	for _, d := range []time.Duration{
		r.DialTimeout, r.ReadTimeout, r.WriteTimeout, r.HeartbeatInterval, r.HeartbeatTimeout,
		r.FeedFreshnessBound, r.CompleteSyncBound,
	} {
		if d <= 0 {
			return coded(CodeConfigInvalid, errors.New("timeout/threshold must be a positive duration"))
		}
	}
	if _, err := loadLocation(r.SourceTimezone); err != nil {
		return coded(CodeConfigInvalid, errors.New("invalid source timezone"))
	}
	return nil
}

// SecretGeneration is the identity + metadata of the active secret generation (never key material).
type SecretGeneration struct {
	ID           string
	GenerationNo int
	Superseded   bool
}

// SecretMaterial is transient decrypted secret bytes. It is zeroed after dial and never logged.
type SecretMaterial struct{ b []byte }

func NewSecretMaterial(b []byte) SecretMaterial { return SecretMaterial{b: b} }
func (s *SecretMaterial) Bytes() []byte         { return s.b }
func (s *SecretMaterial) Zero() {
	for i := range s.b {
		s.b[i] = 0
	}
	s.b = nil
}

// GenerationRequest atomically allocates the next runtime generation and pins the selected revision +
// secret generation.
type GenerationRequest struct {
	TenantID                 string
	SiteID                   string
	PMSInterfaceID           string
	PinnedRevisionID         string
	PinnedSecretGenerationID string // empty for a NONE credential mode
	CredentialMode           string // "NONE" | "AUTH_KEY"
}

// axisBase is the common identity + CAS guard for every independent-axis update.
type axisBase struct {
	TenantID           string
	SiteID             string
	PMSInterfaceID     string
	ExpectedGeneration int64
	At                 time.Time
}

// TransportUpdate mutates ONLY the transport axis (never continuity/sync columns).
type TransportUpdate struct {
	axisBase
	Status               TransportStatus
	LastConnectAttemptAt *time.Time
	LastConnectedAt      *time.Time
	LastHeartbeatAt      *time.Time
	DisconnectedSince    *time.Time
	ErrorCode            Code
}

// ContinuityUpdate mutates ONLY the feed-continuity axis.
type ContinuityUpdate struct {
	axisBase
	Status             ContinuityStatus
	LastValidEventAt   *time.Time
	DiscontinuityAt    *time.Time
	LastResyncMarkerAt *time.Time
	LastEventCursor    string
}

// SyncUpdate mutates ONLY the complete-sync axis.
type SyncUpdate struct {
	axisBase
	Status             SyncStatus
	ResyncRequestedAt  *time.Time
	ResyncStartedAt    *time.Time
	LastCompleteSyncAt *time.Time
	SyncCursor         string
	FailureCode        Code
}

// InboxRow is the typed, provenance-bound evidence appended to the durable inbox (iam_v2.stay_events) for a
// single admitted/staged domain Event. It NEVER carries the raw STX/ETX frame — only the bounded typed
// fields Increment 4 consumes. AdmissionKind is "LIVE" (immediately consumable) or "RESYNC" (staged under a
// typed ResyncGeneration, consumable only once the interface's published boundary reaches it).
type InboxRow struct {
	axisBase              // tenant/site/interface + ExpectedGeneration (the pinned runtime generation) + At
	AdmissionKind         string
	ResyncGeneration      int64 // 0 for LIVE; >0 for RESYNC
	ExternalEventIdentity string
	FingerprintKeyVersion int
	EventType             string // GI | GC | GO
	PMSTimestampRaw       string
	PMSTimestampUTC       *time.Time
	SourceTimezone        string
	ReceivedAt            time.Time
	SequenceVersion       int64
	NormalizationVersion  int
	ClockSuspect          bool
	Payload               []byte // typed JSON payload (bounded; no raw frame / no secret)
}

// GapResyncRequest drives the ATOMIC two-axis "feed gap detected → full resync required" transition. Both
// the continuity axis (→ GAP_DETECTED) and the sync axis (→ RESYNC_REQUIRED) move together, in ONE
// transaction, guarded by the exact runtime_generation. Reason is a bounded typed code persisted where the
// schema supports it (never raw text/PII).
type GapResyncRequest struct {
	axisBase
	Reason Code
}

// Repo is the typed, assignment-scoped PMS repository. Every method takes/embeds Tenant+Site; nothing is
// constructed while the connector is dark. Each Update uses an EXACT compare-and-set on runtime_generation.
type Repo interface {
	ListActiveInterfaces(ctx context.Context, tenantID, siteID string) ([]Interface, error)
	LoadInterface(ctx context.Context, tenantID, siteID, interfaceID string) (Interface, Revision, SecretGeneration, error)
	// AllocateRuntimeGeneration atomically sets runtime_generation = stored+1, pins the revision + secret
	// generation, and returns the new generation.
	AllocateRuntimeGeneration(ctx context.Context, req GenerationRequest) (int64, error)
	UpdateTransport(ctx context.Context, u TransportUpdate) error
	UpdateContinuity(ctx context.Context, u ContinuityUpdate) error
	UpdateSync(ctx context.Context, u SyncUpdate) error
	// MarkGapAndRequireResync atomically transitions continuity→GAP_DETECTED AND sync→RESYNC_REQUIRED in ONE
	// transaction under the exact generation guard (both axes change or neither). It preserves unrelated
	// transport evidence, returns ErrStaleGeneration when ownership changed, and returns every database error.
	MarkGapAndRequireResync(ctx context.Context, req GapResyncRequest) error

	// ---- §G durable inbox + typed resync generation (all guarded by the exact runtime generation) ----

	// AllocateResyncGeneration bumps the interface's monotonic resync_generation_seq by 1 (a NEW typed resync
	// generation) under the exact runtime-generation CAS and returns it. ErrStaleGeneration if ownership moved.
	AllocateResyncGeneration(ctx context.Context, req ResyncScope) (int64, error)
	// AdmitLiveEvent APPENDS a durable LIVE inbox row inside ONE transaction that first proves the caller still
	// owns the exact runtime generation, then inserts. It returns the durable row id (the ONLY thing exposed to
	// the Stay engine). A stale owner inserts nothing and gets ErrStaleGeneration.
	AdmitLiveEvent(ctx context.Context, row InboxRow) (string, error)
	// StageResyncEvent APPENDS a durable RESYNC inbox row (immutable, STAGED) under the same ownership proof +
	// runtime-generation CAS. Staged rows are invisible to the Stay engine until publication.
	StageResyncEvent(ctx context.Context, row InboxRow) (string, error)
	// PublishResyncGeneration advances published_resync_generation to g in ONE atomic row update (never a mass
	// Event-row update) under the exact runtime-generation CAS, and marks the interface IN_SYNC + CONTINUOUS.
	// g must not exceed the allocated seq. ErrStaleGeneration if ownership moved.
	PublishResyncGeneration(ctx context.Context, req ResyncScope, g int64) error
	Close() error
}

// ResyncScope identifies an interface + the pinned runtime generation for a resync-lifecycle operation.
type ResyncScope struct {
	axisBase
}

// Locker is a session-level single-owner advisory lock bound to a dedicated DB connection.
type Locker interface {
	TryLock(ctx context.Context, key int64) (bool, error)
	Lost() <-chan struct{}
	Close() error
}

// DialParams carries everything the adapter needs to open a read-only connection. Secret is zeroed by the
// worker after Dial returns.
type DialParams struct {
	Iface  Interface
	Rev    Revision
	Secret SecretMaterial
}

// Conn is an owned read-only PMS protocol connection.
type Conn interface {
	// Serve runs the read-only protocol loop until ctx is cancelled or the link fails, invoking sink as
	// real protocol evidence is observed. It NEVER sends a financial (PS/PA) record.
	Serve(ctx context.Context, sink AxisSink) error
	Close() error
}

// AxisSink receives real protocol evidence. The worker implements it, translating control observations into
// independent-axis CAS updates and domain records into typed queue events. All methods use the worker
// context and the pinned generation.
type AxisSink interface {
	OnConnected(at time.Time) error
	OnHeartbeat(at time.Time) error
	// RequireInitialResync raises the application barrier at connect: sync→RESYNC_REQUIRED and no LIVE
	// admission until a complete DS→DE generation is published. The adapter then sends the initial DR.
	RequireInitialResync(at time.Time) error
	OnResyncStart(at time.Time) error
	OnResyncComplete(at time.Time, cursor string) error
	OnDisconnected(at time.Time, code Code) error
	// OnDomainEvent validates + enqueues a typed guest Stay mutation. On queue overflow it drives
	// continuity→GAP_DETECTED and sync→RESYNC_REQUIRED and returns a QUEUE_OVERFLOW error so the adapter
	// stops normal application until a verified resync.
	OnDomainEvent(ctx context.Context, ev Event) error
	// OnContinuityFault records a feed-continuity fault for a record the adapter could NOT admit as a valid
	// typed event (malformed framing, overlong/identity-truncating field, failed normalization). It DRIVES
	// continuity→GAP_DETECTED and sync→RESYNC_REQUIRED and PERSISTS both under the pinned generation; the
	// adapter must never silently drop such a record. A persist failure is returned so the transport closes
	// (a fault we cannot durably record must not be swallowed).
	OnContinuityFault(ctx context.Context, code Code) error
}

// Deps injects every external effect so flags-OFF and failure paths are provable with spies.
type Deps struct {
	// LoadAssignment loads + cryptographically verifies the signed appliance assignment. NOT called while
	// dark. Returns assigned=false for a factory-clean/unassigned appliance.
	LoadAssignment func(ctx context.Context) (Assignment, bool, error)
	// OpenRepo constructs the DB-backed repository scoped to the assignment. NOT called while dark.
	OpenRepo func(ctx context.Context, a Assignment) (Repo, error)
	// NewLocker creates a dedicated single-owner lock session. NOT called while dark / before ownership.
	NewLocker func(ctx context.Context) (Locker, error)
	// DecryptSecret decrypts the selected secret generation AFTER ownership. Lock losers never call it.
	DecryptSecret func(ctx context.Context, iface Interface, rev Revision, sg SecretGeneration) (SecretMaterial, error)
	// Dial opens the owned read-only PMS connection AFTER lock + re-read + allocate + decrypt. Not while dark.
	Dial func(ctx context.Context, p DialParams) (Conn, error)

	// NewStayApplier builds the Stay-Event application owner (the Stay Engine with its Checkout Converter).
	// Called ONLY when the ingest flag is on; nil while dark. Returning an error, or a nil applier, is a
	// startup failure: applying PMS events without the engine is the unverified path Phase 3 removed.
	NewStayApplier func(ctx context.Context, a Assignment) (StayApplier, error)

	Now func() time.Time
	Log *slog.Logger

	// tuning (all bounded; zero -> sane defaults)
	ReconcileInterval   time.Duration
	BackoffMin          time.Duration
	BackoffMax          time.Duration
	StableResetAfter    time.Duration
	StopGrace           time.Duration
	QueueCapacity       int
	QueueEnqueueTimeout time.Duration

	jitter func(int64) int64
}

var (
	ErrStaleGeneration = errors.New("pmsd: stale runtime generation (a newer owner exists)")
	ErrConnectorDark   = errors.New("pmsd: connector flag OFF")
	ErrNoAssignment    = errors.New("pmsd: no verified signed appliance assignment (factory-clean)")
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
	if d.QueueCapacity <= 0 {
		d.QueueCapacity = 256
	}
	if d.QueueEnqueueTimeout <= 0 {
		d.QueueEnqueueTimeout = 2 * time.Second
	}
}

// startStayApplier launches one application loop per assigned PMS Interface and returns a channel closed
// when they have all drained. With the ingest flag OFF it starts nothing and closes immediately, so a
// connector-only deployment pays nothing for a surface it does not run.
func startStayApplier(ctx context.Context, cfg iamv2.PMSConfig, a Assignment, repo Repo, deps *Deps) <-chan struct{} {
	done := make(chan struct{})
	if !cfg.IngestOn() {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		ap, err := deps.NewStayApplier(ctx, a)
		if err != nil || ap == nil {
			// A missing applier at this point is a real fault, not a reason to apply events unsafely.
			logEvent(deps.log(), EventSupervisorNoAssignment, CodeConfigInvalid, SafeFields{Stage: StageDiscover})
			return
		}
		// Interfaces come from the same authoritative discovery the connector uses, so the applier covers
		// exactly the ACTIVE interfaces of the assigned scope and nothing else.
		ifaces, err := repo.ListActiveInterfaces(ctx, a.TenantID, a.SiteID)
		if err != nil {
			logEvent(deps.log(), EventSupervisorNoAssignment, Classify(err), SafeFields{Stage: StageDiscover})
			return
		}
		var wg sync.WaitGroup
		for _, iface := range ifaces {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				// one loop per Interface: an outage or backlog on one cannot stall another
				runApplierScope(ctx, ap, applierScope{Tenant: a.TenantID, Site: a.SiteID, Interface: id},
					applierConfig{}, deps.log())
			}(iface.ID)
		}
		wg.Wait()
	}()
	return done
}

// Run is the daemon entry point. Fail-closed and DARK-gated: with the connector flag OFF it loads NO
// assignment, constructs NO repository/lock/worker/socket and reads NO secret, returning nil. With the flag
// ON it verifies the signed assignment (fail-closed if unassigned), opens the scoped repository, and serves
// until ctx is cancelled.
func Run(ctx context.Context, cfg iamv2.PMSConfig, deps Deps) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	// Nothing is constructed unless at least one Phase-3 surface this daemon owns is live. With both the
	// connector and the ingest flags OFF there is no assignment load, no database handle, no worker and no
	// socket — the process starts and immediately reports that it has nothing to do.
	if !cfg.ConnectorOn() && !cfg.IngestOn() {
		deps.log().Info("pmsd: connector and ingest flags OFF; no assignment, DB, secret, worker or PMS socket",
			"flags", cfg.SafeFlagSummary())
		return nil
	}
	// The ingest surface REQUIRES an applier. Failing closed here means a misconfigured deployment cannot end
	// up admitting events that nothing applies (a silently growing inbox), nor applying checkouts without the
	// Converter that derives their boundary.
	if cfg.IngestOn() && deps.NewStayApplier == nil {
		return ErrApplierRequired
	}
	deps.withDefaults()
	// Loop so an assignment rotation drains the old scope and re-scopes to the new one. ctx cancellation
	// or an unassigned state ends the loop.
	for {
		if ctx.Err() != nil {
			return nil
		}
		assignment, assigned, err := deps.LoadAssignment(ctx)
		if err != nil {
			return err
		}
		if !assigned {
			logEvent(deps.log(), EventSupervisorNoAssignment, CodeAssignmentMissing, SafeFields{Stage: StageDiscover})
			return ErrNoAssignment // fail closed: a factory-clean appliance does no PMS work
		}
		repo, err := deps.OpenRepo(ctx, assignment)
		if err != nil {
			return err
		}
		// The Stay-Event applier is supervised ALONGSIDE the connector workers, not inside one: it owns a
		// different job (applying the durable inbox) and must keep running while a PMS socket is down.
		appCtx, stopApplier := context.WithCancel(ctx)
		applierDone := startStayApplier(appCtx, cfg, assignment, repo, &deps)

		sup := newSupervisor(cfg, assignment, repo, &deps)
		serr := sup.run(ctx) // drains all workers before returning

		stopApplier()
		<-applierDone    // drain: no application is left half-running when the scope ends
		_ = repo.Close() // §9 explicit repository ownership + close (per scope)
		if errors.Is(serr, errAssignmentChanged) {
			logEvent(deps.log(), EventSupervisorAssignChange, CodeAssignmentChanged, SafeFields{Stage: StageShutdown})
			continue // re-scope to the new assignment
		}
		return serr
	}
}
