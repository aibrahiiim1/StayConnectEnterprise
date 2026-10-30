// Package poststay is the Post-Stay sign-in: a guest who has checked out comes back with the ROOM NUMBER and the
// RESERVATION NUMBER of the stay they left (Product-Owner decision on audit item AD-36, replacing the earlier PIN).
//
// The properties that make that safe, and that the tests attack:
//
//   - The stay is never found from what was typed. The candidates are the stays THIS DEVICE was authorised on; the typed
//     room and reservation are compared with those stays' own records in constant time. Typing another guest's room and
//     reservation from a device that was never on that stay finds nothing (see below for the new-device route).
//   - A device with no such stay (a new device) must give one more fact from the stay record, the last name on it, and
//     then the stay is looked up by all three together; one match, never several.
//   - Every refusal is the same refusal, after the same work: wrong room, wrong reservation, no stay, revoked,
//     expired, moved-on episode, locked out. Nothing says which part was wrong or whether a stay exists.
//   - The lockout is counted per stay identity (the post-stay profile of the episode) AND device, so one device cannot
//     lock out another device's attempts for the same stay, and a guesser's device is locked out on its own.
//   - The profile is per stay EPISODE (UNIQUE(origin_stay_id, origin_lifecycle_version)), never per room, so it cannot
//     be inherited by the next occupant: a reinstatement orphans it in the database.
//   - A device never gets an Entitlement of its own and never resets what the stay spent. The stay has ONE live
//     Entitlement: a returning device or a further device JOINS it (the plan's device limit decides), and only when the
//     stay has none (it ended at check-out) is the Post-Stay BENEFIT granted -- once per stay, a new Entitlement from
//     the zero-price POST_STAY package, by the package's design and not by the device (see convert.go).
//   - Brute-force limits are the site's settings (limits.go, migration 0190); a client that rotates its device
//     identity is bounded by the room and reservation it must guess, not by the identity it can change for free.
//   - Every refusal, grant and join is a row in the audit log without guest data (audit.go).
//   - v1 grants zero-price packages only. No PIN exists any more: the pin_* columns of older profiles are historical
//     evidence of how those profiles were issued and are never read for a decision.
package poststay

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/throttle"
	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

// ThrottleMethod is its own method so a post-stay brute force neither consumes nor is masked by the budget for ordinary
// portal authentication. The name is historical (the table's CHECK lists it); it counts room + reservation attempts now.
const ThrottleMethod = "post_stay_pin"

const CapPostStayIdentity = "post_stay_identity"

// ProfileWindow is how long a profile created by a successful check stays usable for the post-stay conversion.
const ProfileWindow = 24 * time.Hour

// DefaultWindowDays is how long after check-out a guest may still sign in with their room and reservation when the
// caller names no window (the setting's default, migration 0179).
const DefaultWindowDays = 7

var (
	// ErrNotAuthenticable covers every failure alike; any distinction would be an oracle.
	ErrNotAuthenticable = errors.New("poststay: not authenticable")
	// ErrThrottled is separate so the caller can send Retry-After; the guest-visible envelope stays uniform.
	ErrThrottled   = errors.New("poststay: throttled")
	ErrNotEligible = errors.New("poststay: stay is not eligible for a post-stay profile")
	// ErrSettlementRequired: v1 refuses priced packages rather than silently granting them free.
	ErrSettlementRequired  = errors.New("poststay: post-stay v1 grants zero-price packages only")
	ErrPackageNotGrantable = errors.New("poststay: package revision is not a grantable post-stay package")
	ErrAlreadyConverted    = errors.New("poststay: the once-per-stay post-stay benefit was already granted and is no longer available")
	// ErrEntitlementNotJoinable: the stay has a live Entitlement that a device cannot join now (not active, or its window has
	// ended). Granting a second one would break one-live-Entitlement-per-Stay and reset what it spent, so nothing is granted.
	ErrEntitlementNotJoinable = errors.New("poststay: the stay's entitlement cannot be joined now")
	// ErrDeviceLimit: the entitlement's plan allows no more devices; a device cannot add itself past it.
	ErrDeviceLimit = errors.New("poststay: the entitlement already has its maximum number of devices")
)

type Store struct {
	pool *pgxpool.Pool
	thr  *throttle.Store
	now  func() time.Time
	aud  *auditor
}

func New(pool *pgxpool.Pool, thr *throttle.Store) *Store {
	return &Store{pool: pool, thr: thr, now: time.Now, aud: newAuditor()}
}

// SetClock is for tests only.
func (s *Store) SetClock(f func() time.Time) { s.now = f }

// RevokeRequest revokes a profile; revocation is a state, not a deletion, so the row stays as the record.
type RevokeRequest struct {
	Tenant, Site string
	Profile      string
	Operator     string
	Reason       string
}

