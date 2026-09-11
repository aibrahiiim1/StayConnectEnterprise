package signinattempt

// THE DURABLE RECORD OF EVERY DELIBERATE CONNECT SUBMISSION.
//
// Before this existed, a failed room sign-in left nothing behind that an operator could look at. A refusal
// that never reached a stay recorded no resolution at all, and the ones that did recorded an outcome code
// with no submission beside it — so the only honest answer to "why can't this guest get online" was to ask
// them to try again while somebody watched the journal.
//
// THE WRITE NEVER FAILS THE GUEST. Recording is best-effort by construction: a guest's access does not depend
// on the diagnostics about it. If the database refuses the insert, or the sealing key is missing, the guest is
// admitted or refused exactly as they would have been and the failure is logged for the operator. The one
// thing that must never happen is the reverse — a guest kept off the internet because an audit row could not
// be written.
//
// THE PURGE IS PART OF THE FEATURE, NOT AN AFTERTHOUGHT. These rows hold guest credential material, so they
// are retained for thirty days and then deleted. The mechanism is the one this appliance already uses for the
// durable throttle: a bounded DELETE on a ticker in the owning service. It touches nothing but its own table.

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Retention is how long an attempt record lives. The Product Owner set thirty days: long enough to
// investigate a complaint that reaches the desk a fortnight later, short enough that the appliance is not a
// standing archive of what guests typed.
const Retention = 30 * 24 * time.Hour

// Attempt is one recorded submission. Everything here except Sensitive is operational diagnostics.
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

	// RoomInMirror and EligibleStayCandidates are what the probe SAW, and they are what turns a bare
	// "not verified" into an answer: a room that is absent, present-but-ineligible, or present with
	// candidates the value did not match are three different conversations at the desk.
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

// Store reads and writes attempt records. It holds the keyring, so only a service trusted with the
// appliance-local key material constructs one — in practice scd, which already holds the voucher DEK for the
// same reason.
type Store struct {
	pool  *pgxpool.Pool
	kr    Keyring
	keyID string
}

func NewStore(pool *pgxpool.Pool, kr Keyring, keyID string) *Store {
	return &Store{pool: pool, kr: kr, keyID: keyID}
}

// Record writes one attempt. It returns the id it wrote so a caller can correlate, and an error only for the
// operator's log — a caller must not fail a guest's request on it.
//
// WHEN THE KEY IS MISSING the row is still written, with the sensitive columns NULL. That is a deliberate
// choice between two bad options: refusing to record leaves the operator with nothing at all, and writing
// plaintext would put guest credentials in a database backup. A row that says "this happened, and the
// submitted value is unavailable on this appliance" is the honest third answer, and the detail panel says so
// in words rather than showing a blank.
func (s *Store) Record(ctx context.Context, a Attempt) (string, error) {
	if s == nil || s.pool == nil {
		return "", nil
	}
	var id string
	if err := s.pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		return "", err
	}

	var ct, nonce []byte
	var keyID *string
	var cipherVersion *int
	if !a.Sensitive.Empty() {
		sealed, err := Seal(s.kr, s.keyID, a.TenantID, a.SiteID, id, a.Sensitive)
		if err != nil {
			// Loud, once, and without the value: an operator needs to know the panel will be empty, and the
			// log is exactly the place this material must never appear.
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

// OpenSensitive decrypts the sealed half of ONE attempt, scoped to the tenant and site the caller serves.
//
// The scoping is in the WHERE clause rather than checked afterwards on purpose: an id from another site
// returns pgx.ErrNoRows, which is indistinguishable from an id that does not exist, so an operator cannot
// probe for the existence of another site's attempts by watching which ids error differently.
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
		// The attempt exists and has no sensitive half — recorded while the key was unavailable.
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

// Complete stamps the entitlement and session a proved identity ended in, one call later, or moves that row
// to a terminal grant outcome.
//
// It goes through a scoped SECURITY DEFINER operation rather than an UPDATE, because svc_scd deliberately
// holds no UPDATE on the table: a service able to rewrite an attempt is a service able to rewrite the
// evidence of its own refusals. The operation acts only on a row that is still VERIFIED with no session, so
// calling it twice is a no-op and it can never turn a recorded refusal into a success.
func (s *Store) Complete(ctx context.Context, tenantID, siteID, requestID string, result Result, entitlementID, sessionID string) error {
	if s == nil || s.pool == nil || requestID == "" {
		return nil
	}
	var rows int
	return s.pool.QueryRow(ctx,
		`SELECT iam_v2.complete_sign_in_attempt($1::uuid,$2::uuid,$3::uuid,$4,NULLIF($5,'')::uuid,NULLIF($6,'')::uuid)`,
		tenantID, siteID, requestID, string(result), entitlementID, sessionID).Scan(&rows)
}

// Purge deletes attempts older than Retention and returns how many went.
//
// It is deliberately narrow. It names ONE table, filters on ONE column, and touches nothing that describes a
// stay, a guest, an entitlement, a session, a financial record or the general audit log — those have their
// own lifecycles and their own authorities, and a retention sweep that reached into them would be destroying
// evidence rather than expiring it.
func (s *Store) Purge(ctx context.Context, now time.Time) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, nil
	}
	ct, err := s.pool.Exec(ctx,
		`DELETE FROM iam_v2.sign_in_attempts WHERE occurred_at < $1`, now.Add(-Retention))
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// RunPurge purges on a ticker until ctx is cancelled. Same shape as the durable throttle's cleanup loop,
// which is the appliance's established pattern for bounded retention: the owning service does it, in Go,
// rather than a database scheduler nobody would remember exists.
func (s *Store) RunPurge(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Hour
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		// Sweep on entry as well as on the tick: an appliance that is restarted more often than the interval
		// would otherwise never purge at all.
		if n, err := s.Purge(ctx, time.Now()); err != nil {
			if ctx.Err() == nil {
				slog.Warn("sign-in attempt purge failed", "err", err)
			}
		} else if n > 0 {
			slog.Info("sign-in attempts purged", "rows", n, "retention_days", int(Retention.Hours()/24))
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

// NormalizeMAC renders a hardware address for storage, or "" when there is none to record.
func NormalizeMAC(mac net.HardwareAddr) string {
	if len(mac) == 0 {
		return ""
	}
	return mac.String()
}
