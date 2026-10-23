package signinattempt

// Durable record of every Connect submission. Recording is best-effort: if the insert fails or the sealing
// key is missing, the guest is admitted or refused exactly as otherwise and the failure is only logged; a
// guest must never be kept offline because an audit row could not be written. The rows hold guest
// credential material, so they are purged after the site's retention by a bounded DELETE on a ticker.

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Retention is the fallback when the database has no retention function; it must equal the default of
// iam_v2.data_retention_get(...).sign_in_attempt_days.
const Retention = 30 * 24 * time.Hour

// Attempt is one recorded submission; everything except Sensitive is operational diagnostics.
type Attempt struct {
	ID         string
	TenantID   string
	SiteID     string
	OccurredAt time.Time

	GuestNetworkID   string
	GuestNetworkName string
	PMSInterfaceID   string
	RequestID        string

	SubmittedRoom string
	VerifierKind  VerifierKind

	Result       Result
	MatchedField MatchedField
	MatchedStay  string
	MatchedGuest string

	// What the probe saw: absent, present-but-ineligible and unmatched candidates are different conversations at the desk.
	RoomInMirror           bool
	EligibleStayCandidates int

	PMSTransportStatus       string
	MirrorLastCompleteSyncAt *time.Time
	MirrorAgeSeconds         *int64

	LatencyMS int64

	EntitlementID string
	SessionID     string

	DeviceIP  string
	DeviceMAC string

	// Sensitive is sealed before it is written and is never carried in a log line.
	Sensitive Sensitive
}

// Store holds the keyring, so only a service trusted with the appliance-local key (scd) constructs one.
type Store struct {
	pool  *pgxpool.Pool
	kr    Keyring
	keyID string
}

func NewStore(pool *pgxpool.Pool, kr Keyring, keyID string) *Store {
	return &Store{pool: pool, kr: kr, keyID: keyID}
}