func (s *Store) Revoke(ctx context.Context, req RevokeRequest) error {
	if strings.TrimSpace(req.Operator) == "" || strings.TrimSpace(req.Reason) == "" {
		return ErrNotEligible
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := writerguard.OpenPhase5(ctx, tx, CapPostStayIdentity); err != nil {
		return err
	}
	ct, err := tx.Exec(ctx, `UPDATE iam_v2.post_stay_profiles
		SET status='REVOKED', revoked_at=now(), revoked_by=$4, revoke_reason=$5
		WHERE tenant_id=$1 AND site_id=$2 AND id=$3 AND status='ACTIVE'`,
		req.Tenant, req.Site, req.Profile, req.Operator, req.Reason)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotAuthenticable
	}
	return tx.Commit(ctx)
}

// ensureProfile returns the post-stay profile of the stay's current episode, creating it on the first successful check.
// It never returns a profile that cannot authenticate (revoked, expired, a moved-on episode, a stay that has not checked
// out): that is the same refusal as a wrong answer. The insert runs in the controlled-writer scope the table requires,
// and a concurrent creator is absorbed by the episode's uniqueness.
func (s *Store) ensureProfile(ctx context.Context, tenant, site, stay string, windowDays int) (string, error) {
	// The common case after the first device: the episode already has its profile (the first device's conversion may even
	// have moved the stay on to POST_STAY_ACTIVE, which the insert below cannot accept). Read before writing.
	if profile, err := s.currentProfile(ctx, tenant, site, stay); err != nil {
		return "", err
	} else if profile != "" {
		return s.authenticable(ctx, tenant, site, profile)
	}
	profile, err := s.insertProfile(ctx, tenant, site, stay, windowDays)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "P0001" {
			// Two devices of one stay racing: the other one created the profile and converted (the stay left CHECKED_OUT)
			// between our read and our insert, and the profile guard refused ours. Take the one that exists.
			if cur, rerr := s.currentProfile(ctx, tenant, site, stay); rerr == nil && cur != "" {
				return s.authenticable(ctx, tenant, site, cur)
			}
			return "", ErrNotAuthenticable
		}
		return "", err
	}
	return s.authenticable(ctx, tenant, site, profile)
}

// currentProfile is the profile of the stay's CURRENT episode, or "".
func (s *Store) currentProfile(ctx context.Context, tenant, site, stay string) (string, error) {
	var profile string
	err := s.pool.QueryRow(ctx, `SELECT p.id::text FROM iam_v2.post_stay_profiles p
		JOIN iam_v2.stays st ON st.tenant_id=p.tenant_id AND st.site_id=p.site_id AND st.id=p.origin_stay_id
		                    AND st.lifecycle_version=p.origin_lifecycle_version
		 WHERE p.tenant_id=$1 AND p.site_id=$2 AND p.origin_stay_id=$3`, tenant, site, stay).Scan(&profile)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return profile, err
}

func (s *Store) authenticable(ctx context.Context, tenant, site, profile string) (string, error) {
	var ok bool
	if err := s.pool.QueryRow(ctx, `SELECT iam_v2.p5_post_stay_authenticable($1,$2,$3)`, tenant, site, profile).Scan(&ok); err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNotAuthenticable
	}
	return profile, nil
}

func (s *Store) insertProfile(ctx context.Context, tenant, site, stay string, windowDays int) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := writerguard.OpenPhase5(ctx, tx, CapPostStayIdentity); err != nil {
		return "", err
	}
	var profile string
	err = tx.QueryRow(ctx, `INSERT INTO iam_v2.post_stay_profiles
		(tenant_id, site_id, origin_stay_id, origin_lifecycle_version, valid_until, issued_via, pin_revealed_at)
		SELECT st.tenant_id, st.site_id, st.id, st.lifecycle_version, now() + make_interval(secs => $4), 'ROOM_RESERVATION', NULL
		  FROM iam_v2.stays st
		 WHERE st.tenant_id=$1 AND st.site_id=$2 AND st.id=$3 AND st.status = 'CHECKED_OUT'
		   AND st.effective_checkout_at > now() - make_interval(days => $5)
		ON CONFLICT (origin_stay_id, origin_lifecycle_version) DO NOTHING
		RETURNING id::text`, tenant, site, stay, ProfileWindow.Seconds(), windowDays).Scan(&profile)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already there (or the stay is not CHECKED_OUT): read the current episode's profile, whoever created it.
		err = tx.QueryRow(ctx, `SELECT p.id::text FROM iam_v2.post_stay_profiles p
			JOIN iam_v2.stays st ON st.tenant_id=p.tenant_id AND st.site_id=p.site_id AND st.id=p.origin_stay_id
			                    AND st.lifecycle_version=p.origin_lifecycle_version
			 WHERE p.tenant_id=$1 AND p.site_id=$2 AND p.origin_stay_id=$3`, tenant, site, stay).Scan(&profile)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotAuthenticable
		}
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return profile, nil
}
