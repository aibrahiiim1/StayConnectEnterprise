// Package pmsd is the PMS connector runtime: one supervised worker per Interface under a DB advisory
// single-owner lock, persisting the transport / continuity / sync axes via independent CAS updates.
// Tenant/Site derive only from the verified signed assignment. With the connector flag off nothing loads,
// connects or starts; all external effects are injected via Deps.
package pmsd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/pmsprovider"
)

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

// Assignment is the only source of Tenant/Site identity, never environment or client input.
type Assignment struct {
	ApplianceID string
	TenantID    string
	SiteID      string
}

type Interface struct {
	TenantID          string
	SiteID            string
	ID                string
	ConnectorKind     string
	LifecycleState    string // ACTIVE | AUTH_DISABLED | DRAINING | DECOMMISSIONED
	CurrentRevisionID string
}

// Revision is the pinned connector configuration; nothing defaults implicitly and Validate fails closed.
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
	// ProviderConfig is the raw config.provider JSON, REST connectors only.
	ProviderConfig []byte
	// TLSCertSHA256, when set, pins the PMS certificate by lower-case hex SHA-256 instead of system roots.
	TLS           bool
	TLSCertSHA256 string
}

// Protel FIAS is no-auth (NONE: no Secret Generation exists); AUTH_KEY connectors must carry one.
const (
	CredentialNone    = "NONE"
	CredentialAuthKey = "AUTH_KEY"
)

// supportedConnectorKinds derives from the provider registry, which edged's authoring allowlist shares.
var supportedConnectorKinds = pmsprovider.KindSet()
var supportedCredentialModes = map[string]struct{}{CredentialNone: {}, CredentialAuthKey: {}}

func (r Revision) RequiresSecret() bool { return r.CredentialMode == CredentialAuthKey }

// Validate fails closed: a missing read-only capability is never treated as true.
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

type GenerationRequest struct {
	TenantID                 string
	SiteID                   string
	PMSInterfaceID           string
	PinnedRevisionID         string
	PinnedSecretGenerationID string // empty for a NONE credential mode
	CredentialMode           string // "NONE" | "AUTH_KEY"
}

type axisBase struct {
	TenantID           string
	SiteID             string
	PMSInterfaceID     string
	ExpectedGeneration int64
	At                 time.Time
}

// Each axis update touches only its own columns.
type TransportUpdate struct {
	axisBase
	Status               TransportStatus
	LastConnectAttemptAt *time.Time
	LastConnectedAt      *time.Time
	LastHeartbeatAt      *time.Time
	DisconnectedSince    *time.Time
	ErrorCode            Code
}

type ContinuityUpdate struct {
	axisBase
	Status             ContinuityStatus
	LastValidEventAt   *time.Time
	DiscontinuityAt    *time.Time
	LastResyncMarkerAt *time.Time
	LastEventCursor    string
}

type SyncUpdate struct {
	axisBase
	Status             SyncStatus
	ResyncRequestedAt  *time.Time
	ResyncStartedAt    *time.Time
	LastCompleteSyncAt *time.Time
	SyncCursor         string
	FailureCode        Code
}

