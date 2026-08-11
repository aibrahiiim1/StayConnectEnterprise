package pmsd

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

// pgRepo is the PostgreSQL-backed, assignment-scoped typed PMS repository (iam_v2). It uses ONE pool for its
// whole lifetime (no pool-per-operation). Constructed only when the connector is ON.
type pgRepo struct {
	pool *pgxpool.Pool
	owns bool // true if Close should close the pool (false when the daemon shares one pool across scopes)
}

// NewPgRepo opens a single OWNED pool and returns the typed repository (Close closes the pool). Used by
// integration tests and single-scope callers.
func NewPgRepo(ctx context.Context, dsn string) (*pgRepo, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return &pgRepo{pool: pool, owns: true}, nil
}

// NewPgRepoFromPool wraps a SHARED pool (Close is a no-op). The daemon owns the pool once and shares it
// across assignment scopes + the dedicated-connection lockers, so re-scoping never churns the pool.
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

func (r *pgRepo) ListActiveInterfaces(ctx context.Context, tenantID, siteID string) ([]Interface, error) {
	rows, err := r.pool.Query(ctx, `SELECT tenant_id::text, site_id::text, id::text, connector_kind,
		lifecycle_state, COALESCE(current_revision_id::text,'') FROM iam_v2.pms_interfaces
		WHERE tenant_id=$1 AND site_id=$2 AND lifecycle_state='ACTIVE'`, tenantID, siteID)
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
	err := r.pool.QueryRow(ctx, `SELECT pi.tenant_id::text, pi.site_id::text, pi.id::text, pi.connector_kind,
		pi.lifecycle_state, COALESCE(pi.current_revision_id::text,''),
		COALESCE(pr.id::text,''), COALESCE(pr.source_timezone,''), COALESCE(pr.config->>'endpoint',''),
		(pr.config->'auth'->>'read_only')::boolean,
		pr.normalization_version,
		(pr.config->>'dial_timeout_ms')::bigint, (pr.config->>'read_timeout_ms')::bigint,
		(pr.config->>'write_timeout_ms')::bigint, (pr.config->>'heartbeat_interval_ms')::bigint,
		(pr.config->>'heartbeat_timeout_ms')::bigint, (pr.config->>'feed_freshness_ms')::bigint,
		(pr.config->>'complete_sync_ms')::bigint, (pr.config->>'resync_supported')::boolean,
		COALESCE(pr.config->'auth'->>'credential_mode','')
		FROM iam_v2.pms_interfaces pi
		LEFT JOIN iam_v2.pms_interface_revisions pr
		  ON pr.tenant_id=pi.tenant_id AND pr.site_id=pi.site_id AND pr.pms_interface_id=pi.id AND pr.id=pi.current_revision_id
		WHERE pi.tenant_id=$1 AND pi.site_id=$2 AND pi.id=$3`, tenantID, siteID, interfaceID).
		Scan(&i.TenantID, &i.SiteID, &i.ID, &i.ConnectorKind, &i.LifecycleState, &i.CurrentRevisionID,
			&rev.ID, &rev.SourceTimezone, &rev.Endpoint, &readOnly, &normVer,
			&dialMs, &readMs, &writeMs, &hbIntMs, &hbToMs, &freshMs, &syncMs, &resync, &credMode)
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

// AllocateRuntimeGeneration atomically sets runtime_generation = stored+1 and pins the revision + secret
// generation, returning the new generation. First allocation for a fresh row is 1.
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

// cas runs an axis UPDATE guarded by the EXACT expected generation. Zero rows affected => a newer owner
// exists => ErrStaleGeneration.
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

func (r *pgRepo) UpdateSync(ctx context.Context, u SyncUpdate) error {
	return r.cas(ctx, `UPDATE iam_v2.pms_interface_runtime SET
		sync_status=$5, resync_requested_at=COALESCE($6,resync_requested_at),
		resync_started_at=COALESCE($7,resync_started_at), last_complete_sync_at=COALESCE($8,last_complete_sync_at),
		sync_cursor=COALESCE(NULLIF($9,''),sync_cursor), last_sync_failure_code=NULLIF($10,''), updated_at=now()
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND runtime_generation=$4`,
		u.TenantID, u.SiteID, u.PMSInterfaceID, u.ExpectedGeneration,
		string(u.Status), u.ResyncRequestedAt, u.ResyncStartedAt, u.LastCompleteSyncAt, u.SyncCursor, u.FailureCode.String())
}

// MarkGapAndRequireResync atomically moves BOTH the continuity axis (→GAP_DETECTED) and the sync axis
// (→RESYNC_REQUIRED) in ONE transaction, guarded by the exact runtime_generation. It is a single-row UPDATE
// (inherently all-or-none) wrapped in an explicit transaction. resync_started_at is reset to NULL because a
// fresh RESYNC_REQUIRED abandons any in-progress resync — and leaving a past resync_started_at alongside a
// new resync_requested_at would violate the pir_resync_coherent CHECK. Transport columns are untouched.
// Zero rows affected ⇒ a newer owner exists ⇒ ErrStaleGeneration. Every DB error is returned.
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

// AllocateResyncGeneration bumps resync_generation_seq by 1 under the exact runtime-generation CAS.
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

// insertInboxRow appends one stay_events inbox row inside a transaction that FIRST proves the caller still
// owns the exact runtime generation (SELECT ... FOR ... the runtime row), then inserts. Returns the row id.
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

func (r *pgRepo) StageResyncEvent(ctx context.Context, row InboxRow) (string, error) {
	row.AdmissionKind = "RESYNC"
	return r.insertInboxRow(ctx, row)
}

// PublishResyncGeneration advances the publication boundary in ONE atomic row update and marks the interface
// IN_SYNC + CONTINUOUS. The generation must not exceed the allocated seq (guarded here + by the CHECK).
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

// pgLocker is a session-level single-owner advisory lock on a DEDICATED connection acquired from the shared
// repository pool (no pool-per-lock). Close releases the lock + the connection deterministically.
type pgLocker struct {
	conn *pgxpool.Conn
	lost chan struct{}
	once sync.Once
	key  int64
	held bool
	stop chan struct{}
}

// NewPgLocker acquires ONE dedicated connection from the shared pool for a single-owner advisory lock.
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
