package pmsd

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

// pgRepo is the assignment-scoped PMS repository (iam_v2); one pool for its whole lifetime.
type pgRepo struct {
	pool *pgxpool.Pool
	owns bool // true if Close should close the pool (false when the daemon shares one pool across scopes)
}

// NewPgRepo opens an owned pool (Close closes it).
func NewPgRepo(ctx context.Context, dsn string) (*pgRepo, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return &pgRepo{pool: pool, owns: true}, nil
}

// NewPgRepoFromPool wraps a shared pool (Close is a no-op) so re-scoping never churns the pool.
func NewPgRepoFromPool(pool *pgxpool.Pool) *pgRepo { return &pgRepo{pool: pool, owns: false} }

// Pool exposes the pool so the daemon can build a dedicated-connection locker from it (no second pool).
func (r *pgRepo) Pool() *pgxpool.Pool { return r.pool }

func (r *pgRepo) Close() error {
	if r.owns && r.pool != nil {
		r.pool.Close()
	}
	r.pool = nil
	return nil
}

// ListActiveInterfaces returns ACTIVE interfaces plus DRAINING ones with undrained room charges (dropping the
// link at once would strand authorized charges). The Stay-Event applier keeps to ACTIVE only.
func (r *pgRepo) ListActiveInterfaces(ctx context.Context, tenantID, siteID string) ([]Interface, error) {
	rows, err := r.pool.Query(ctx, `SELECT tenant_id::text, site_id::text, id::text, connector_kind,
		lifecycle_state, COALESCE(current_revision_id::text,'') FROM iam_v2.pms_interfaces
		WHERE tenant_id=$1 AND site_id=$2
		  AND (lifecycle_state='ACTIVE'
		       OR (lifecycle_state='DRAINING'
		           AND iam_v2.pms_interface_has_undrained_postings(tenant_id, site_id, id)))`, tenantID, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Interface
	for rows.Next() {
		var i Interface
		if err := rows.Scan(&i.TenantID, &i.SiteID, &i.ID, &i.ConnectorKind, &i.LifecycleState, &i.CurrentRevisionID); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (r *pgRepo) LoadInterface(ctx context.Context, tenantID, siteID, interfaceID string) (Interface, Revision, SecretGeneration, error) {
	var i Interface
	var rev Revision
	var readOnly *bool
	var normVer *int
	var dialMs, readMs, writeMs, hbIntMs, hbToMs, freshMs, syncMs *int64
	var resync *bool
	var credMode string
	var providerCfg string
	var tlsOn *bool
	var tlsPin string
	err := r.pool.QueryRow(ctx, `SELECT pi.tenant_id::text, pi.site_id::text, pi.id::text, pi.connector_kind,
		pi.lifecycle_state, COALESCE(pi.current_revision_id::text,''),
		COALESCE(pr.id::text,''), COALESCE(pr.source_timezone,''), COALESCE(pr.config->>'endpoint',''),
		(pr.config->'auth'->>'read_only')::boolean,
		pr.normalization_version,
		(pr.config->>'dial_timeout_ms')::bigint, (pr.config->>'read_timeout_ms')::bigint,
		(pr.config->>'write_timeout_ms')::bigint, (pr.config->>'heartbeat_interval_ms')::bigint,
		(pr.config->>'heartbeat_timeout_ms')::bigint, (pr.config->>'feed_freshness_ms')::bigint,
		(pr.config->>'complete_sync_ms')::bigint, (pr.config->>'resync_supported')::boolean,
		COALESCE(pr.config->'auth'->>'credential_mode',''),
		COALESCE((pr.config->'provider')::text,''),
		(pr.config->'tls'->>'enabled')::boolean, COALESCE(pr.config->'tls'->>'cert_sha256','')
		FROM iam_v2.pms_interfaces pi
		LEFT JOIN iam_v2.pms_interface_revisions pr
		  ON pr.tenant_id=pi.tenant_id AND pr.site_id=pi.site_id AND pr.pms_interface_id=pi.id AND pr.id=pi.current_revision_id
		WHERE pi.tenant_id=$1 AND pi.site_id=$2 AND pi.id=$3`, tenantID, siteID, interfaceID).
		Scan(&i.TenantID, &i.SiteID, &i.ID, &i.ConnectorKind, &i.LifecycleState, &i.CurrentRevisionID,
			&rev.ID, &rev.SourceTimezone, &rev.Endpoint, &readOnly, &normVer,
			&dialMs, &readMs, &writeMs, &hbIntMs, &hbToMs, &freshMs, &syncMs, &resync, &credMode, &providerCfg, &tlsOn, &tlsPin)
	if err != nil {
		return Interface{}, Revision{}, SecretGeneration{}, err
	}
	rev.ConnectorKind = i.ConnectorKind
	rev.Published = rev.ID != "" // the current revision IS the published one
	rev.ReadOnly = readOnly != nil && *readOnly
	rev.ResyncSupported = resync != nil && *resync
	if normVer != nil {
		rev.NormalizationVersion = *normVer
	}
	rev.DialTimeout = msDur(dialMs)
	rev.ReadTimeout = msDur(readMs)
	rev.WriteTimeout = msDur(writeMs)
	rev.HeartbeatInterval = msDur(hbIntMs)
	rev.HeartbeatTimeout = msDur(hbToMs)
	rev.FeedFreshnessBound = msDur(freshMs)
	rev.CompleteSyncBound = msDur(syncMs)
	rev.CredentialMode = credMode
	rev.TLS = tlsOn != nil && *tlsOn
	rev.TLSCertSHA256 = tlsPin
	if providerCfg != "" {
		rev.ProviderConfig = []byte(providerCfg)
	}
	if rev.CredentialMode == "" {
		rev.CredentialMode = CredentialAuthKey // fail-closed: an explicit NONE is required to skip the secret
	}

	// A Secret Generation is loaded/pinned ONLY for AUTH_KEY. A NONE (no-auth) connector fabricates none.
	var sg SecretGeneration
	if rev.RequiresSecret() {
		sgErr := r.pool.QueryRow(ctx, `SELECT id::text, generation_no FROM iam_v2.pms_interface_secret_generations
			WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND superseded_at IS NULL
			ORDER BY generation_no DESC LIMIT 1`, tenantID, siteID, interfaceID).Scan(&sg.ID, &sg.GenerationNo)
		if sgErr == nil {
			rev.ActiveSecretGenerationID = sg.ID
		}
	}
	return i, rev, sg, nil
}

func msDur(ms *int64) time.Duration {
	if ms == nil || *ms <= 0 {
		return 0
	}
	return time.Duration(*ms) * time.Millisecond
}

// AllocateRuntimeGeneration atomically sets runtime_generation = stored+1 and pins the revision and secret generation.
func (r *pgRepo) AllocateRuntimeGeneration(ctx context.Context, req GenerationRequest) (int64, error) {
	var gen int64
	credMode := req.CredentialMode
	if credMode == "" {
		credMode = "AUTH_KEY" // fail-closed: never silently allow a no-secret connection without an explicit NONE
	}
	err := r.pool.QueryRow(ctx, `INSERT INTO iam_v2.pms_interface_runtime
		(tenant_id, site_id, pms_interface_id, pinned_revision_id, pinned_secret_generation_id, credential_mode, runtime_generation, updated_at)
		VALUES ($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6,1,now())
		ON CONFLICT (tenant_id,site_id,pms_interface_id) DO UPDATE SET
		  runtime_generation = iam_v2.pms_interface_runtime.runtime_generation + 1,
		  pinned_revision_id = EXCLUDED.pinned_revision_id,
		  pinned_secret_generation_id = EXCLUDED.pinned_secret_generation_id,
		  credential_mode = EXCLUDED.credential_mode,
		  updated_at = now()
		RETURNING runtime_generation`,
		req.TenantID, req.SiteID, req.PMSInterfaceID, req.PinnedRevisionID, req.PinnedSecretGenerationID, credMode).Scan(&gen)
	return gen, err
}

// cas guards an axis UPDATE by the exact generation; zero rows means a newer owner (ErrStaleGeneration).
func (r *pgRepo) cas(ctx context.Context, sql string, args ...any) error {
	tag, err := r.pool.Exec(ctx, sql, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrStaleGeneration
	}
	return nil
}

func (r *pgRepo) UpdateTransport(ctx context.Context, u TransportUpdate) error {
	return r.cas(ctx, `UPDATE iam_v2.pms_interface_runtime SET
		transport_status=$5, last_connect_attempt_at=COALESCE($6,last_connect_attempt_at),
		last_connected_at=COALESCE($7,last_connected_at), last_heartbeat_at=COALESCE($8,last_heartbeat_at),
		disconnected_since=$9, transport_error_code=NULLIF($10,''), updated_at=now()
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND runtime_generation=$4`,
		u.TenantID, u.SiteID, u.PMSInterfaceID, u.ExpectedGeneration,
		string(u.Status), u.LastConnectAttemptAt, u.LastConnectedAt, u.LastHeartbeatAt, u.DisconnectedSince, u.ErrorCode.String())
}

func (r *pgRepo) UpdateContinuity(ctx context.Context, u ContinuityUpdate) error {
	return r.cas(ctx, `UPDATE iam_v2.pms_interface_runtime SET
		continuity_status=$5, last_valid_event_at=COALESCE($6,last_valid_event_at),
		discontinuity_detected_at=COALESCE($7,discontinuity_detected_at),
		last_resync_marker_at=COALESCE($8,last_resync_marker_at),
		last_event_cursor=COALESCE(NULLIF($9,''),last_event_cursor), updated_at=now()
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND runtime_generation=$4`,
		u.TenantID, u.SiteID, u.PMSInterfaceID, u.ExpectedGeneration,
		string(u.Status), u.LastValidEventAt, u.DiscontinuityAt, u.LastResyncMarkerAt, u.LastEventCursor)
}

// UpdateSync moves the sync axis under the runtime-generation CAS. Declaring RESYNC_REQUIRED must clear
// resync_started_at: pir_resync_coherent requires started_at >= requested_at, so a stale started_at from an
// abandoned DS..DE window makes the CHECK reject the row and the link wedges in a reconnect loop. Keyed off the
// status so every path declaring RESYNC_REQUIRED stays coherent.
func (r *pgRepo) UpdateSync(ctx context.Context, u SyncUpdate) error {
	return r.cas(ctx, `UPDATE iam_v2.pms_interface_runtime SET
		sync_status=$5, resync_requested_at=COALESCE($6,resync_requested_at),
		resync_started_at=CASE WHEN $5='RESYNC_REQUIRED' THEN NULL ELSE COALESCE($7,resync_started_at) END,
		last_complete_sync_at=COALESCE($8,last_complete_sync_at),
		sync_cursor=COALESCE(NULLIF($9,''),sync_cursor), last_sync_failure_code=NULLIF($10,''), updated_at=now()
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND runtime_generation=$4`,
		u.TenantID, u.SiteID, u.PMSInterfaceID, u.ExpectedGeneration,
		string(u.Status), u.ResyncRequestedAt, u.ResyncStartedAt, u.LastCompleteSyncAt, u.SyncCursor, u.FailureCode.String())
}

// MarkGapAndRequireResync moves the continuity (→GAP_DETECTED) and sync (→RESYNC_REQUIRED) axes in one
// transaction under the exact runtime_generation. resync_started_at is reset to NULL (see UpdateSync).
// Zero rows means a newer owner (ErrStaleGeneration).
func (r *pgRepo) MarkGapAndRequireResync(ctx context.Context, req GapResyncRequest) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit
	tag, err := tx.Exec(ctx, `UPDATE iam_v2.pms_interface_runtime SET
		continuity_status='GAP_DETECTED', discontinuity_detected_at=$5,
		sync_status='RESYNC_REQUIRED', resync_requested_at=$5, resync_started_at=NULL,
		last_sync_failure_code=NULLIF($6,''), updated_at=now()
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND runtime_generation=$4`,
		req.TenantID, req.SiteID, req.PMSInterfaceID, req.ExpectedGeneration, req.At, req.Reason.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrStaleGeneration
	}
	return tx.Commit(ctx)
}

func (r *pgRepo) AllocateResyncGeneration(ctx context.Context, req ResyncScope) (int64, error) {
	var g int64
	err := r.pool.QueryRow(ctx, `UPDATE iam_v2.pms_interface_runtime
		SET resync_generation_seq = resync_generation_seq + 1, updated_at = now()
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND runtime_generation=$4
		RETURNING resync_generation_seq`,
		req.TenantID, req.SiteID, req.PMSInterfaceID, req.ExpectedGeneration).Scan(&g)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrStaleGeneration // a newer owner exists
		}
		return 0, err
	}
	return g, nil
}

