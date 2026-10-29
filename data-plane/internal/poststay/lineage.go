package poststay

// The guest-facing post-stay flow never accepts a Stay, profile or PMS interface from the client. It derives the
// candidate stays from the server-resolved device (the stays it was authorised on) and compares what the guest typed
// with those stays' own records. A device that was never on a stay finds nothing, unless it also gives the last name
// on that stay, in which case the stay is looked up by room, reservation and name together and must be unique.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/namenorm"
	"github.com/stayconnect/enterprise/data-plane/internal/throttle"
)

// ErrNoLineage must never be distinguishable from a wrong answer at the API boundary.
var ErrNoLineage = errors.New("poststay: no authenticated lineage for this device")

// maxTypedLen bounds every typed value; a longer one cannot be a room, a reservation or a name.
const maxTypedLen = 64

// VerifyRequest is a guest's attempt. Nothing in it names a stay, a profile or an interface. Room and Reservation are
// always required; LastName is required only from a device with no stay of its own (it is ignored otherwise).
type VerifyRequest struct {
	Tenant, Site string
	// Device is the device id scd resolved; IP is the source address. Both are throttle dimensions (hashed by the store).
	Device, IP                  string
	Room, Reservation, LastName string
	// WindowDays is how long after check-out a stay can still be signed in to (the site's post_stay_signin_days setting,
	// migration 0179). Zero or less means DefaultWindowDays: an unset window never means "for ever".
	WindowDays int
}

type candidate struct {
	stay, room, reservation string
	version                 int
}