// InboxRow is the durable inbox evidence (iam_v2.stay_events) for one domain Event; it never carries the raw
// frame. AdmissionKind is "LIVE" (consumable now) or "RESYNC" (staged, consumable once the published
// boundary reaches its ResyncGeneration).
type InboxRow struct {
	axisBase
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

// GapResyncRequest drives the atomic continuity→GAP_DETECTED and sync→RESYNC_REQUIRED transition. Reason is
// a bounded code, never raw text or PII.
type GapResyncRequest struct {
	axisBase
	Reason Code
}

// Repo is the assignment-scoped repository; every write is a compare-and-set on the exact runtime_generation
// and returns ErrStaleGeneration when ownership moved.
type Repo interface {
	ListActiveInterfaces(ctx context.Context, tenantID, siteID string) ([]Interface, error)
	LoadInterface(ctx context.Context, tenantID, siteID, interfaceID string) (Interface, Revision, SecretGeneration, error)
	AllocateRuntimeGeneration(ctx context.Context, req GenerationRequest) (int64, error)
	UpdateTransport(ctx context.Context, u TransportUpdate) error
	UpdateContinuity(ctx context.Context, u ContinuityUpdate) error
	UpdateSync(ctx context.Context, u SyncUpdate) error
	// MarkGapAndRequireResync moves both axes in one transaction (both change or neither).
	MarkGapAndRequireResync(ctx context.Context, req GapResyncRequest) error

	AllocateResyncGeneration(ctx context.Context, req ResyncScope) (int64, error)
	// AdmitLiveEvent proves ownership and inserts in one transaction; a stale owner inserts nothing. An exact
	// repeat of an admitted LIVE record inserts nothing and returns the existing id with *DuplicateLiveEventError.
	AdmitLiveEvent(ctx context.Context, row InboxRow) (string, error)
	// StageResyncEvent appends an immutable STAGED row, invisible to the Stay engine until publication.
	StageResyncEvent(ctx context.Context, row InboxRow) (string, error)
	// LoadConnectionSettings is read-only for the connector (a daemon that could widen its own backoff
	// makes the configuration meaningless), keyed per interface so a flaky link doesn't slow a healthy one.
	LoadConnectionSettings(ctx context.Context, tenantID, siteID, interfaceID string) (ConnectionSettings, error)
	RecordResyncCoverage(ctx context.Context, req ResyncScope, generation int64, rooms []string, roster, vacant, conflicts int) error
	// ReconcileRoster runs once per published generation; a refusal is recorded in the run ledger, not an error.
	ReconcileRoster(ctx context.Context, req ResyncScope, generation int64) (ReconcileOutcome, error)
	// PublishResyncGeneration atomically advances the boundary (g must not exceed the allocated seq).
	PublishResyncGeneration(ctx context.Context, req ResyncScope, g int64) error
	// ClaimResyncCommand returns (nil, nil) when nothing is pending, the command belongs to a replaced
	// generation, or a resync is running; the caller treats all three the same.
	ClaimResyncCommand(ctx context.Context, req ResyncScope) (*ResyncCommand, error)
	UpdateSyncStage(ctx context.Context, u StageUpdate) error
	Close() error
}

type ResyncScope struct {
	axisBase
}

type Locker interface {
	TryLock(ctx context.Context, key int64) (bool, error)
	Lost() <-chan struct{}
	Close() error
}

// Secret is zeroed by the worker after Dial returns.
type DialParams struct {
	Iface  Interface
	Rev    Revision
	Secret SecretMaterial
}

type Conn interface {
	// Serve never sends a financial (PS/PA) record.
	Serve(ctx context.Context, sink AxisSink) error
	Close() error
}

// AxisSink is implemented by the worker; all methods use the worker context and the pinned generation.
type AxisSink interface {
	OnConnected(at time.Time) error
	OnHeartbeat(at time.Time) error
	// RequireInitialResync bars LIVE admission until a complete DS→DE generation is published.
	RequireInitialResync(at time.Time) error
	OnResyncStart(at time.Time) error
	OnResyncComplete(at time.Time, cursor string) error
	OnDisconnected(at time.Time, code Code) error
	// OnDomainEvent enqueues a guest Stay mutation; on queue overflow it drives GAP_DETECTED/RESYNC_REQUIRED
	// and returns QUEUE_OVERFLOW so the adapter stops applying until a verified resync.
	OnDomainEvent(ctx context.Context, ev Event) error
	// OnContinuityFault records a record the adapter could not admit (malformed, overlong, failed
	// normalization) and persists GAP_DETECTED/RESYNC_REQUIRED. The adapter must never silently drop one; a
	// persist failure is returned so the transport closes.
	OnContinuityFault(ctx context.Context, code Code) error
	// ClaimOperatorResync hides from the adapter that the command channel is a database row.
	ClaimOperatorResync() *ResyncCommand
	// RecordSkipped reports the running count of well-formed records describing no keyable Stay.
	RecordSkipped(n int64)
	// RecordCoverage is called once per DS..DE before publication: a published roster with no recorded
	// observation can never be judged complete by reconciliation.
	RecordCoverage(rooms []string, rosterRecords, vacantRecords, conflictingRooms int)
	// OnFullSyncRequested is called for the automatic initial sync as well as an operator's, so operators see
	// the same stages either way.
	OnFullSyncRequested()
}

// Deps injects every external effect. None is called while the connector is dark.
type Deps struct {
	// LoadAssignment returns assigned=false for a factory-clean appliance.
	LoadAssignment func(ctx context.Context) (Assignment, bool, error)
	OpenRepo       func(ctx context.Context, a Assignment) (Repo, error)
	NewLocker      func(ctx context.Context) (Locker, error)
	// DecryptSecret runs only after ownership; lock losers never call it.
	DecryptSecret func(ctx context.Context, iface Interface, rev Revision, sg SecretGeneration) (SecretMaterial, error)
	// Dial runs after lock, re-read, allocate and decrypt.
	Dial func(ctx context.Context, p DialParams) (Conn, error)

	// NewStayApplier is called only when the ingest flag is on. An error or nil applier is a startup failure:
	// applying PMS events without the engine is an unverified path.
	NewStayApplier func(ctx context.Context, a Assignment) (StayApplier, error)

	Now func() time.Time
	Log *slog.Logger

	// Tuning; zero selects defaults. ApplierReconcileInterval bounds how fast applier loops notice an Interface change.
	ApplierReconcileInterval time.Duration
	ReconcileInterval        time.Duration
	BackoffMin               time.Duration
	BackoffMax               time.Duration
	StableResetAfter         time.Duration
	StopGrace                time.Duration
	QueueCapacity            int
	QueueEnqueueTimeout      time.Duration

	jitter func(int64) int64
}

var (
	ErrStaleGeneration = errors.New("pmsd: stale runtime generation (a newer owner exists)")
	ErrConnectorDark   = errors.New("pmsd: connector flag OFF")
	ErrNoAssignment    = errors.New("pmsd: no verified signed appliance assignment (factory-clean)")
	// Distinct from ErrNoAssignment so "this appliance was rejected" is never read as "this appliance is new".
	ErrAssignmentNotGranting = errors.New("pmsd: appliance assignment present but grants no tenant/site scope")
	// Distinct from "no identity" so a corrupt or tampered identity is never reported as never-enrolled.
	ErrIdentityUnreadable = errors.New("pmsd: appliance identity present but unreadable or invalid")
)

// DuplicateLiveEventError reports that a LIVE record was already admitted with the same identity, so nothing
// was inserted. ProcessingStatus is the existing row's status.
type DuplicateLiveEventError struct {
	ExistingID       string
	ProcessingStatus string
}

func (e *DuplicateLiveEventError) Error() string { return "pmsd: live record already admitted" }

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

// buildStayApplier validates the applier before anything starts, rejecting a typed nil (non-nil to `== nil`,
// but it would silently apply nothing).
func buildStayApplier(ctx context.Context, cfg iamv2.PMSConfig, a Assignment, deps *Deps) (StayApplier, error) {
	if !cfg.IngestOn() {
		return nil, nil // dark: nothing is constructed and nothing will run
	}
	if deps.NewStayApplier == nil {
		return nil, ErrApplierRequired
	}
	ap, err := deps.NewStayApplier(ctx, a)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrApplierRequired, err)
	}
	if ap == nil || isTypedNil(ap) {
		return nil, ErrApplierRequired
	}
	return ap, nil
}

func isTypedNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return rv.IsNil()
	default:
		return false
	}
}

// startStayApplier returns a channel closed when all per-Interface application loops have drained.
func startStayApplier(ctx context.Context, cfg iamv2.PMSConfig, a Assignment, repo Repo, ap StayApplier, deps *Deps) <-chan struct{} {
	done := make(chan struct{})
	if !cfg.IngestOn() || ap == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		runApplierSupervisor(ctx, a, repo, ap, deps)
	}()
	return done
}

// Run fails closed: with the flags off nothing is loaded or opened; unassigned appliances do no PMS work.
func Run(ctx context.Context, cfg iamv2.PMSConfig, deps Deps) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if !cfg.ConnectorOn() && !cfg.IngestOn() {
		deps.log().Info("pmsd: connector and ingest flags OFF; no assignment, DB, secret, worker or PMS socket",
			"flags", cfg.SafeFlagSummary())
		return nil
	}
	// Ingest requires an applier, else events would be admitted that nothing applies (a growing inbox).
	if cfg.IngestOn() && deps.NewStayApplier == nil {
		return ErrApplierRequired
	}
	deps.withDefaults()
	// Loops so an assignment rotation drains the old scope and re-scopes.
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
		// Build the applier synchronously before any worker: a goroutine failure would leave the connector
		// admitting events nothing applies.
		ap, aerr := buildStayApplier(ctx, cfg, assignment, &deps)
		if aerr != nil {
			_ = repo.Close()
			return aerr
		}
		// Supervised alongside the workers, not inside one: it must keep running while a PMS socket is down.
		appCtx, stopApplier := context.WithCancel(ctx)
		applierDone := startStayApplier(appCtx, cfg, assignment, repo, ap, &deps)

		sup := newSupervisor(cfg, assignment, repo, &deps)
		serr := sup.run(ctx) // drains all workers before returning

		stopApplier()
		<-applierDone
		_ = repo.Close()
		if errors.Is(serr, errAssignmentChanged) {
			logEvent(deps.log(), EventSupervisorAssignChange, CodeAssignmentChanged, SafeFields{Stage: StageShutdown})
			continue // re-scope to the new assignment
		}
		return serr
	}
}

type ReconcileOutcome struct {
	Outcome          string
	RosterSize       int
	MirrorInHouse    int
	AbsentFromRoster int
	StaysClosed      int
	RoomsEnumerated  int
	RoomsExpected    int
	Protected        int
}

// ConnectionSettings bound the interval between reconnect attempts, never their number: the connector retries
// indefinitely so a PMS returning at 3am is picked up unattended.
type ConnectionSettings struct {
	BackoffMin       time.Duration
	BackoffMax       time.Duration
	StableResetAfter time.Duration
}
