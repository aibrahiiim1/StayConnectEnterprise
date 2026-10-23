package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Runtime half of FINANCIAL_RECOVERY_MODE; the database holds the rules. ReconcileEpoch must run before any financial
// worker starts: a worker draining an outbox on restored data has already sent what it sent.

type RecoveryStatus struct {
	Epoch      int64
	Reason     string
	Active     bool
	HeldTotal  int
	HeldOpen   int
	EnteredAt  string
	ReleasedAt string
}

type Hold struct {
	ID          string
	Kind        string
	WorkID      string
	HeldStatus  string
	AmountMinor *int64
	Currency    string
	Resolution  string
	HeldAt      string
}

// SystemIdentity comes from pg_control, not anything StayConnect writes: an application-stored value would be restored
// with everything else and agree with itself after the very event it should detect.
func SystemIdentity(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT system_identifier::text FROM pg_control_system()`).Scan(&id)
	if err != nil {
		return "", fail(ErrRepo, "could not read the database system identity")
	}
	return id, nil
}

// MarkerPath is deliberately outside the database directory so pg_dump and pg_restore do not touch it; that is how it
// carries the truth across a restore.
const MarkerPath = "/etc/stayconnect/financial-restore-generation.json"

// EnvMarkerPath lets a test point at a temporary file; it changes where the marker is read, never what it is trusted to
// do.
const EnvMarkerPath = "STAYCONNECT_FINANCIAL_MARKER"

type restoreMarker struct {
	RestoreGeneration int64  `json:"restore_generation"`
	AdvancedAt        string `json:"advanced_at"`
	ManifestSHA256    string `json:"manifest_sha256"`
}

// A missing marker is normal (never restored) and reported as absent; the database decides what absence means.
// An unreadable marker is returned as an error: at generation zero absence means nothing, so a permission error read as
// absent hid a real restore on PRE-LIVE. The marker is the only signal that catches a pg_restore into the existing
// cluster (system_identifier is unchanged), so failing to read it must not be swallowed.
func ReadRestoreMarker() (generation int64, present bool, err error) {
	path := os.Getenv(EnvMarkerPath)
	if path == "" {
		path = MarkerPath
	}
	b, rerr := os.ReadFile(path)
	if rerr != nil {
		if errors.Is(rerr, fs.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("the restore marker at %s exists but could not be read: %w", path, rerr)
	}
	var m restoreMarker
	if jerr := json.Unmarshal(b, &m); jerr != nil {
		return 0, false, fmt.Errorf("the restore marker at %s is not readable JSON: %w", path, jerr)
	}
	if m.RestoreGeneration < 0 {
		return 0, false, fmt.Errorf("the restore marker at %s reports a negative generation (%d)",
			path, m.RestoreGeneration)
	}
	return m.RestoreGeneration, true, nil
}

// ReconcileEpoch runs per site at startup before any financial worker. Neither signal suffices alone: the management
// marker catches the supported pg_restore into the existing cluster; the system identity catches a new cluster,
// promoted replica or cloned appliance.
// Neither is caller-asserted and both can only cause more holding. Idempotent: a restart returns UNCHANGED without
// writing; otherwise INITIALIZED, RECOVERY_ENTERED or RECOVERY_ACTIVE.
func (e *Engine) ReconcileEpoch(ctx context.Context, tenantID, siteID string) (string, error) {
	ident, err := SystemIdentity(ctx, e.pool)
	if err != nil {
		return "", err
	}
	gen, present, merr := ReadRestoreMarker()
	if merr != nil {
		// Refuse to conclude: an unreadable marker cannot be reported as "nothing was restored". The caller logs loudly
		// while Phase 4 is dark and exits once transmission is enabled (cmd/edged/financial_epoch_reconcile.go).
		return "", fail(ErrRepo, merr.Error())
	}
	var out string
	if err := e.pool.QueryRow(ctx,
		`SELECT iam_v2.p4_reconcile_financial_epoch_v2($1,$2,$3,$4,$5)`,
		tenantID, siteID, ident, gen, present).Scan(&out); err != nil {
		return "", classify(err)
	}
	return out, nil
}

func (e *Engine) CurrentRestoreGeneration(ctx context.Context, tenantID, siteID string) (int64, error) {
	var g int64
	if err := e.pool.QueryRow(ctx,
		`SELECT iam_v2.p4_current_restore_generation($1,$2)`, tenantID, siteID).Scan(&g); err != nil {
		return 0, classify(err)
	}
	return g, nil
}

func (e *Engine) RecoveryActive(ctx context.Context, tenantID, siteID string) (bool, error) {
	var b bool
	if err := e.pool.QueryRow(ctx,
		`SELECT iam_v2.p4_financial_recovery_active($1,$2)`, tenantID, siteID).Scan(&b); err != nil {
		return false, classify(err)
	}
	return b, nil
}

func (e *Engine) RecoveryStatusFor(ctx context.Context, tenantID, siteID string) (RecoveryStatus, error) {
	var s RecoveryStatus
	var released *string
	err := e.pool.QueryRow(ctx, `
SELECT epoch, reason, recovery_active, held_total::int, held_open::int,
       entered_at::text, released_at::text
  FROM iam_v2.v_financial_recovery
 WHERE tenant_id=$1 AND site_id=$2
 ORDER BY (released_at IS NULL) DESC, epoch DESC LIMIT 1`, tenantID, siteID).
		Scan(&s.Epoch, &s.Reason, &s.Active, &s.HeldTotal, &s.HeldOpen, &s.EnteredAt, &released)
	if errors.Is(err, pgx.ErrNoRows) {
		return RecoveryStatus{}, nil
	}
	if err != nil {
		return RecoveryStatus{}, classify(err)
	}
	if released != nil {
		s.ReleasedAt = *released
	}
	return s, nil
}

// OpenHolds is bounded by construction: an operator surface that can return the whole financial history is a
// denial-of-service and data-exposure problem.
func (e *Engine) OpenHolds(ctx context.Context, tenantID, siteID string, limit int) ([]Hold, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := e.pool.Query(ctx, `
SELECT id::text, work_kind, work_id::text, held_status, amount_minor, coalesce(currency,''),
       coalesce(resolution,''), held_at::text
  FROM iam_v2.financial_recovery_holds
 WHERE tenant_id=$1 AND site_id=$2 AND resolution IS NULL
 ORDER BY held_at, id LIMIT $3`, tenantID, siteID, limit)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	var out []Hold
	for rows.Next() {
		var h Hold
		if err := rows.Scan(&h.ID, &h.Kind, &h.WorkID, &h.HeldStatus, &h.AmountMinor, &h.Currency,
			&h.Resolution, &h.HeldAt); err != nil {
			return nil, classify(err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (e *Engine) ResolveHold(ctx context.Context, holdID, resolution, actorID, note string) error {
	_, err := e.pool.Exec(ctx,
		`SELECT iam_v2.p4_resolve_recovery_hold($1::uuid,$2::text,$3::uuid,$4::text)`,
		holdID, resolution, actorID, note)
	return classify(err)
}

// ReleaseRecovery: the database refuses while any hold is unresolved; there is no force parameter because there is no
// force path.
func (e *Engine) ReleaseRecovery(ctx context.Context, tenantID, siteID, actorID, note string) (int64, error) {
	var epoch int64
	if err := e.pool.QueryRow(ctx,
		`SELECT iam_v2.p4_release_financial_recovery($1::uuid,$2::uuid,$3::uuid,$4::text)`,
		tenantID, siteID, actorID, note).Scan(&epoch); err != nil {
		return 0, classify(err)
	}
	return epoch, nil
}

// DeclareRecovery holds money movement without a restore; entering is always available, leaving is controlled.
func (e *Engine) DeclareRecovery(ctx context.Context, tenantID, siteID, actorID, reason string) (int64, error) {
	var epoch int64
	if err := e.pool.QueryRow(ctx,
		`SELECT iam_v2.p4_declare_financial_recovery($1::uuid,$2::uuid,$3::uuid,$4::text)`,
		tenantID, siteID, actorID, reason).Scan(&epoch); err != nil {
		return 0, classify(err)
	}
	return epoch, nil
}