// candidateStays lists the stays this device was authorised on that are in a state post-stay sign-in can use. Bounded to
// cap the work of one attempt. The room and reservation come from the stay's own record.
func (s *Store) candidateStays(ctx context.Context, tenant, site, device string, windowDays int) ([]candidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT st.id::text, st.lifecycle_version, COALESCE(st.normalized_room_number,''), COALESCE(st.external_reservation_id,'')
		  FROM (SELECT DISTINCT ON (e.stay_id) e.stay_id, eda.authorized_at
		          FROM iam_v2.entitlement_device_authorizations eda
		          JOIN iam_v2.entitlements e
		            ON e.tenant_id=eda.tenant_id AND e.site_id=eda.site_id AND e.id=eda.entitlement_id
		         WHERE eda.tenant_id=$1 AND eda.site_id=$2 AND eda.device_id=$3 AND e.stay_id IS NOT NULL
		         ORDER BY e.stay_id, eda.authorized_at DESC) lineage
		  JOIN iam_v2.stays st ON st.tenant_id=$1 AND st.site_id=$2 AND st.id=lineage.stay_id
		 WHERE st.status IN ('CHECKED_OUT','POST_STAY_ACTIVE')
		   AND st.effective_checkout_at > now() - make_interval(days => $4)
		 ORDER BY lineage.authorized_at DESC
		 LIMIT 5`, tenant, site, device, windowDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.stay, &c.version, &c.room, &c.reservation); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// sameText compares two values in constant time regardless of their lengths (both are reduced to fixed-length digests
// first) and never matches an empty value, so a stay with no room on record cannot be "matched" by typing nothing.
func sameText(stored, typed string) bool {
	if stored == "" || typed == "" {
		// Still do the comparison's work so the empty case costs the same.
		a, b := sha256.Sum256([]byte(stored)), sha256.Sum256([]byte(typed))
		_ = subtle.ConstantTimeCompare(a[:], b[:])
		return false
	}
	a, b := sha256.Sum256([]byte(stored)), sha256.Sum256([]byte(typed))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// chargeThrottle charges one attempt against every dimension before any comparison is made, so a locked-out device
// learns nothing and a refusal costs the same as a wrong answer. An unreachable store denies the attempt. The identity
// rule is keyed by the stay identity (episode) AND the device; with no candidate it is keyed by the device alone, so a
// device with no stay is limited as tightly as one with several.
func (s *Store) chargeThrottle(ctx context.Context, req VerifyRequest, cands []candidate) error {
	rules := []throttle.Rule{
		{Kind: throttle.ScopeDevice, Value: req.Device, Method: ThrottleMethod, Limit: 10, Block: 15 * time.Minute},
		{Kind: throttle.ScopeEndpoint, Value: "post-stay-pin", Method: ThrottleMethod, Limit: 200},
	}
	if strings.TrimSpace(req.IP) != "" {
		rules = append(rules, throttle.Rule{Kind: throttle.ScopeIP, Value: req.IP, Method: ThrottleMethod, Limit: 20, Block: 15 * time.Minute})
	}
	if len(cands) == 0 {
		rules = append(rules, throttle.Rule{Kind: throttle.ScopeIdentity, Value: "new-device|" + req.Device,
			Method: ThrottleMethod, Limit: 5, Block: 15 * time.Minute})
	}
	for _, c := range cands {
		rules = append(rules, throttle.Rule{Kind: throttle.ScopeIdentity,
			Value:  "stay:" + c.stay + ":" + strconv.Itoa(c.version) + "|device:" + req.Device,
			Method: ThrottleMethod, Limit: 5, Block: 15 * time.Minute})
	}
	d, err := s.thr.Allow(ctx, rules...)
	if err != nil || !d.Allowed {
		return ErrThrottled
	}
	return nil
}

// Verify checks the room and reservation (and, from a new device, the last name) against the original stay and returns
// the profile id of that stay's episode. Every failure is ErrNotAuthenticable (or ErrThrottled) and the work done is the
// same on every path: the candidates are always read, every candidate is always compared, and the new-device lookup runs
// whenever there is no candidate.
func (s *Store) Verify(ctx context.Context, req VerifyRequest) (string, error) {
	windowDays := req.WindowDays
	if windowDays <= 0 {
		windowDays = DefaultWindowDays
	}
	cands, err := s.candidateStays(ctx, req.Tenant, req.Site, req.Device, windowDays)
	if err != nil {
		return "", err
	}
	if s.thr != nil {
		if err := s.chargeThrottle(ctx, req, cands); err != nil {
			return "", err
		}
	}
	room, reservation, last := namenorm.Room(req.Room), namenorm.Reservation(req.Reservation), namenorm.Name(req.LastName)
	if len(room) > maxTypedLen || len(reservation) > maxTypedLen || len(last) > maxTypedLen {
		return "", ErrNotAuthenticable
	}

	matchedStay := ""
	// Every candidate is compared; none short-circuits, so the position of a match is not visible in the time taken.
	for _, c := range cands {
		roomOK := sameText(namenorm.Room(c.room), room)
		resOK := sameText(namenorm.Reservation(c.reservation), reservation)
		if roomOK && resOK && matchedStay == "" {
			matchedStay = c.stay
		}
	}
	if len(cands) == 0 {
		// A new device: the stay is found by all three facts together, and only if exactly one stay has them.
		var ids []string
		if room != "" && reservation != "" && last != "" {
			rows, err := s.pool.Query(ctx, `
				SELECT st.id::text
				  FROM iam_v2.stays st
				 WHERE st.tenant_id=$1 AND st.site_id=$2 AND st.status IN ('CHECKED_OUT','POST_STAY_ACTIVE')
				   AND st.effective_checkout_at > now() - make_interval(days => $6)
				   AND st.normalized_room_number=$3 AND st.external_reservation_id=$4
				   AND EXISTS (SELECT 1 FROM iam_v2.stay_guests g
				                WHERE g.tenant_id=st.tenant_id AND g.site_id=st.site_id AND g.stay_id=st.id
				                  AND g.last_name_norm=$5)
				 LIMIT 2`, req.Tenant, req.Site, room, reservation, last, windowDays)
			if err != nil {
				return "", err
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return "", err
				}
				ids = append(ids, id)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return "", err
			}
		} else {
			// The same query's cost, with a value that matches nothing.
			rows, err := s.pool.Query(ctx, `SELECT st.id::text FROM iam_v2.stays st
				WHERE st.tenant_id=$1 AND st.site_id=$2 AND st.normalized_room_number=$3 AND false LIMIT 2`, req.Tenant, req.Site, room)
			if err == nil {
				rows.Close()
			}
		}
		if len(ids) == 1 {
			matchedStay = ids[0]
		}
	}
	if matchedStay == "" {
		return "", ErrNotAuthenticable
	}
	return s.ensureProfile(ctx, req.Tenant, req.Site, matchedStay, windowDays)
}