// Record returns an error only for the operator's log; a caller must not fail a guest's request on it.
// When the key is missing the row is still written with the sensitive columns NULL, because refusing to
// record leaves nothing and plaintext would put guest credentials in backups.
func (s *Store) Record(ctx context.Context, a Attempt) (string, error) {
	if s == nil || s.pool == nil {
		return "", nil
	}
	var id string
	if err := s.pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		return "", err
	}

	// An unclassified attempt (e.g. an undecodable body) must be UNKNOWN: the column is NOT NULL with a
	// closed CHECK, and coercing here spares each call site from remembering (early exits once wrote nothing).
	if a.VerifierKind == "" {
		a.VerifierKind = VerifierUnknown
	}

	var ct, nonce []byte
	var keyID *string
	var cipherVersion *int
	if !a.Sensitive.Empty() {
		sealed, err := Seal(s.kr, s.keyID, a.TenantID, a.SiteID, id, a.Sensitive)
		if err != nil {
			// Log without the value: the panel will be empty, and logs must never hold this material.
			slog.Error("sign-in attempt recorded without its sensitive half: the sealing key is unavailable",
				"err", err, "attempt", id)
		} else {
			ct, nonce = sealed.Ciphertext, sealed.Nonce
			k, v := sealed.EncryptionKey, sealed.CipherVersion
			keyID, cipherVersion = &k, &v
		}
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO iam_v2.sign_in_attempts
		  (id, tenant_id, site_id, occurred_at, guest_network_id, guest_network_name, pms_interface_id,
		   request_id, submitted_room, verifier_kind, result, matched_field, matched_stay_id, matched_guest_id,
		   room_in_mirror, eligible_stay_candidates, pms_transport_status, mirror_last_complete_sync_at,
		   mirror_age_seconds, latency_ms, entitlement_id, session_id, device_ip, device_mac,
		   sensitive_ciphertext, sensitive_nonce, encryption_key_id, cipher_version)
		VALUES ($1::uuid,$2::uuid,$3::uuid, COALESCE($4, now()), NULLIF($5,'')::uuid, NULLIF($6,''),
		        NULLIF($7,'')::uuid, NULLIF($8,'')::uuid, NULLIF($9,''), $10, $11, NULLIF($12,''),
		        NULLIF($13,'')::uuid, NULLIF($14,'')::uuid, $15, $16, NULLIF($17,''), $18, $19, $20,
		        NULLIF($21,'')::uuid, NULLIF($22,'')::uuid, NULLIF($23,'')::inet, NULLIF($24,'')::macaddr,
		        $25, $26, $27::uuid, $28)`,
		id, a.TenantID, a.SiteID, nullTime(a.OccurredAt), a.GuestNetworkID, a.GuestNetworkName,
		a.PMSInterfaceID, a.RequestID, a.SubmittedRoom, string(a.VerifierKind), string(a.Result),
		string(a.MatchedField), a.MatchedStay, a.MatchedGuest, a.RoomInMirror, a.EligibleStayCandidates,
		a.PMSTransportStatus, a.MirrorLastCompleteSyncAt, a.MirrorAgeSeconds, a.LatencyMS,
		a.EntitlementID, a.SessionID, a.DeviceIP, a.DeviceMAC,
		ct, nonce, keyID, cipherVersion)
	if err != nil {
		return "", err
	}
	return id, nil
}

// OpenSensitive scopes by tenant and site in the WHERE clause, so another site's id returns ErrNoRows,
// indistinguishable from a missing id, and cannot be probed.
func (s *Store) OpenSensitive(ctx context.Context, tenantID, siteID, attemptID string) (Sensitive, bool, error) {
	var out Sensitive
	var ct, nonce []byte
	var keyID *string
	var cipherVersion *int
	err := s.pool.QueryRow(ctx, `
		SELECT sensitive_ciphertext, sensitive_nonce, encryption_key_id::text, cipher_version
		  FROM iam_v2.sign_in_attempts
		 WHERE tenant_id=$1 AND site_id=$2 AND id=$3::uuid`,
		tenantID, siteID, attemptID).Scan(&ct, &nonce, &keyID, &cipherVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if len(ct) == 0 || keyID == nil || cipherVersion == nil {
		// Recorded while the key was unavailable.
		return out, false, nil
	}
	sensitive, err := Open(s.kr, Sealed{
		Ciphertext: ct, Nonce: nonce, EncryptionKey: *keyID, CipherVersion: *cipherVersion,
	}, tenantID, siteID, attemptID)
	if err != nil {
		return out, false, err
	}
	return sensitive, true, nil
}

// Complete stamps the entitlement and session, or a terminal grant outcome, via a SECURITY DEFINER
// operation because svc_scd holds no UPDATE (it could otherwise rewrite the evidence of its own refusals).
// It acts only on a VERIFIED row with no session, so it is idempotent and never turns a refusal into a success.
func (s *Store) Complete(ctx context.Context, tenantID, siteID, requestID string, result Result, entitlementID, sessionID string) error {
	if s == nil || s.pool == nil || requestID == "" {
		return nil
	}
	var rows int
	return s.pool.QueryRow(ctx,
		`SELECT iam_v2.complete_sign_in_attempt($1::uuid,$2::uuid,$3::uuid,$4,NULLIF($5,'')::uuid,NULLIF($6,'')::uuid)`,
		tenantID, siteID, requestID, string(result), entitlementID, sessionID).Scan(&rows)
}

// Purge is deliberately narrow: one table, one column, nothing describing a stay, guest, entitlement,
// session, financial record or the general audit log, which have their own lifecycles.
func (s *Store) Purge(ctx context.Context, now time.Time) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, nil
	}
	ct, err := s.pool.Exec(ctx, `
		DELETE FROM iam_v2.sign_in_attempts a
		 USING (SELECT DISTINCT tenant_id, site_id FROM iam_v2.sign_in_attempts) s
		 CROSS JOIN LATERAL iam_v2.data_retention_get(s.tenant_id, s.site_id) r
		 WHERE a.tenant_id = s.tenant_id AND a.site_id = s.site_id
		   AND a.occurred_at < $1::timestamptz - make_interval(days => r.sign_in_attempt_days)`, now)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42883" {
		ct, err = s.pool.Exec(ctx, `DELETE FROM iam_v2.sign_in_attempts WHERE occurred_at < $1`, now.Add(-Retention))
	}
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

func (s *Store) RunPurge(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Hour
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		// Sweep on entry too, or an appliance restarted more often than the interval would never purge.
		if n, err := s.Purge(ctx, time.Now()); err != nil {
			if ctx.Err() == nil {
				slog.Warn("sign-in attempt purge failed", "err", err)
			}
		} else if n > 0 {
			slog.Info("sign-in attempts purged", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func NormalizeMAC(mac net.HardwareAddr) string {
	if len(mac) == 0 {
		return ""
	}
	return mac.String()
}
