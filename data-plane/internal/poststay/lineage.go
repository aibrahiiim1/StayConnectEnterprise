package poststay

// The guest-facing post-stay flow never accepts a Stay, room, PMS interface or profile from the client;
// both issuance and verification derive them from the server-resolved device. A client-supplied Stay id
// would let anyone mint a credential for a room they were never in, and a client-supplied profile id would
// be an enumeration oracle. A device never authorized on the stay gets the uniform non-success.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ErrNoLineage must never be distinguishable from a wrong PIN at the API boundary.
var ErrNoLineage = errors.New("poststay: no authenticated lineage for this device")

// EligibleStayForDevice returns the Stay whose current episode this device may mint a post-stay profile
// for: one where the device holds an authorization on the Stay's entitlement. Only IN_HOUSE or CHECKED_OUT
// stays qualify.
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

// CandidateProfilesForDevice lists currently authenticable profiles whose origin Stay this device was
// authorized on. A device may have been on several stays; the list is bounded to cap hashing work per attempt.
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

// VerifyForDevice tries every candidate without stopping early, so timing does not reveal the match's
// position; with no match a dummy hash runs once so "no identity" costs the same as "wrong PIN". The
// throttle is charged once per attempt, not per candidate.
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