// insertInboxRow first proves, in the same transaction, that the caller still owns the runtime generation, then inserts.
func (r *pgRepo) insertInboxRow(ctx context.Context, row InboxRow) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Appending to the inbox is a write to the capability-scoped Stay family.
	if err := writerguard.Open(ctx, tx, writerguard.CapStay); err != nil {
		return "", err
	}
	// synchronous ownership proof under the exact runtime generation
	var owned int
	err = tx.QueryRow(ctx, `SELECT 1 FROM iam_v2.pms_interface_runtime
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND runtime_generation=$4 FOR UPDATE`,
		row.TenantID, row.SiteID, row.PMSInterfaceID, row.ExpectedGeneration).Scan(&owned)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrStaleGeneration
		}
		return "", err
	}
	payload := row.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO iam_v2.stay_events
		(tenant_id, site_id, pms_interface_id, external_event_identity, event_type,
		 pms_timestamp_raw, pms_timestamp_utc, source_timezone, received_at,
		 sequence_version, normalization_version, clock_suspect, payload,
		 admission_kind, admission_runtime_generation, resync_generation, fingerprint_key_version)
		VALUES ($1,$2,$3,$4,$5, NULLIF($6,''),$7, NULLIF($8,''), $9, $10,$11,$12,$13::jsonb, $14,$15,$16,$17)
		RETURNING id::text`,
		row.TenantID, row.SiteID, row.PMSInterfaceID, row.ExternalEventIdentity, row.EventType,
		row.PMSTimestampRaw, row.PMSTimestampUTC, row.SourceTimezone, row.ReceivedAt,
		row.SequenceVersion, row.NormalizationVersion, row.ClockSuspect, string(payload),
		row.AdmissionKind, row.ExpectedGeneration, row.ResyncGeneration, row.FingerprintKeyVersion).Scan(&id)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

func (r *pgRepo) AdmitLiveEvent(ctx context.Context, row InboxRow) (string, error) {
	row.AdmissionKind = "LIVE"
	row.ResyncGeneration = 0
	return r.insertInboxRow(ctx, row)
}

// FinancialResyncDue is the database's answer to "should the financial mirror be proven again now".
func (r *pgRepo) FinancialResyncDue(ctx context.Context, scope ResyncScope) (bool, error) {
	var due bool
	err := r.pool.QueryRow(ctx, `SELECT iam_v2.p4_financial_resync_due($1::uuid,$2::uuid,$3::uuid)`,
		scope.TenantID, scope.SiteID, scope.PMSInterfaceID).Scan(&due)
	return due, err
}

func (r *pgRepo) StageResyncEvent(ctx context.Context, row InboxRow) (string, error) {
	row.AdmissionKind = "RESYNC"
	return r.insertInboxRow(ctx, row)
}

// PublishResyncGeneration atomically advances the publication boundary and marks IN_SYNC + CONTINUOUS; the generation must not exceed the allocated seq.
func (r *pgRepo) PublishResyncGeneration(ctx context.Context, req ResyncScope, g int64) error {
	tag, err := r.pool.Exec(ctx, `UPDATE iam_v2.pms_interface_runtime SET
		published_resync_generation=$5,
		sync_status='IN_SYNC', last_complete_sync_at=$6, resync_started_at=NULL,
		continuity_status='CONTINUOUS', last_resync_marker_at=$6, updated_at=now()
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND runtime_generation=$4
		  AND resync_generation_seq >= $5`,
		req.TenantID, req.SiteID, req.PMSInterfaceID, req.ExpectedGeneration, g, req.At)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrStaleGeneration
	}
	return nil
}

// pgLocker holds a session-level advisory lock on a dedicated pooled connection; Close releases both.
type pgLocker struct {
	conn *pgxpool.Conn
	lost chan struct{}
	once sync.Once
	key  int64
	held bool
	stop chan struct{}
}

func NewPgLocker(ctx context.Context, pool *pgxpool.Pool) (*pgLocker, error) {
	c, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return &pgLocker{conn: c, lost: make(chan struct{}), stop: make(chan struct{})}, nil
}

func (l *pgLocker) TryLock(ctx context.Context, key int64) (bool, error) {
	var got bool
	if err := l.conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&got); err != nil {
		return false, err
	}
	if got {
		l.key = key
		l.held = true
		go l.watch()
	}
	return got, nil
}

func (l *pgLocker) watch() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			if err := l.conn.Ping(context.Background()); err != nil {
				l.once.Do(func() { close(l.lost) })
				return
			}
		}
	}
}

func (l *pgLocker) Lost() <-chan struct{} { return l.lost }

func (l *pgLocker) Close() error {
	if l.conn == nil {
		return nil
	}
	close(l.stop)
	if l.held {
		_, _ = l.conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, l.key)
		l.held = false
	}
	l.once.Do(func() { close(l.lost) })
	l.conn.Release()
	l.conn = nil
	return nil
}

// ClaimResyncCommand takes an operator's pending full-resync request in ONE statement (separate read and clear
// would let two workers both see it). The WHERE clause is the whole safety model:
//
//	runtime_generation=$4          this worker still owns the interface
//	resync_command_generation=$4   the command was issued against THIS worker
//	sync_status<>'RESYNC_IN_PROGRESS'  no second DR can be opened
//
// Zero rows means nothing to do, for any of the three reasons. A worker dying between claim and DR loses the
// request instead of repeating it: a duplicated DR mid-roster is worse than a lost click.
func (r *pgRepo) ClaimResyncCommand(ctx context.Context, req ResyncScope) (*ResyncCommand, error) {
	var c ResyncCommand
	// The CTE reads the command before the UPDATE clears it: RETURNING yields the NEW row (NULLs).
	err := r.pool.QueryRow(ctx, `
		WITH claim AS (
		  SELECT resync_command_id AS id,
		         resync_command_requested_at AS requested_at,
		         COALESCE(resync_command_reason,'') AS reason
		    FROM iam_v2.pms_interface_runtime
		   WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3
		     AND runtime_generation=$4
		     AND resync_command_id IS NOT NULL
		     AND resync_command_generation=$4
		     AND sync_status <> 'RESYNC_IN_PROGRESS'
		   FOR UPDATE
		)
		UPDATE iam_v2.pms_interface_runtime rt
		   SET resync_command_id=NULL, resync_command_requested_at=NULL, resync_command_reason=NULL,
		       resync_command_generation=NULL, resync_command_claimed_at=$5,
		       sync_stage='WAITING_FOR_PMS', sync_stage_at=$5, sync_failure_code=NULL,
		       sync_records_received=0, sync_records_skipped=0, updated_at=now()
		  FROM claim
		 WHERE rt.tenant_id=$1 AND rt.site_id=$2 AND rt.pms_interface_id=$3
		   AND rt.runtime_generation=$4
		RETURNING claim.id::text, claim.requested_at, claim.reason`,
		req.TenantID, req.SiteID, req.PMSInterfaceID, req.ExpectedGeneration, req.At).
		Scan(&c.ID, &c.RequestedAt, &c.Reason)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // nothing pending, not ours, or already resyncing — all the same answer
		}
		return nil, err
	}
	return &c, nil
}

// UpdateSyncStage records sync progress under the CAS. Counters are replaced, not incremented, so retries
// don't inflate the number an operator watches.
func (r *pgRepo) UpdateSyncStage(ctx context.Context, u StageUpdate) error {
	return r.cas(ctx, `UPDATE iam_v2.pms_interface_runtime SET
		sync_stage=$5, sync_stage_at=$6,
		sync_records_received=COALESCE($7,sync_records_received),
		sync_records_skipped=COALESCE($8,sync_records_skipped),
		sync_failure_code=NULLIF($9,''),
		last_sync_in_house_count=COALESCE($10,last_sync_in_house_count),
		updated_at=now()
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND runtime_generation=$4`,
		u.TenantID, u.SiteID, u.PMSInterfaceID, u.ExpectedGeneration,
		string(u.Stage), u.At, u.RecordsReceived, u.RecordsSkipped, u.FailureCode, u.InHouseCount)
}

// RecordResyncCoverage goes through the definer function, not an INSERT, so svc_pmsd has no table write and a coverage row without a sweep is inexpressible.
func (r *pgRepo) RecordResyncCoverage(ctx context.Context, req ResyncScope, generation int64,
	rooms []string, roster, vacant, conflicts int) error {
	_, err := r.pool.Exec(ctx,
		`SELECT iam_v2.pms_record_resync_coverage($1,$2,$3,$4,$5,$6,$7,$8)`,
		req.TenantID, req.SiteID, req.PMSInterfaceID, generation, rooms, roster, vacant, conflicts)
	return err
}

// ReconcileRoster applies the reconciliation for a published generation. A refusal (incomplete coverage,
// superseded generation, unhealthy link) is an outcome recorded in the run ledger, not an error; only the call
// not completing is a fault.
func (r *pgRepo) ReconcileRoster(ctx context.Context, req ResyncScope, generation int64) (ReconcileOutcome, error) {
	var o ReconcileOutcome
	err := r.pool.QueryRow(ctx,
		`SELECT outcome, roster_size, mirror_in_house, absent_from_roster, stays_closed,
		        rooms_enumerated, rooms_expected, protected
		   FROM iam_v2.pms_roster_reconcile($1,$2,$3,$4,$5,true,$6)`,
		req.TenantID, req.SiteID, req.PMSInterfaceID, generation,
		"pmsd (automatic, on a complete published roster)",
		"automatic reconciliation after resync generation "+strconv.FormatInt(generation, 10)).
		Scan(&o.Outcome, &o.RosterSize, &o.MirrorInHouse, &o.AbsentFromRoster, &o.StaysClosed,
			&o.RoomsEnumerated, &o.RoomsExpected, &o.Protected)
	return o, err
}

// LoadConnectionSettings reads this interface's reconnect bounds (defaults from the definer function when no
// row exists). Scoped per interface, not site: a flaky link's backoff must not slow a healthy sibling.
func (r *pgRepo) LoadConnectionSettings(ctx context.Context, tenantID, siteID, interfaceID string) (ConnectionSettings, error) {
	var minMs, maxMs, stableS int
	err := r.pool.QueryRow(ctx,
		`SELECT backoff_min_ms, backoff_max_ms, stable_reset_seconds
		   FROM iam_v2.pms_connection_settings_get($1::uuid,$2::uuid,$3::uuid)`,
		tenantID, siteID, interfaceID).
		Scan(&minMs, &maxMs, &stableS)
	if err != nil {
		return ConnectionSettings{}, err
	}
	return ConnectionSettings{
		BackoffMin:       time.Duration(minMs) * time.Millisecond,
		BackoffMax:       time.Duration(maxMs) * time.Millisecond,
		StableResetAfter: time.Duration(stableS) * time.Second,
	}, nil
}
