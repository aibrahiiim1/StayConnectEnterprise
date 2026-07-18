package pmsd

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// pgRepo is the PostgreSQL-backed typed PMS repository (iam_v2). Constructed only when the connector is ON.
type pgRepo struct{ pool *pgxpool.Pool }

// NewPgRepo opens the pool and returns the typed repository.
func NewPgRepo(ctx context.Context, dsn string) (Repo, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return &pgRepo{pool: pool}, nil
}

func (r *pgRepo) ListActiveInterfaces(ctx context.Context) ([]Interface, error) {
	rows, err := r.pool.Query(ctx, `SELECT tenant_id::text, site_id::text, id::text, connector_kind,
		lifecycle_state, COALESCE(current_revision_id::text,'') FROM iam_v2.pms_interfaces
		WHERE lifecycle_state='ACTIVE'`)
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

func (r *pgRepo) LoadInterface(ctx context.Context, tenantID, siteID, interfaceID string) (Interface, Revision, error) {
	var i Interface
	var rev Revision
	// read-only capability derives from the pinned revision config (auth.read_only defaults true for a
	// read-only Phase-3 connector); endpoint/timezone come from the current revision.
	err := r.pool.QueryRow(ctx, `SELECT pi.tenant_id::text, pi.site_id::text, pi.id::text, pi.connector_kind,
		pi.lifecycle_state, COALESCE(pi.current_revision_id::text,''),
		COALESCE(pr.id::text,''), COALESCE(pr.source_timezone,''),
		COALESCE(pr.config->>'endpoint',''), COALESCE((pr.config->'auth'->>'read_only')::boolean, true)
		FROM iam_v2.pms_interfaces pi
		LEFT JOIN iam_v2.pms_interface_revisions pr
		  ON pr.tenant_id=pi.tenant_id AND pr.site_id=pi.site_id AND pr.pms_interface_id=pi.id AND pr.id=pi.current_revision_id
		WHERE pi.tenant_id=$1 AND pi.site_id=$2 AND pi.id=$3`, tenantID, siteID, interfaceID).
		Scan(&i.TenantID, &i.SiteID, &i.ID, &i.ConnectorKind, &i.LifecycleState, &i.CurrentRevisionID,
			&rev.ID, &rev.SourceTimezone, &rev.Endpoint, &rev.ReadOnly)
	if err != nil {
		return Interface{}, Revision{}, err
	}
	return i, rev, nil
}

func (r *pgRepo) UpsertRuntime(ctx context.Context, st RuntimeState) error {
	// optimistic generation: only apply when the incoming generation is >= the stored one; a stale worker
	// (older generation) is rejected so a previous owner cannot overwrite the current owner's state.
	tag, err := r.pool.Exec(ctx, `INSERT INTO iam_v2.pms_interface_runtime
		(tenant_id,site_id,pms_interface_id,pinned_revision_id,runtime_generation,updated_at,
		 transport_status,last_connect_attempt_at,last_connected_at,last_heartbeat_at,disconnected_since,transport_error_code,
		 continuity_status,last_valid_event_at,last_event_cursor,discontinuity_detected_at,last_resync_marker_at,
		 sync_status,resync_requested_at,resync_started_at,last_complete_sync_at,sync_cursor,last_sync_failure_code)
		VALUES ($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,''),
		        $13,$14,NULLIF($15,''),$16,$17,$18,$19,$20,$21,NULLIF($22,''),NULLIF($23,''))
		ON CONFLICT (tenant_id,site_id,pms_interface_id) DO UPDATE SET
		  pinned_revision_id=EXCLUDED.pinned_revision_id, runtime_generation=EXCLUDED.runtime_generation,
		  updated_at=EXCLUDED.updated_at, transport_status=EXCLUDED.transport_status,
		  last_connect_attempt_at=EXCLUDED.last_connect_attempt_at, last_connected_at=EXCLUDED.last_connected_at,
		  last_heartbeat_at=EXCLUDED.last_heartbeat_at, disconnected_since=EXCLUDED.disconnected_since,
		  transport_error_code=EXCLUDED.transport_error_code, continuity_status=EXCLUDED.continuity_status,
		  last_valid_event_at=EXCLUDED.last_valid_event_at, last_event_cursor=EXCLUDED.last_event_cursor,
		  discontinuity_detected_at=EXCLUDED.discontinuity_detected_at, last_resync_marker_at=EXCLUDED.last_resync_marker_at,
		  sync_status=EXCLUDED.sync_status, resync_requested_at=EXCLUDED.resync_requested_at,
		  resync_started_at=EXCLUDED.resync_started_at, last_complete_sync_at=EXCLUDED.last_complete_sync_at,
		  sync_cursor=EXCLUDED.sync_cursor, last_sync_failure_code=EXCLUDED.last_sync_failure_code
		WHERE EXCLUDED.runtime_generation >= iam_v2.pms_interface_runtime.runtime_generation`,
		st.TenantID, st.SiteID, st.PMSInterfaceID, st.PinnedRevisionID, st.Generation, st.UpdatedAt,
		string(st.Transport), st.LastConnectAttemptAt, st.LastConnectedAt, st.LastHeartbeatAt, st.DisconnectedSince, st.TransportErrorCode,
		string(st.Continuity), st.LastValidEventAt, st.LastEventCursor, st.DiscontinuityAt, st.LastResyncMarkerAt,
		string(st.Sync), st.ResyncRequestedAt, st.ResyncStartedAt, st.LastCompleteSyncAt, st.SyncCursor, st.LastSyncFailureCode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrStaleGeneration
	}
	return nil
}

// pgLocker is a session-level single-owner advisory lock on a dedicated connection.
type pgLocker struct {
	conn *pgxpool.Conn
	lost chan struct{}
	once sync.Once
	key  int64
	held bool
}

// NewPgLocker acquires a dedicated connection for a single-owner advisory lock.
func NewPgLocker(ctx context.Context, dsn string) (Locker, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	c, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &pgLocker{conn: c, lost: make(chan struct{})}, nil
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

// watch closes lost() when the dedicated session dies (ownership gone).
func (l *pgLocker) watch() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		if err := l.conn.Ping(context.Background()); err != nil {
			l.once.Do(func() { close(l.lost) })
			return
		}
	}
}

