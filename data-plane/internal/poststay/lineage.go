package poststay

// TRUSTED SERVER-SIDE LINEAGE.
//
// Nothing in the guest-facing post-stay flow accepts a Stay, a room, a PMS interface or a profile from the
// client. Both directions are derived from the DEVICE, which the appliance resolved itself from the packet it
// received — the same identity every other guest path is built on.
//
// Why this is not merely tidy. If issuance took a Stay id, anyone who could guess or observe one could mint a
// post-stay credential for a room they were never in. If verification took a profile id, the endpoint would
// become an enumeration surface: submit ids, watch which ones behave differently, and the system answers "is
// this a real post-stay identity" for free. Deriving both from the device removes the parameter entirely, and
// a removed parameter cannot be validated wrongly.
//
// The cost is deliberate and bounded: a guest who authenticates from a device that was never authorized on
// the stay gets the uniform non-success. That is the correct direction to fail.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ErrNoLineage — this device has no authenticated lineage that would make it eligible. It is never
// distinguishable from a wrong PIN at the API boundary.
var ErrNoLineage = errors.New("poststay: no authenticated lineage for this device")

// EligibleStayForDevice returns the Stay whose CURRENT episode this device may mint a post-stay profile for.
//
// The lineage is: the device holds (or held, within this episode) an authorization on an entitlement whose
// subject is a Stay. That is the durable record of "this device was authenticated on this stay", and it is
// what makes issuance safe to expose without any client-supplied subject.
//
// Only an IN_HOUSE or CHECKED_OUT stay qualifies — before checkout (the guest arranging post-stay access in
// advance) or during Checkout Grace (the ordinary case). A stay that has already converted, been cancelled or
// moved to a new episode yields nothing.
func (s *Store) EligibleStayForDevice(ctx context.Context, tenant, site, device string) (string, error) {
	var stay string
	err := s.pool.QueryRow(ctx, `
		SELECT st.id::text
		  FROM iam_v2.entitlement_device_authorizations eda
		  JOIN iam_v2.entitlements e
		    ON e.tenant_id=eda.tenant_id AND e.site_id=eda.site_id AND e.id=eda.entitlement_id
		  JOIN iam_v2.stays st
		    ON st.tenant_id=e.tenant_id AND st.site_id=e.site_id AND st.id=e.stay_id
		 WHERE eda.tenant_id=$1 AND eda.site_id=$2 AND eda.device_id=$3
		   AND st.status IN ('IN_HOUSE','CHECKED_OUT')
		 ORDER BY eda.authorized_at DESC
		 LIMIT 1`, tenant, site, device).Scan(&stay)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNoLineage
		}
		return "", err
	}
	return stay, nil
}

// CandidateProfilesForDevice returns the post-stay profiles this device may attempt a PIN against: profiles
// that are authenticable RIGHT NOW and whose origin Stay this device was authorized on.
//
// It is a list rather than a single value because a device can legitimately have been on more than one stay
// at this site (a returning guest, a shared family device). It is BOUNDED so that a device with a long
// history cannot turn one PIN attempt into an unbounded amount of hashing — the throttle limits attempts, and
// this limits the work per attempt.
func (s *Store) CandidateProfilesForDevice(ctx context.Context, tenant, site, device string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT psp.id::text, psp.created_at
		  FROM iam_v2.entitlement_device_authorizations eda
		  JOIN iam_v2.entitlements e
		    ON e.tenant_id=eda.tenant_id AND e.site_id=eda.site_id AND e.id=eda.entitlement_id
		  JOIN iam_v2.post_stay_profiles psp
		    ON psp.tenant_id=e.tenant_id AND psp.site_id=e.site_id AND psp.origin_stay_id=e.stay_id
		 WHERE eda.tenant_id=$1 AND eda.site_id=$2 AND eda.device_id=$3
		   AND iam_v2.p5_post_stay_authenticable($1,$2,psp.id)
		 ORDER BY psp.created_at DESC
		 LIMIT 5`, tenant, site, device)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		var ignored any
		if err := rows.Scan(&id, &ignored); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// VerifyForDevice is the guest-facing verification: no profile is named by the caller.
//
// Every candidate is tried, and the loop does NOT stop early on a match — stopping early would make the
// response time reveal the candidate's position, and the position is a fact about the guest's history. When
// no candidate matches, the constant-work dummy hash runs once so that "this device has no post-stay
// identity" costs the same as "this device has one and the PIN was wrong".
//
// The throttle is charged once per ATTEMPT, not per candidate: an attempt is what the guest made.
func (s *Store) VerifyForDevice(ctx context.Context, req VerifyRequest) (string, error) {
	if s.thr != nil {
		if err := s.chargeThrottle(ctx, req); err != nil {
			return "", err
		}
	}
	candidates, err := s.CandidateProfilesForDevice(ctx, req.Tenant, req.Site, req.Device)
	if err != nil {
		return "", err
	}
	pin := NormalizePIN(req.PIN)
	matched := ""
	for _, id := range candidates {
		var hash string
		if err := s.pool.QueryRow(ctx, `SELECT pin_hash FROM iam_v2.post_stay_profiles
			WHERE tenant_id=$1 AND site_id=$2 AND id=$3
			  AND iam_v2.p5_post_stay_authenticable($1,$2,$3)`,
			req.Tenant, req.Site, id).Scan(&hash); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return "", err
		}
		if verifyPIN(pin, hash) && matched == "" {
			matched = id
		}
	}
	if matched == "" {
		_ = verifyPIN(pin, dummyHash)
		return "", ErrNotAuthenticable
	}
	return matched, nil
}