func (l *pgLocker) Lost() <-chan struct{} { return l.lost }

func (l *pgLocker) Close() error {
	if l.conn == nil {
		return nil
	}
	if l.held {
		_, _ = l.conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, l.key)
		l.held = false
	}
	l.once.Do(func() { close(l.lost) })
	l.conn.Release()
	l.conn = nil
	return nil
}

var errLiveDialDeferred = errors.New("pmsd: live read-only PMS dial is enabled at live verification (increment 9); connector remains dark")

// DialFIAS is the production dial. It REUSES the accepted FIAS protocol implementation
// (internal/pms.ProtelFIAS) — pmsd builds no second parser/stack — and enforces the outbound allowlist.
// Phase-3 Increment 3 opens no live socket: Serve returns errLiveDialDeferred (dark). Live serving is
// wired to the reused provider at Increment 9.
func DialFIAS(ctx context.Context, iface Interface, rev Revision) (Conn, error) {
	// reuse-proof: construct the accepted provider (no second FIAS stack in pmsd).
	provider := pms.NewProtelFIAS("pmsd-" + iface.ID)
	return &fiasConn{provider: provider, iface: iface, rev: rev}, nil
}

type fiasConn struct {
	provider *pms.ProtelFIAS
	iface    Interface
	rev      Revision
}

func (c *fiasConn) Serve(ctx context.Context, sink AxisSink) error {
	// Outbound is restricted to the read-only allowlist; a financial (PS) frame can never be emitted.
	for _, rec := range []string{"LS", "LD", "LR"} {
		if err := CheckOutbound(rec); err != nil {
			return err
		}
	}
	// Increment 3 is DARK: do not open a live socket. (Live serving via c.provider is enabled at inc 9.)
	return errLiveDialDeferred
}

func (c *fiasConn) Close() error { return nil }
